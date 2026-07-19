# Proglog — Comprehensive Architecture & Internals

> This document explains every component, function, data structure, and control flow in the proglog codebase. It is written at a low level and includes exact type definitions and function signatures so you can trace how the system works without reading every file.

---

## 1. What This Project Is

**Proglog** is an educational distributed commit log service written in Go. It is based on the book *Distributed Services with Go* by Travis Jeffrey. At its core it is an **append-only, partitioned log** that exposes:

- A **gRPC API** with unary and streaming produce/consume methods.
- A simple **JSON-over-HTTP API** as a fallback.
- **mTLS authentication** (client certificates required).
- **Casbin-based ACL authorization**.
- **Service discovery** via HashiCorp Serf.
- **Log replication** across nodes using gRPC streaming.
- **Observability** via OpenCensus traces/metrics and Zap structured logging.

The storage engine segments data into bounded files (Store + Index) similar to Kafka or NATS JetStream.

---

## 2. Directory Structure

```
/home/pc/Code/proglog/
├── api/v1/
│   ├── log.proto              # Protobuf service definition
│   ├── log.pb.go              # Generated protobuf messages
│   ├── log_grpc.pb.go         # Generated gRPC client/server stubs
│   └── error.go               # Custom ErrOffsetOutOfRange
├── cmd/server/
│   └── main.go                # Entry point — starts HTTP server on :8080
├── internal/
│   ├── auth/
│   │   └── authorizer.go      # Casbin ACL enforcer
│   ├── config/
│   │   ├── files.go           # Resolves paths to certs and ACL files
│   │   └── tls.go             # Builds tls.Config for server/client
│   ├── discovery/
│   │   ├── membership.go      # Serf-based cluster membership
│   │   └── membership_test.go
│   ├── log/                   # Core storage engine
│   │   ├── config.go          # Segment size limits
│   │   ├── index.go           # Memory-mapped offset → position index
│   │   ├── index_test.go
│   │   ├── log.go             # Log manager (segment lifecycle)
│   │   ├── replicator.go      # Replicates records from peers
│   │   ├── segment.go         # One segment = one Store + one Index
│   │   ├── segment_test.go
│   │   ├── store.go           # Buffered disk-backed store
│   │   └── store_test.go
│   └── server/
│       ├── http.go            # JSON-over-HTTP server
│       ├── server.go          # gRPC server (main production interface)
│       └── server_test.go
├── test/
│   ├── ca-config.json         # CA signing profiles
│   ├── ca-csr.json            # CA CSR
│   ├── client-csr.json        # Client CSR template
│   ├── model.conf             # Casbin ACL model
│   ├── policy.csv             # Casbin policy
│   └── server-csr.json        # Server CSR
├── Makefile                   # Build targets (gencert, test, compile)
├── go.mod / go.sum
└── README.md
```

---

## 3. API Layer (`api/v1`)

### 3.1 Protobuf Definition (`log.proto`)

```protobuf
syntax = "proto3";
package log.v1;

message Record {
  bytes value  = 1;
  int64 offset = 2;
}

service Log {
  rpc Produce(ProduceRequest)       returns (ProduceResponse);
  rpc Consume(ConsumeRequest)       returns (ConsumeResponse);
  rpc ConsumeStream(ConsumeRequest) returns (stream ConsumeResponse);
  rpc ProduceStream(stream ProduceRequest) returns (stream ProduceResponse);
}

message ProduceRequest  { Record record = 1; }
message ProduceResponse { uint64 offset = 1; }
message ConsumeRequest  { uint64 offset = 1; }
message ConsumeResponse { Record record = 2; }
```

### 3.2 Generated Go Types (`log.pb.go`)

The generated package is `log_v1`. The key message is:

```go
type Record struct {
    Value  []byte
    Offset int64
}
```

### 3.3 gRPC Client/Server Interface (`log_grpc.pb.go`)

```go
type LogClient interface {
    Produce(ctx context.Context, in *ProduceRequest, opts ...grpc.CallOption) (*ProduceResponse, error)
    Consume(ctx context.Context, in *ConsumeRequest, opts ...grpc.CallOption) (*ConsumeResponse, error)
    ConsumeStream(ctx context.Context, in *ConsumeRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[ConsumeResponse], error)
    ProduceStream(ctx context.Context, opts ...grpc.CallOption) (grpc.BidiStreamingClient[ProduceRequest, ProduceResponse], error)
}

type LogServer interface {
    Produce(context.Context, *ProduceRequest) (*ProduceResponse, error)
    Consume(context.Context, *ConsumeRequest) (*ConsumeResponse, error)
    ConsumeStream(*ConsumeRequest, Log_ConsumeStreamServer) error
    ProduceStream(Log_ProduceStreamServer) error
    mustEmbedUnimplementedLogServer()
}
```

### 3.4 Custom Error (`error.go`)

```go
type ErrOffsetOutOfRange struct {
    Offset uint64
}
```

It implements `error` and also `GRPCStatus() *status.Status` so gRPC can return it with a `404` code and a detailed `LocalizedMessage`.

---

## 4. Core Storage Engine (`internal/log`)

This is the heart of the project. Storage is layered:

```
Log (manager)
  └── []*segment
        └── segment (active)
              ├── Store  (buffered file I/O)
              └── index  (memory-mapped file)
```

### 4.1 Config (`config.go`)

```go
type Config struct {
    Segment struct {
        MaxStoreBytes uint64  // Max raw data bytes per segment
        MaxIndexBytes uint64  // Max index bytes per segment
        InitialOffset uint64  // Starting offset for the first segment
    }
}
```

If you pass a zero-value `Config`, `NewLog` applies defaults:
- `MaxStoreBytes = 1024`
- `MaxIndexBytes = 1024`

### 4.2 Store (`store.go`)

**Purpose:** Writes and reads raw bytes to/from disk using a `bufio.Writer` for buffering.

**Record format on disk:**

```
| 8 bytes: uint64 length | N bytes: payload |
```

This is called **length-prefixing**. `lenWidth = 8`.

**Type:**

```go
type Store struct {
    file *os.File
    buf  *bufio.Writer
    size uint64      // current logical size (including buffered data)
    mu   sync.Mutex
}
```

**Key functions:**

```go
func NewStore(f *os.File) (*Store, error)
```
Reads the file's current size to set `size` and wraps the file in a `bufio.Writer`.

```go
func (s *Store) Append(p []byte) (n uint64, pos uint64, err error)
```
- Locks the store.
- `pos = s.size` (byte position where this record will start).
- Writes `uint64(len(p))` (8 bytes, big-endian) to the buffer.
- Writes `p` to the buffer.
- `s.size += lenWidth + len(p)`.
- Returns total bytes written (`n`) and the starting position (`pos`).

**Important:** Data is **not** flushed to disk until `Read`, `ReadAt`, or `Close` calls `s.buf.Flush()`.

```go
func (s *Store) Read(pos uint64) ([]byte, error)
```
- Flushes the buffer first.
- Reads 8 bytes at `pos` to get the length.
- Reads `length` bytes at `pos + lenWidth`.
- Returns the payload.

```go
func (s *Store) ReadAt(p []byte, off int64) (int, error)
```
Flushes the buffer, then delegates to `s.file.ReadAt(p, off)`.

```go
func (s *Store) Close() error
```
Flushes the buffer, then closes the underlying file.

### 4.3 Index (`index.go`)

**Purpose:** Maps a **relative offset** to the **byte position** of that record in the Store.

It uses **memory-mapped files** via `github.com/tysonmote/gommap` for fast random access.

**Entry format in the mmap region:**

```
| 4 bytes: uint32 relative offset | 8 bytes: uint64 position in Store |
```

So each entry is `entWidth = 4 + 8 = 12` bytes.

**Type:**

```go
type index struct {
    file *os.File
    mmap gommap.MMap
    size uint64  // number of bytes actually written into the mmap
}
```

**Key functions:**

```go
func newIndex(file *os.File, c Config) (*index, error)
```
- Stats the file to get its current size.
- `os.Truncate` the file to `MaxIndexBytes` so the mmap region is large enough.
- Maps the file into memory with `PROT_READ | PROT_WRITE` and `MAP_SHARED`.

```go
func (i *index) Close() error
```
- `mmap.Sync(MS_SYNC)` — forces memory-mapped data to the underlying file.
- `file.Sync()` — forces file to stable storage.
- `file.Truncate(int64(i.size))` — truncates to only the data actually written.

```go
func (i *index) Read(in int64) (out uint32, pos uint64, err error)
```
- If `in == -1`, it means "read the last entry". `out = (i.size / entWidth) - 1`.
- Otherwise `out = uint32(in)` (the relative offset).
- Computes `pos = uint64(out) * entWidth` (byte offset into the mmap).
- If `i.size < pos + entWidth`, returns `io.EOF`.
- Decodes the 4-byte offset and 8-byte position from the mmap.
- **Returns the relative offset and the Store position.**

```go
func (i *index) Write(off uint32, pos uint64) error
```
- Checks if there is room for another `entWidth`-sized entry.
- Encodes `off` at `mmap[i.size : i.size+offWidth]`.
- Encodes `pos` at `mmap[i.size+offWidth : i.size+entWidth]`.
- `i.size += entWidth`.

**Why relative offsets?** If absolute offsets were stored, each entry would need 8 extra bytes (uint64 instead of uint32). At scale (billions/trillions of records) this saves enormous space.

### 4.4 Segment (`segment.go`)

**Purpose:** Brings together one `Store` and one `index`. Handles protobuf marshaling/unmarshaling. Computes absolute offsets.

**Type:**

```go
type segment struct {
    store                  *Store
    index                  *index
    baseOffset, nextOffset uint64
    config                 Config
}
```

- `baseOffset`: the absolute offset of the first record in this segment.
- `nextOffset`: the absolute offset that the *next* appended record will receive.

**Key functions:**

```go
func newSegment(dir string, baseOffset uint64, c Config) (*segment, error)
```
- Opens (or creates) `<dir>/<baseOffset>.store`.
- Opens (or creates) `<dir>/<baseOffset>.index`.
- Calls `newIndex` and `NewStore`.
- Reads the last index entry (`index.Read(-1)`):
  - If the index is empty, `nextOffset = baseOffset`.
  - Otherwise `nextOffset = baseOffset + uint64(lastRelativeOffset) + 1`.

```go
func (s *segment) Append(record *api.Record) (offset uint64, err error)
```
- `cur := s.nextOffset`
- Sets `record.Offset = int64(cur)`.
- `proto.Marshal(record)` → raw bytes.
- `store.Append(rawBytes)` → gets `pos` (byte position in store).
- `index.Write(uint32(s.nextOffset - s.baseOffset), pos)`.
- `s.nextOffset++`.
- Returns `cur`.

```go
func (s *segment) Read(off uint64) (*api.Record, error)
```
- `index.Read(int64(off - s.baseOffset))` → gets relative offset and `pos`.
- `store.Read(pos)` → gets raw protobuf bytes.
- `proto.Unmarshal(rawBytes, record)`.

```go
func (s *segment) IsMaxed() bool
```
Returns `true` if either `store.size >= MaxStoreBytes` or `index.size >= MaxIndexBytes`.

```go
func (s *segment) Close() error
```
Closes index, then store.

```go
func (s *segment) Remove() error
```
Closes, then deletes both `.index` and `.store` files from disk.

### 4.5 Log (`log.go`)

**Purpose:** Manages a list of segments, creates new ones when the active segment fills up, routes reads to the correct segment, and handles lifecycle operations.

**Type:**

```go
type Log struct {
    mu            sync.RWMutex
    Dir           string
    Config        Config
    activeSegment *segment
    segments      []*segment
}
```

**Key functions:**

```go
func NewLog(dir string, c Config) (*Log, error)
```
- Sets defaults if `MaxStoreBytes` or `MaxIndexBytes` are zero.
- Calls `l.setup()`.

```go
func (l *Log) setup() error
```
- Reads the `dir` to find existing segment files.
- Extracts base offsets from filenames (e.g. `16.store` → `16`).
- Sorts base offsets ascending.
- For each unique base offset, calls `l.newSegment(baseOffset)`.
- If no segments exist, creates a new segment with `InitialOffset`.

```go
func (l *Log) Append(record *api.Record) (uint64, error)
```
- Write-locks.
- Appends to `l.activeSegment`.
- If the active segment `IsMaxed()`, creates a new segment starting at `off + 1`.

```go
func (l *Log) Read(off uint64) (*api.Record, error)
```
- Read-locks.
- Iterates `l.segments` to find the one where `baseOffset <= off < nextOffset`.
- If no segment found, or `s.nextOffset <= off`, returns `api.ErrOffsetOutOfRange{Offset: off}`.
- Delegates to `s.Read(off)`.

```go
func (l *Log) newSegment(off uint64) error
```
- Calls `newSegment(l.Dir, off, l.Config)`.
- Appends to `l.segments`.
- Sets `l.activeSegment = s`.

**Lifecycle functions:**

```go
func (l *Log) Close() error      // closes all segments
func (l *Log) Remove() error     // close + os.RemoveAll(dir)
func (l *Log) Reset() error      // remove + setup()
```

**Offset queries:**

```go
func (l *Log) LowestOffset()  (uint64, error)  // baseOffset of first segment
func (l *Log) HighestOffset() (uint64, error)  // nextOffset of last segment - 1 (or 0)
```

**Log compaction:**

```go
func (l *Log) Truncate(lowest uint64) error
```
Removes all segments whose `nextOffset <= lowest+1`.

**Streaming reads:**

```go
func (l *Log) Reader() io.Reader
```
Builds a `io.MultiReader` from all segments' stores. Each segment is wrapped in an `originReader` that implements `io.Reader` by tracking its own offset and calling `ReadAt`.

---

## 5. Replication (`internal/log/replicator.go`)

**Purpose:** Keeps replicas in sync by connecting to peer nodes and consuming their logs via gRPC streaming.

**Type:**

```go
type Replicator struct {
    DialOptions []grpc.DialOption
    LocalServer api.LogClient    // local gRPC client used to re-produce received records

    logger *zap.Logger

    mu      sync.Mutex
    servers map[string]chan struct{}  // name → leave signal
    closed  bool
    close   chan struct{}             // global shutdown signal
}
```

**Key functions:**

```go
func (r *Replicator) Join(name, addr string) error
```
- If already replicating to `name`, returns nil.
- Creates a leave channel: `r.servers[name] = make(chan struct{})`.
- Spawns `go r.replicate(addr, r.servers[name])`.

```go
func (r *Replicator) replicate(addr string, leave chan struct{})
```
- Dials `addr` with `r.DialOptions`.
- Creates a `LogClient` from the connection.
- Calls `client.ConsumeStream(ctx, &api.ConsumeRequest{Offset: 0})`.
- Spawns a goroutine that receives records from the stream and pushes them into a local `records` channel.
- Main loop:
  - `select` on `r.close` (global shutdown), `leave` (this peer left), or `records`.
  - On a record: calls `r.LocalServer.Produce(ctx, &api.ProduceRequest{Record: record})`.

```go
func (r *Replicator) Leave(name string) error
```
- Closes the leave channel for that server.
- Deletes it from `r.servers`.

```go
func (r *Replicator) Close() error
```
- Sets `r.closed = true` and closes `r.close`, which stops all replication goroutines.

**Connection to discovery:** The `Replicator` implements the `discovery.Handler` interface (`Join`, `Leave`). The `Membership` component calls these when Serf detects node changes.

---

## 6. Service Discovery (`internal/discovery/membership.go`)

**Purpose:** Uses HashiCorp Serf for gossip-based cluster membership. When a node joins or leaves, it notifies the `Replicator` (or any `Handler`) so replication streams can start or stop.

**Types:**

```go
type Membership struct {
    Config
    handler Handler
    serf    *serf.Serf
    events  chan serf.Event
    logger  *zap.Logger
}

type Config struct {
    NodeName       string
    BindAddr       string
    Tags           map[string]string   // e.g. {"rpc_addr": "127.0.0.1:8081"}
    StartJoinAddrs []string            // seeds to join on startup
}

type Handler interface {
    Join(name, addr string) error
    Leave(name string) error
}
```

**Key functions:**

```go
func New(handler Handler, config Config) (*Membership, error)
```
- Creates the struct.
- Calls `setupSerf()`.

```go
func (m *Membership) setupSerf() error
```
- Resolves `BindAddr` into an IP and port.
- Creates a Serf config, wiring `m.events` as the event channel.
- Calls `serf.Create(config)`.
- Spawns `go m.eventHandler()`.
- If `StartJoinAddrs` is set, calls `m.serf.Join(...)`.

```go
func (m *Membership) eventHandler()
```
- Listens on `m.events`.
- `serf.EventMemberJoin`: for each member (excluding self), calls `m.handler.Join(name, member.Tags["rpc_addr"])`.
- `serf.EventMemberLeave` / `serf.EventMemberFailed`: for each member (excluding self), calls `m.handler.Leave(name)`.

**Other methods:**

```go
func (m *Membership) isLocal(member serf.Member) bool
func (m *Membership) Members() []serf.Member
func (m *Membership) Leave() error   // calls serf.Leave()
```

---

## 7. Authentication & Authorization

### 7.1 TLS Config (`internal/config/tls.go`)

```go
type TLSConfig struct {
    CertFile      string
    KeyFile       string
    CAFile        string
    ServerAddress string
    Server        bool   // true = server mode, false = client mode
}

func SetupTLSConfig(cfg TLSConfig) (*tls.Config, error)
```
- Loads the certificate/key pair.
- If `Server == true`: sets `ClientCAs` and `ClientAuth = tls.RequireAndVerifyClientCert`.
- If `Server == false`: sets `RootCAs`.
- Sets `ServerName`.

### 7.2 File Paths (`internal/config/files.go`)

```go
var (
    CAFile               = configFile("ca.pem")
    ServerCertFile       = configFile("server.pem")
    ServerKeyFile        = configFile("server-key.pem")
    RootClientCertFile   = configFile("root-client.pem")
    RootClientKeyFile    = configFile("root-client-key.pem")
    NobodyClientCertFile = configFile("nobody-client.pem")
    NobodyClientKeyFile  = configFile("nobody-client-key.pem")
    ACLModelFile         = configFile("model.conf")
    ACLPolicyFile        = configFile("policy.csv")
)

func configFile(filename string) string
```
- Uses `CONFIG_DIR` env var if set.
- Otherwise resolves to `~/.proglog/<filename>`.

### 7.3 Casbin Authorizer (`internal/auth/authorizer.go`)

```go
type Authorizer struct {
    enforcer *casbin.Enforcer
}

func New(model, policy string) *Authorizer
```
- Loads the Casbin model and policy from the given file paths.

```go
func (a *Authorizer) Authorize(subject, object, action string) error
```
- Calls `a.enforcer.Enforce(subject, object, action)`.
- If false, returns a gRPC `status.Error(codes.PermissionDenied, ...)`.

**Policy example (`test/policy.csv`):**
```
p, root, *, produce
p, root, *, consume
```

This allows the client whose certificate CN is `root` to produce and consume on any object (`*`).

---

## 8. Server Layer (`internal/server`)

### 8.1 gRPC Server (`server.go`)

**Config:**

```go
type Config struct {
    CommitLog  CommitLog
    Authorizer Authorizer
}

type CommitLog interface {
    Append(*api.Record) (uint64, error)
    Read(uint64) (*api.Record, error)
}

type Authorizer interface {
    Authorize(subject, object, action string) error
}
```

The interfaces exist so tests can inject mocks.

**Server type:**

```go
type grpcServer struct {
    *Config
    api.UnimplementedLogServer
}
```

**Constructor:**

```go
func NewGRPCServer(config *Config, grpcOpts ...grpc.ServerOption) (*grpc.Server, error)
```
Sets up a full production gRPC server with these interceptors (in order):

1. **`grpc_ctxtags`** — extracts tags into context for logging.
2. **`grpc_zap`** — structured request/response logging with nanosecond duration fields.
3. **`grpc_auth`** — calls `authenticate()` to extract the client certificate CN.
4. **`ocgrpc.ServerHandler`** — OpenCensus stats handler for metrics/traces.

It also configures OpenCensus to always sample traces and registers default server views.

**RPC handlers:**

```go
func (s *grpcServer) Produce(ctx context.Context, req *api.ProduceRequest) (*api.ProduceResponse, error)
```
- `s.Authorizer.Authorize(subject(ctx), "*", "produce")`
- `s.CommitLog.Append(req.Record)` → returns offset.

```go
func (s *grpcServer) Consume(ctx context.Context, req *api.ConsumeRequest) (*api.ConsumeResponse, error)
```
- `s.Authorizer.Authorize(subject(ctx), "*", "consume")`
- `s.CommitLog.Read(req.Offset)` → returns record.

```go
func (s *grpcServer) ProduceStream(stream api.Log_ProduceStreamServer) error
```
- Loops `stream.Recv()`.
- Calls `s.Produce(stream.Context(), req)`.
- Sends the response back via `stream.Send(res)`.

```go
func (s *grpcServer) ConsumeStream(req *api.ConsumeRequest, stream api.Log_ConsumeStreamServer) error
```
- Loops forever.
- Calls `s.Consume(stream.Context(), req)`.
- If error is `api.ErrOffsetOutOfRange`, it **continues** (useful when waiting for new records).
- Otherwise returns on error.
- Sends the response and increments `req.Offset++`.

**Authentication helper:**

```go
func authenticate(ctx context.Context) (context.Context, error)
```
- Gets peer info from context.
- Casts `peer.AuthInfo` to `credentials.TLSInfo`.
- Extracts `VerifiedChains[0][0].Subject.CommonName`.
- Stores it in context under `subjectContextKey{}`.

```go
func subject(ctx context.Context) string
```
Retrieves the CN from context.

### 8.2 HTTP Server (`http.go`)

A simple JSON-over-HTTP fallback. No auth, no TLS, no streams.

```go
func NewHTTPServer(addr string) *http.Server
```
- Creates an `httpServer` with a brand new `log.Log` in `os.TempDir()`.
- Registers `POST /` → `handleProduce`.
- Registers `GET  /` → `handleConsume`.

**Types:**

```go
type httpServer struct {
    Log *log.Log
}

type ProduceRequest  struct { Record api.Record `json:"record"` }
type ProduceResponse struct { Offset uint64    `json:"offset"` }
type ConsumeRequest  struct { Offset uint64    `json:"offset"` }
type ConsumeResponse struct { Record api.Record `json:"record"` }
```

**Handlers:**

```go
func (s *httpServer) handleProduce(w http.ResponseWriter, r *http.Request)
```
Decodes JSON, calls `s.Log.Append`, encodes the offset as JSON.

```go
func (s *httpServer) handleConsume(w http.ResponseWriter, r *http.Request)
```
Decodes JSON, calls `s.Log.Read`, handles `ErrOffsetOutOfRange` as HTTP 404, encodes record as JSON.

---

## 9. Entry Point (`cmd/server/main.go`)

```go
func main() {
    srv := server.NewHTTPServer(":8080")
    log.Fatal(srv.ListenAndServe())
}
```

**Note:** The current binary only starts the HTTP server. The full-featured gRPC server (with mTLS, auth, logging, metrics, and streams) is defined and thoroughly tested but is **not wired into the runnable binary**. To use it in production you would extend `main.go` (or create a second binary) to call `server.NewGRPCServer` with proper TLS credentials and a `log.Log` instance.

---

## 10. Build System (`Makefile`)

| Target | What it does |
|--------|-------------|
| `make init` | Creates `~/.proglog` directory |
| `make gencert` | Generates CA, server, `root-client`, and `nobody-client` certs with `cfssl`. Copies to `~/.proglog/`. |
| `make test` | Copies ACL files to `~/.proglog/`, then runs `go test -race ./...` |
| `make compile` | Runs `protoc` to regenerate `log.pb.go` and `log_grpc.pb.go` |

**Certificate generation order:**
1. `cfssl gencert -initca test/ca-csr.json` → `ca.pem`, `ca-key.pem`
2. `cfssl gencert -ca=ca.pem -ca-key=ca-key.pem -config=test/ca-config.json -profile=server test/server-csr.json` → `server.pem`, `server-key.pem`
3. `cfssl gencert ... -profile=client ... -cn=root` → `root-client.pem`, `root-client-key.pem`
4. `cfssl gencert ... -profile=client ... -cn=nobody` → `nobody-client.pem`, `nobody-client-key.pem`

The `root` client is authorized in `policy.csv`; the `nobody` client is not.

---

## 11. End-to-End Data Flows

### 11.1 Producing a Record (gRPC, authorized)

```
Client sends ProduceRequest{Record}
    ↓
[authenticate]
    - extracts CN from client cert (e.g. "root")
    ↓
[grpc_auth interceptor → Authorizer.Authorize("root", "*", "produce")]
    - Casbin enforcer checks policy.csv → allow
    ↓
[grpcServer.Produce]
    - calls CommitLog.Append(record)
        ↓
    [Log.Append]
        - write-locks
        - activeSegment.Append(record)
            ↓
        [segment.Append]
            - record.Offset = nextOffset
            - proto.Marshal(record) → raw bytes
            - store.Append(rawBytes) → pos
            - index.Write(relativeOffset, pos)
            - nextOffset++
        - if IsMaxed() → newSegment(nextOffset)
    - returns offset
```

### 11.2 Consuming a Record (gRPC, authorized)

```
Client sends ConsumeRequest{Offset: N}
    ↓
[authenticate + Authorizer.Authorize(..., "consume")]
    ↓
[grpcServer.Consume]
    - calls CommitLog.Read(N)
        ↓
    [Log.Read]
        - read-locks
        - finds segment where baseOffset <= N < nextOffset
        - if none → ErrOffsetOutOfRange
        - segment.Read(N)
            ↓
        [segment.Read]
            - index.Read(N - baseOffset) → pos
            - store.Read(pos) → raw bytes
            - proto.Unmarshal → *Record
    - returns record
```

### 11.3 Replication Flow (when a new node joins)

```
Membership (Serf) detects new peer "node-2"
    ↓
Membership.eventHandler() → calls handler.Join("node-2", "rpc_addr")
    ↓
Replicator.Join("node-2", "rpc_addr")
    - spawns goroutine: replicate("rpc_addr", leaveCh)
        ↓
    [replicate goroutine]
        - grpc.Dial("rpc_addr")
        - client.ConsumeStream(ctx, Offset: 0)
        - receives records from stream
        - for each record:
            LocalServer.Produce(ctx, &ProduceRequest{Record: record})
                ↓
            [local gRPC server] → same path as 11.1
```

When the peer leaves, `Membership` calls `handler.Leave`, which closes the leave channel, causing the `replicate` goroutine to return and clean up its gRPC connection.

---

## 12. Key Design Decisions

| Decision | Rationale |
|----------|-----------|
| **Length-prefixing in Store** | Allows reading any record at any position without scanning. The first 8 bytes tell you the payload size. |
| **Relative offsets in Index** | Saves 4 bytes per entry (uint32 vs uint64). Critical at scale. |
| **Memory-mapped Index** | Fast random access for offset lookups. Sync + Truncate on Close ensures durability without writing the whole file. |
| **Buffered Store** | `bufio.Writer` batches small writes to reduce syscalls. Flush on Read/ReadAt/Close ensures consistency. |
| **Segment rotation** | Keeps individual files bounded. Old segments can be truncated/deleted independently. |
| **Interfaces for CommitLog & Authorizer** | Enables unit testing the server with mocks. |
| **mTLS + Casbin** | Transport-level identity (certs) + policy-level authorization (ACLs). Separates concerns cleanly. |
| **Serf for discovery** | Lightweight gossip protocol. No separate coordination service (like ZooKeeper) needed. |

---

## 13. Test Summary

| Test File | What it covers |
|-----------|---------------|
| `internal/log/store_test.go` | Append/Read/ReadAt on Store; Close flushes buffered data. |
| `internal/log/index_test.go` | Write/Read on Index; reading last entry (`-1`); reloading from existing file. |
| `internal/log/segment_test.go` | Segment Append/Read lifecycle; IsMaxed on both store and index limits; Remove/re-create. |
| `internal/server/server_test.go` | Produce/Consume round-trip; Consume past boundary returns ErrOffsetOutOfRange; ProduceStream/ConsumeStream; Unauthorized access returns PermissionDenied. |
| `internal/discovery/membership_test.go` | Serf cluster formation; Join/Leave event propagation to Handler. |

---

## 14. Notable Gaps / Observations

1. **HTTP server is unauthenticated.** The `NewHTTPServer` creates a plain `log.Log` with no auth. It is only a learning/demo entry point.
2. **gRPC server is not in the binary.** `cmd/server/main.go` only starts the HTTP server. To use the gRPC server in production you must wire it up yourself.
3. **No snapshot / snapshotting mechanism.** The log engine handles segments but there is no higher-level snapshot or compaction beyond `Truncate`.
4. **Single `rpc_addr` tag.** The membership system assumes one gRPC address per node. In a real system you might advertise multiple addresses or a load balancer.
5. **No leader election.** Every node can produce and consume. There is no concept of a "leader" or partition ownership.
