GO ?= go
BIN := bin/shoal

.PHONY: build test vet race check demo iface clean

build:
	$(GO) build -o $(BIN) ./cmd/shoal

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

check: vet race

demo:
	$(GO) run ./cmd/shoal --demo

iface:
	$(GO) run ./cmd/shoal iface

clean:
	rm -rf bin
