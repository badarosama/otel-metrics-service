package main

import (
	"crypto/tls"
	"crypto/x509"
	grpcmiddleware "github.com/grpc-ecosystem/go-grpc-middleware"
	grpcrecovery "github.com/grpc-ecosystem/go-grpc-middleware/recovery"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/viper"
	pb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
	"io/ioutil"
	"log"
	"metrics/server/pb/pv"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

const (
	pathOfConfigFile = "./server/config.yaml"
	cacheSize        = 10
)

type server struct {
	pb.UnimplementedMetricsServiceServer
	pv.UnimplementedVersionServiceServer
	lastSuccessfulRequests *CircularQueue
	lastErrorRequests      *CircularQueue
	cacheMutex             sync.Mutex
	logger                 *zap.Logger
}

type LoggerConfig struct {
	Level string `mapstructure:"level"`
}

// Refer to doc: https://grpc.io/docs/guides/keepalive/
// https://github.com/grpc/grpc-go/blob/master/examples/features/keepalive/server/main.go
var kaep = keepalive.EnforcementPolicy{
	MinTime:             5 * time.Second, // If a client pings more than once every 5 seconds, terminate the connection
	PermitWithoutStream: true,            // Allow pings even when there are no active streams
}

// Refer to doc: https://grpc.io/docs/guides/keepalive/
// https://github.com/grpc/grpc-go/blob/master/examples/features/keepalive/server/main.go
var kasp = keepalive.ServerParameters{
	MaxConnectionIdle:     15 * time.Second, // If a client is idle for 15 seconds, send a GOAWAY
	MaxConnectionAge:      30 * time.Second, // If any connection is alive for more than 30 seconds, send a GOAWAY
	MaxConnectionAgeGrace: 5 * time.Second,  // Allow 5 seconds for pending RPCs to complete before forcibly closing connections
	Time:                  5 * time.Second,  // Ping the client if it is idle for 5 seconds to ensure the connection is still active
	Timeout:               1 * time.Second,  // Wait 1 second for the ping ack before assuming the connection is dead
}

func loadConfig(path string) (LoggerConfig, error) {
	// Load configuration from file
	viper.SetConfigFile(path)
	if err := viper.ReadInConfig(); err != nil {
		return LoggerConfig{}, err
	}
	// Unmarshal configuration into struct
	var loggerConfig LoggerConfig
	if err := viper.Unmarshal(&loggerConfig); err != nil {
		return LoggerConfig{}, err
	}

	return loggerConfig, nil
}

func initLogger(config LoggerConfig) (*zap.Logger, error) {
	var level zap.AtomicLevel
	if err := level.UnmarshalText([]byte(config.Level)); err != nil {
		return nil, err
	}

	cfg := zap.NewProductionConfig()
	cfg.Level = level
	cfg.EncoderConfig.EncodeTime = zapcore.ISO8601TimeEncoder // Human-readable timestamps
	cfg.OutputPaths = []string{"stdout", "./logs/server.log"}
	cfg.ErrorOutputPaths = []string{"stderr"}
	cfg.Sampling = &zap.SamplingConfig{
		Initial:    100,
		Thereafter: 100,
	}

	logger, err := cfg.Build(zap.AddCallerSkip(1)) // Skip the zap library's frames in the call stack
	if err != nil {
		return nil, err
	}

	return logger, nil
}

func getServerCertAndPool() (tls.Certificate, *x509.CertPool) {
	// load certs
	caPem, err := ioutil.ReadFile("certs/ca.crt")
	// create cert pool and append ca's cert
	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caPem) {
		log.Fatal(err)
	}
	// read server cert & key
	serverCert, err := tls.LoadX509KeyPair("certs/server.crt", "certs/server.key")

	if err != nil {
		log.Fatal(err)
	}

	return serverCert, certPool
}

func configureLogger() *zap.Logger {
	// Ensure the logs directory exists
	err := os.MkdirAll("./logs", os.ModePerm)
	if err != nil {
		log.Fatalf("Failed to create log directory: %v", err)
	}

	// Initialize logger based on configuration
	loggerConfig, err := loadConfig(pathOfConfigFile)
	if err != nil {
		log.Fatalf("Failed to load configs: %v", err)
	}

	logger, err := initLogger(loggerConfig)
	if err != nil {
		log.Fatalf("Failed to create logger: %v", err)
	}

	return logger
}

func main() {
	// Setup logger.
	logger := configureLogger()
	defer logger.Sync()

	listener, err := net.Listen("tcp", ":8080")
	if err != nil {
		logger.Fatal("Failed to listen", zap.Error(err))
	}

	serverCert, certPool := getServerCertAndPool()
	// configuration of the certificates
	conf := &tls.Config{
		Certificates: []tls.Certificate{serverCert},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    certPool,
	}

	tlsCredentials := credentials.NewTLS(conf)
	s := grpc.NewServer(
		grpc.Creds(tlsCredentials),
		grpc.KeepaliveEnforcementPolicy(kaep),
		grpc.KeepaliveParams(kasp),
		grpc.ChainUnaryInterceptor(
			// Custom unary interceptor defined in middleware.go
			UnaryInterceptorPrometheus,
			// Recovery interceptor to handle panics
			grpcmiddleware.ChainUnaryServer(
				grpcrecovery.UnaryServerInterceptor(),
			)),
	)
	// Initialize the server struct with the logger and cache.
	srv := &server{
		logger:                 logger,
		lastErrorRequests:      NewCircularQueue(cacheSize),
		lastSuccessfulRequests: NewCircularQueue(cacheSize),
	}

	pb.RegisterMetricsServiceServer(s, srv)
	pv.RegisterVersionServiceServer(s, srv)
	reflection.Register(s)

	// Register prometheus for instrumentation.
	http.Handle("/metrics", promhttp.Handler())
	go func() {
		http.ListenAndServe(":9091", nil)
	}()

	// Graceful shutdown on SIGINT/SIGTERM.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		logger.Info("Server is listening on port 8080...")
		if err := s.Serve(listener); err != nil {
			logger.Fatal("Failed to serve", zap.Error(err))
		}
	}()

	sig := <-quit
	logger.Info("Received shutdown signal, draining connections...", zap.String("signal", sig.String()))
	s.GracefulStop()
	logger.Info("Server stopped gracefully")
}
