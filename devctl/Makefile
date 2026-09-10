.PHONY: test build clean
export GOTOOLCHAIN=local
export GOPROXY=off
export GOSUMDB=off
export CGO_ENABLED=0

test:
	go test ./...
	go vet ./...

build:
	mkdir -p dist
	GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/devctl-windows-amd64.exe ./cmd/devctl
	GOOS=windows GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/devctl-windows-arm64.exe ./cmd/devctl
	GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o dist/devctl-linux-arm64 ./cmd/devctl
	GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o dist/devctl-linux-amd64 ./cmd/devctl
	cd dist && sha256sum devctl-* > SHA256SUMS

clean:
	rm -rf dist
