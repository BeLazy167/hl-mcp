.PHONY: build test race vet benchmark

build:
	CGO_ENABLED=0 go build -trimpath -o bin/hl-mcp ./cmd/hl-mcp

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

benchmark:
	go test -run '^$' -bench . -benchmem ./internal/audit ./internal/hyperliquid
