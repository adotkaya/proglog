# Proglog

A distributed commit log service written in Go, built while working through *Distributed Services with Go* by Travis Jeffrey.

The official book repository can be found at [travisjeffery/proglog](https://github.com/travisjeffery/proglog). This repository is a personal record of that journey.

---

## What It Is

Proglog is an append-only, partitioned log storage engine that exposes both a **gRPC** and a simple **HTTP** API. It demonstrates building production-grade distributed services in Go, including:

- **Segmented log storage** with memory-mapped indexes and buffered disk I/O
- **gRPC API** with unary and streaming produce/consume methods
- **JSON-over-HTTP API** as a lightweight fallback
- **mTLS authentication** with client certificates
- **Casbin-based ACL authorization**
- **Service discovery** via HashiCorp Serf gossip protocol
- **Log replication** across nodes using gRPC streaming
- **Observability** via OpenCensus traces/metrics and Zap structured logging

---

## Prerequisites

- [Go](https://golang.org/dl/) 1.25+
- [cfssl](https://github.com/cloudflare/cfssl) and `cfssljson` (for certificate generation)
- `protoc` with the Go and gRPC Go plugins (only needed if you modify `.proto` files)

---

## Building

### 1. Initialize the config directory

```bash
make init
```

This creates `~/.proglog/` where certificates and ACL files will live.

### 2. Generate TLS certificates

```bash
make gencert
```

This generates:
- `ca.pem` / `ca-key.pem` — the certificate authority
- `server.pem` / `server-key.pem` — server certificate
- `root-client.pem` / `root-client-key.pem` — authorized client cert (CN=`root`)
- `nobody-client.pem` / `nobody-client-key.pem` — unauthorized client cert (CN=`nobody`)

All certificates are copied to `~/.proglog/`.

### 3. Build the server

```bash
go build ./cmd/server
```

### 4. Regenerate protobuf code (only if you edit `api/v1/log.proto`)

```bash
make compile
```

---

## Testing

Run the full test suite with race detection:

```bash
make test
```

This automatically copies the Casbin ACL model and policy files to `~/.proglog/` before running tests.

Individual packages can be tested directly:

```bash
go test -race ./internal/log/...
go test -race ./internal/server/...
go test -race ./internal/discovery/...
```

To enable OpenCensus telemetry output during tests for debugging:

```bash
go test -race ./internal/server/... -debug
```

This writes metrics and trace logs to temporary files whose paths are printed in the test output.

---

## Running the Server

The provided binary starts the HTTP server on port `8080`:

```bash
go run ./cmd/server
```

Then produce a record:

```bash
curl -X POST localhost:8080 \
  -H "Content-Type: application/json" \
  -d '{"record":{"value":"SGVsbG8gV29ybGQ="}}'
```

And consume it back:

```bash
curl -X GET localhost:8080 \
  -H "Content-Type: application/json" \
  -d '{"offset":0}'
```

---

## Architecture

See [ARCHITECTURE.md](ARCHITECTURE.md) for a comprehensive breakdown of every component, data structure, function signature, and control flow.

---

## Project Notes

- Progress through the book was tracked with Git tags marking the completion of each major section.
- Some idioms and style preferences differ from the official repository while still maintaining correctness against the book's tests.
