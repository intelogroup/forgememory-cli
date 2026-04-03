.PHONY: build test lint clean cross dist

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "0.1.0")
LDFLAGS := -ldflags "-s -w -X main.version=$(VERSION)"

build:
	go build $(LDFLAGS) -o forge ./cmd/

test:
	go test ./... -v -count=1

lint:
	go vet ./...
	gofmt -l -w .

clean:
	rm -f forge forge-*

cross: clean
	GOOS=darwin  GOARCH=amd64 CGO_ENABLED=0 go build $(LDFLAGS) -o forge-darwin-amd64 ./cmd/
	GOOS=darwin  GOARCH=arm64 CGO_ENABLED=0 go build $(LDFLAGS) -o forge-darwin-arm64 ./cmd/
	GOOS=linux   GOARCH=amd64 CGO_ENABLED=0 go build $(LDFLAGS) -o forge-linux-amd64 ./cmd/
	GOOS=linux   GOARCH=arm64 CGO_ENABLED=0 go build $(LDFLAGS) -o forge-linux-arm64 ./cmd/
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build $(LDFLAGS) -o forge-windows-amd64.exe ./cmd/

dist: cross
	mkdir -p dist
	tar czf dist/forge-darwin-amd64.tar.gz forge-darwin-amd64
	tar czf dist/forge-darwin-arm64.tar.gz forge-darwin-arm64
	tar czf dist/forge-linux-amd64.tar.gz forge-linux-amd64
	tar czf dist/forge-linux-arm64.tar.gz forge-linux-arm64
	zip dist/forge-windows-amd64.zip forge-windows-amd64.exe
	cd dist && shasum -a 256 * > SHA256SUMS

integration:
	./test_integration.sh
