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
| Huawei OBS     | `oss/huanweiobs/obs.go`    | `AK`, `SK`, `Endpoint`, `Bucket`                |
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
