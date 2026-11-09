# Dify Cloud Kit

Dify Cloud Kit is a unified abstraction library for integrating various cloud object storage services in Go. It simplifies switching between providers and supports local testing and multi-cloud deployments.

## ✨ Features

- Supports multiple backends: Local FS, Aliyun OSS, AWS S3, Azure Blob, Tencent COS, Huawei OBS, Google GCS
- Unified and clean interface
- Factory pattern to dynamically load drivers
- Easy to write tests with local and in-memory backends

## 📦 Installation

```bash
go get github.com/langgenius/dify-cloud-kit
```

## 🚀 Quick Start

```go
import (
    "github.com/langgenius/dify-cloud-kit/oss"
    "github.com/langgenius/dify-cloud-kit/oss/factory"
)

func main() {
    store, err := factory.Load("local", oss.OSSArgs{
        Local: &oss.Local{
            Path: "/tmp/files",
        },
    })
    if err != nil {
        panic(err)
    }

    files, _ := store.List("/")
    fmt.Println(files)
}
```

## 📁 Supported Storage Providers

| Provider       | Module Path                | Required Fields                                 |
|----------------|----------------------------|-------------------------------------------------|
| Local          | `oss/local/localfile.go`   | `Path`                                          |
| Aliyun OSS     | `oss/aliyun/aliyun.go`     | `Endpoint`, `AccessKey`, `SecretKey`, `Bucket`  |
| AWS S3         | `oss/s3/s3.go`             | `Region`, `AccessKey`, `SecretKey`, `Bucket`    |
| Azure Blob     | `oss/azureblob/blob.go`    | `AccountName`, `AccountKey`, `Container`        |
| Google GCS     | `oss/gcsblob/gcs.go`       | `CredentialsJSON`, `Bucket`                     |
| Tencent COS    | `oss/tencentcos/cos.go`    | `SecretId`, `SecretKey`, `Bucket`, `Region`     |
| Huawei OBS     | `oss/huaweiobs/obs.go`    | `AK`, `SK`, `Endpoint`, `Bucket`                |
| Volcengine TOS | `oss/volcenginetos/tos.go` | `Endpoint`,  `AccessKey`, `SecretKey`, `Bucket` |

## 🏗️ Usage with Factory

You can dynamically load a storage backend using the factory:

```go
store, err := factory.Load("s3", oss.OSSArgs{
    S3: &oss.S3{
        Region:    "us-west-2",
        AccessKey: "AKIA...",
        SecretKey: "SECRET...",
        Bucket:    "my-bucket",
    },
})
```

## 🌊 Streaming Reads (Range Support)

`oss.OSS.Load` always buffers the entire object in memory. For large objects or
partial reads, providers can implement the optional `oss.StreamingOSS`
interface, which streams object content through an `io.ReadCloser` without
buffering.

```go
type StreamingOSS interface {
    OSS
    Range(ctx context.Context, key string, byteRange *ByteRange) (io.ReadCloser, error)
}

type ByteRange struct {
    Offset int64 // >= 0
    Length int64 // > 0 bounded, -1 = from Offset to EOF
}
```

Semantics:

- `byteRange == nil` streams the whole object.
- `Length == -1` streams from `Offset` to end of object.
- `Length == 0` or `< -1` fails with `oss.ErrInvalidRange` before any I/O.
- A range outside the object fails with `oss.ErrRangeNotSatisfiable` (HTTP 416
  is mapped to this sentinel for remote providers).
- There is **no suffix-range parser**: resolve `bytes=-N` yourself using the
  object size from `State`.
- Cancelling `ctx` aborts the request and subsequent `Read` calls. Always
  `defer rc.Close()`.
- **No automatic fallback**: backends without `Range` fail the type assertion;
  the library never secretly buffers a ranged request via `Load`.
- Partial `Range` calls verify the server actually honored the `Range` header
  (via `Content-Range`); a server that ignores it causes an error instead of a
  silent full download.

### Supported streaming providers

| Provider   | `Range` support | Notes                                     |
|------------|----------------|-------------------------------------------|
| Local      | ✅             | `os.File` + `io.SectionReader`, end clamped to file size |
| AWS S3     | ✅             | `GetObjectInput.Range`, body streamed     |
| Aliyun OSS | ✅             | `NormalizedRange` + `RangeBehavior("standard")` |
| Azure Blob | ✅             | `DownloadStreamOptions.Range`, `NewRetryReader` resumes interrupted reads |
| Google GCS | ✅             | `NewRangeReader`, 416 mapped to `ErrRangeNotSatisfiable` |
| Tencent COS| ✅             | `ObjectGetOptions.Range`, body streamed  |
| Huawei OBS | ✅             | `GetObjectInput.RangeStart/RangeEnd`; `Length == -1` resolves the size with a HEAD first (the SDK cannot emit `bytes=N-`) |
| Volcengine TOS| ✅          | `GetObjectV2Input.Range`, body streamed |

Huawei OBS caveat: the SDK only emits `bytes=start-end` when `end > start`,
so a single-byte range (`Length == 1`) cannot be expressed. Such requests
still fail loudly (Content-Range verification) instead of returning wrong
data, and an unsatisfiable single-byte range still maps to
`ErrRangeNotSatisfiable` via a size check.

### Capability detection

Callers can check support explicitly instead of relying on a raw type
assertion:

```go
if !oss.SupportsStreaming(store) {
    // backend cannot stream; no fallback buffering is performed
}
```

`oss.OpenStream` performs the assertion and delegates to `Range`, returning an
error wrapping `oss.ErrStreamingNotSupported` for unsupported (or nil)
backends:

```go
rc, err := oss.OpenStream(ctx, store, key, &oss.ByteRange{Offset: 0, Length: -1})
if errors.Is(err, oss.ErrStreamingNotSupported) {
    // handle non-streaming backend
}
if err != nil {
    return err
}
defer rc.Close()
```

### Example

```go
streamer, ok := store.(oss.StreamingOSS)
if !ok {
    return errors.New("backend does not support streaming")
}

ctx := request.Context() // tie the read to the request lifecycle
rc, err := streamer.Range(ctx, "big-file.bin", &oss.ByteRange{Offset: 0, Length: -1})
if err != nil {
    return err
}
defer rc.Close()

// streams without loading the whole object into memory
if _, err := io.Copy(w, rc); err != nil {
    return err
}
```

Note: enterprise-side caller integration (e.g. wiring `StreamingOSS` into
dify-enterprise) is handled separately and is out of scope for this library.

## 🧪 Testing

Unit tests are located in `tests/oss/oss_test.go`.

### Environment Variables

Some providers require credentials to be passed via environment variables. Set them as needed:

```bash
export OSS_AWS_ACCESS_KEY=your-access-key
export OSS_AWS_SECRET_KEY=your-secret-key
export OSS_AWS_REGION=your-region
export OSS_S3_BUCKET=test-bucket

export OSS_ALIYUN_ENDPOINT=your-endpoint
export OSS_ALIYUN_ACCESS_KEY=your-access-key
export OSS_ALIYUN_SECRET_KEY=your-secret-key
export OSS_ALIYUN_BUCKET=test-bucket

```

### Run Tests

```bash
go test ./...
```

## 📄 License

This project is licensed under the [Apache 2.0 License](LICENSE).

## NOTICE
Some parts of the code in this project originate from [dify-plugin-daemon](https://github.com/langgenius/dify-plugin-daemon)

| Provider       | Author | PR |
|----------------|---|---|
| Aliyun OSS     |[bravomark](https://github.com/bravomark)|https://github.com/langgenius/dify-plugin-daemon/pull/261 |
| Azure Blob     |[techan](https://github.com/te-chan)|https://github.com/langgenius/dify-plugin-daemon/pull/172|
| Google GCS     |[Hironori Yamamoto](https://github.com/hiro-o918)|https://github.com/langgenius/dify-plugin-daemon/pull/237|
| Local          |[lengyhua](https://github.com/lengyhua)|https://github.com/langgenius/dify-plugin-daemon/pull/157|
| AWS S3         |[Yeuoly](https://github.com/Yeuoly)|https://github.com/langgenius/dify-plugin-daemon/commit/9ad9d7d4de1d123956ab07955e541bc4053e5170|
| Tencent COS    |[quicksand](https://github.com/quicksandznzn)|https://github.com/langgenius/dify-plugin-daemon/pull/97|
| Volcengine TOS |[quicksand](https://github.com/quicksandznzn)|https://github.com/langgenius/dify-cloud-kit/pull/2|

## 🛠️ Explicit Preparation

Some backends can create their configured storage resource on demand via the
optional `oss.Preparer` capability interface:

```go
type Preparer interface {
    Prepare(ctx context.Context) error
}
```

`Prepare` is **idempotent** and is only ever invoked explicitly — never by
constructors or read/write methods, so read-only clients can simply opt out.
Check support and call it at deployment/startup time:

```go
if !oss.SupportsPrepare(store) {
    // backend does not (yet) implement Preparer
}
err := oss.Prepare(ctx, store)
if errors.Is(err, oss.ErrPrepareNotSupported) {
    // unsupported backend
}
```

`Preparer` is currently implemented by:

- **Azure Blob** — creates the configured container in the existing storage
  account (requires create permission). `ContainerAlreadyExists` is treated
  as success.
- **Google GCS** — creates the configured bucket, requiring
  `GoogleCloudStorage.Project`. A 409 Conflict (bucket already exists) is
  treated as success.
- **AWS S3** — creates the configured bucket when missing. `NotFound` /
  `NoSuchBucket` on the bucket probe triggers creation;
  `BucketAlreadyOwnedByYou` / `BucketAlreadyExists` are treated as success.
  (The legacy constructor's ensure-bucket behavior is unchanged and
  unrelated.)

Every other error — 403, other 409 codes, 5xx, network, cancellation — is
returned to the caller. Remaining providers (Aliyun, Tencent COS, Huawei
OBS, Volcengine TOS) do not implement `Preparer` yet.

## 🧩 Integration Tests (Emulators)

Hermetic integration tests exercise the **library's own interface** —
`factory.Load`, `oss.Prepare`, `Save`, `Range`/`OpenStream`, `Load`,
`Exists`, `State`, `Delete` — end to end against local emulators. No raw
cloud SDK clients are used in the tests and **no real cloud credentials or
accounts are required**:

| Emulator  | Image                                          | Endpoint                    |
|-----------|------------------------------------------------|-----------------------------|
| LocalStack| `localstack/localstack:4.0.3` (S3 only)        | `http://127.0.0.1:14566`    |
| Azurite   | `mcr.microsoft.com/azure-storage/azurite:3.35.0` (blob) | `http://127.0.0.1:11000` |
| fake-gcs  | `fsouza/fake-gcs-server:1.56.1` (memory)       | `http://127.0.0.1:14443`    |

### Make targets

```bash
make integration-up      # start the ephemeral compose project and wait for health
make test-integration    # up + CLOUDKIT_INTEGRATION=1 go test -race ./tests/integration
make integration-down    # stop and remove the compose project
```

`test-integration` leaves services running for fast reruns; use
`integration-down` when finished. Tests create uniquely named
buckets/containers and delete only resources they created. The suite is
gated on `CLOUDKIT_INTEGRATION=1` — a plain `go test ./...` skips it.

### CI

`.github/workflows/integration.yml` runs the unit tests (`./oss/...`) first,
then `make test-integration`, on every pull request, push to `main`, and
manual dispatch ("Storage Integration" check). No secrets are needed;
emulator container logs are uploaded as a workflow artifact on failure and
services are always torn down at the end.

### Scope

- Offline unit tests live under `./oss/...` and run with a plain `go test`
  (every provider that implements `Range` — Local, S3, Aliyun, Azure, GCS,
  Tencent COS, Huawei OBS, Volcengine TOS — has mock-server coverage).
- Emulator-backed integration tests cover S3 (LocalStack), Azure Blob
  (Azurite) and GCS (fake-gcs-server). Tencent COS, Huawei OBS and
  Volcengine TOS have no official emulator, so they are covered by the
  offline mock-server suite only.
- `tests/oss/` is the legacy real-cloud suite (requires credentials) and is
  not part of the hermetic integration gate.
- A true-cloud opt-in test (`oss/azureblob/blob_live_test.go`, requires
  `CLOUDKIT_AZURE_LIVE_*` env vars and an existing blob) is separate.

The streaming feature and these emulator/CI tests ship together in the same
PR — there is no stacked-branch dependency.
