# go-cloud-kit
A library and tools for open cloud development in Go.


```go

s, err := factory.Load("local", oss.OSSArgs{
	Local: &oss.Local{
			Path: "/local/path",
		},
})

if err != nil {
    return err
}
s.List("/object1")

```

## Installation 

```
	go get github.com/langgenius/dify-cloud-kit
```

## Available Blobs


