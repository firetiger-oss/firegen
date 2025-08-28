# firegen 🔥 by Firetiger 🐅

Firegen is a high-cardinality OpenTelemetry metrics generator that creates configurable numbers of metrics, services, and attributes for testing OpenTelemetry collectors.
It sends metrics via OTLP with customizable intervals and attribute combinations to simulate realistic metric loads.

## Usage

```bash
# Build the binary
go build -o firegen ./cmd/firegen

# Run with default config (firegen.yaml)
./firegen

# Run with custom config and endpoint
./firegen -config myconfig.yaml -endpoint otelcol:4317

# Run with plaintext connection (no TLS)
./firegen -plaintext

# Run with HTTP instead of gRPC
./firegen -http

# Run with HTTP, authentication, and custom endpoint
./firegen -http -token "your-token" -endpoint "https://otelcol.example.com"
```

## Configuration

Create a `firegen.yaml` file:

```yaml
metrics: 2           # Number of metrics to generate (metric-0000, metric-0001, ...)
interval: 10         # Export interval in seconds  
services: 2          # Number of services to simulate (resource attribute service.name values service-0000, service-0001, ...)
attributes:          # Custom attributes with cardinality
  - name: region
    cardinality: 2   # Generates values: 000000000, 000000001
  - name: cluster
    cardinality: 1   # Generates values: 000000000
  - name: pod
    cardinality: 3   # Generates values: 000000000, 000000001, 000000002
```

## Flags

- `-config` - Path to config file (default: `firegen.yaml`)
- `-endpoint` - OTLP endpoint (default: `localhost:4317`)
- `-plaintext` - Use plaintext connection instead of TLS (default: false)
- `-token` - Bearer token for authentication (default: none)
- `-http` - Use HTTP instead of gRPC (default: false)

## Output

Firegen generates:
- **Total series**: `services × metrics × (cardinality of all attributes combined)`
- **Float64Gauge metrics** with random values (0.0-1.0)
- **Resource attributes**: `service.name` per service (service-0000, service-0001, etc.)
- **Staggered exports**: Services export with time offsets to spread load
- **Manual export**: Metrics are explicitly exported after each collection cycle
- **Dual protocol support**: HTTP (port 4318) or gRPC (port 4317) with authentication
