# local-debug (ldbg) build
BINARY      := ldbg
PKG         := github.com/hzeng10/local-debug
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo 0.0.0-dev)
LDFLAGS     := -s -w -X $(PKG)/cmd.Version=$(VERSION)
DIST        := dist

.PHONY: all build vet test tidy clean cross

all: vet build

build: ## build host binary
	go build -ldflags '$(LDFLAGS)' -o $(BINARY) .

vet:
	go vet ./...

test:
	go test ./...

tidy:
	go mod tidy

# Cross-compile the two target platforms (laptop OSes) + macOS.
cross: $(DIST)/$(BINARY)-linux-amd64 $(DIST)/$(BINARY)-windows-amd64.exe $(DIST)/$(BINARY)-darwin-arm64

$(DIST)/$(BINARY)-linux-amd64:
	GOOS=linux GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o $@ .
$(DIST)/$(BINARY)-windows-amd64.exe:
	GOOS=windows GOARCH=amd64 go build -ldflags '$(LDFLAGS)' -o $@ .
$(DIST)/$(BINARY)-darwin-arm64:
	GOOS=darwin GOARCH=arm64 go build -ldflags '$(LDFLAGS)' -o $@ .

clean:
	rm -rf $(BINARY) $(DIST)
