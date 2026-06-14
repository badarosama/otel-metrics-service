# Metrics

A high-performance gRPC metrics server and load-testing client built in Go. Implements the OpenTelemetry metrics export protocol with mutual TLS authentication, Prometheus instrumentation, and a built-in concurrent load tester that sustains **4,500 req/s at sub-5ms p99 latency**.

## Features

- **OpenTelemetry Protocol** — Accepts `ExportMetricsServiceRequest` via gRPC, validates metric data points, returns partial success semantics
- **Mutual TLS** — Full mTLS with CA-signed certificates for both server and client authentication
- **Prometheus Instrumentation** — Request count (by method, client, status code) and latency histograms exposed on `:9091/metrics`
- **gRPC Middleware Chain** — Unary interceptor for metrics collection + panic recovery
- **Circular Queue Cache** — O(1) fixed-memory ring buffer caching the last 10 successful and error responses
- **Version Injection** — Build-time `ldflags` embed git commit SHA, semantic version, and timestamp
- **Load Testing Client** — Configurable concurrent workers, duration-based runs, atomic counters for lock-free stats

## Architecture

```
                         ┌─────────────────────────────────────────────┐
                         │              Metrics Server                 │
                         │                                             │
Client ──── mTLS ──────► │  Interceptor ──► Export() ──► Validation    │
  │                      │      │                            │         │
  │                      │      ▼                            ▼         │
  │                      │  Prometheus          CircularQueue Cache    │
  │                      │  :9091/metrics       (success + error)      │
  │                      │                                             │
  ├─── GetVersion() ───► │  ──► version.BuildVersion()                 │
                         └─────────────────────────────────────────────┘
```

## Quick Start

### Prerequisites

- Go 1.22+
- TLS certificates (generate with `certs/cert-gen.sh`)

### Server

```bash
git clone https://github.com/badarosama/metrics.git
cd metrics
go mod tidy

# Build with version info
./server/scripts/build-script.sh

# Or run directly
go run ./server/...
# Listening on :8080, Prometheus on :9091
```

### Client

```bash
go run ./client/client.go \
  -filename client/request_jsons/valid_request.json \
  -duration 60 \
  -concurrent 100
```

## Performance

Load test results with concurrent gRPC clients over sustained runs:

| Metric | Value |
|--------|-------|
| Throughput | 4,500 req/s |
| Total Requests | ~13M |
| p90 Latency | 4.5 ms |
| p95 Latency | 4.5 ms |
| p99 Latency | 4.7 ms |

**p90 Latency**
![p90](https://github.com/badarosama/metrics/assets/549487/5c630b00-b3c2-4157-a0ef-acde953c565d)

**p95 Latency**
![p95](https://github.com/badarosama/metrics/assets/549487/62c8dd00-00f3-4a44-9a44-33a492528bc1)

**p99 Latency**
![p99](https://github.com/badarosama/metrics/assets/549487/53fb8379-f0a6-4e24-8e01-4a400d954dc3)

**Total Requests (Prometheus)**
![total-prom](https://github.com/badarosama/metrics/assets/549487/a1caf604-8fd4-442c-8c5f-1d6e09c2fc36)

**Total Requests (Client)**
![total-client](https://github.com/badarosama/metrics/assets/549487/a8438b4e-b58d-4eb3-a535-1303ee9d5713)

**Request Rate**
![rate](https://github.com/badarosama/metrics/assets/549487/562b3b61-cf4f-4835-aaac-96dbdd4808c6)

### Prometheus Queries

```promql
# Latency percentiles
histogram_quantile(0.90, sum(rate(grpc_request_duration_seconds_bucket[5m])) by (le))
histogram_quantile(0.95, sum(rate(grpc_request_duration_seconds_bucket[5m])) by (le))
histogram_quantile(0.99, sum(rate(grpc_request_duration_seconds_bucket[5m])) by (le))

# Throughput
rate(grpc_request_count[1m])

# Total requests / error rate
sum(grpc_request_count)
sum(grpc_request_count) - sum(grpc_request_count{code="OK"})
```

## Response Caching

The server maintains two fixed-size circular queues (ring buffers) to cache the last 10 successful and last 10 error responses. The implementation uses a head/tail pointer approach with `sync.Mutex` for thread safety, providing O(1) enqueue with zero allocations after initialization. When the buffer is full, the oldest entry is overwritten — guaranteeing a constant memory footprint regardless of request volume.

See [`server/queue.go`](server/queue.go) for the implementation.

## Project Structure

```
metrics/
├── server/
│   ├── server.go          # Server init, TLS, gRPC setup, keepalive config
│   ├── api.go             # Export() and GetVersion() RPC handlers
│   ├── queue.go           # CircularQueue ring buffer implementation
│   ├── metrics.go         # Prometheus counter + histogram definitions
│   ├── middleware.go      # gRPC unary interceptor for metrics collection
│   ├── config.yaml        # Logger configuration
│   ├── version/           # Build-time version injection
│   ├── pb/                # Generated protobuf code
│   └── scripts/           # Build script with ldflags
├── client/
│   ├── client.go          # Load testing client with concurrent workers
│   ├── request_jsons/     # Sample valid/invalid request payloads
│   └── pb/                # Generated protobuf code
├── protos/
│   ├── version.proto      # VersionService definition
│   └── opentelemetry-proto/  # OTel metric proto definitions (submodule)
├── certs/                 # TLS certificates + generation script
├── go.mod
└── README.md
```

## License

[MIT](LICENSE)
