GO ?= go
BIN := bin/shoal

.PHONY: build test vet race check demo iface setcap clean

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

# Linux: let the built binary open raw sockets without running as root.
# macOS has no equivalent; join the access_bpf group (Wireshark's ChmodBPF)
# or run with sudo.
setcap: build
	sudo setcap cap_net_raw+ep $(BIN)

clean:
	rm -rf bin
