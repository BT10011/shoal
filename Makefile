GO ?= go
BIN := bin/shoal

# The GitHub repository a release is downloaded from: by the shipped
# installer, and by `shoal --update`, which has it stamped in. Point it at a
# public download-only repository to hand out betas while the source stays
# private, e.g. make release RELEASE_REPO=BT10011/shoal-beta
RELEASE_REPO ?= BT10011/shoal

# The version stamped into every build: the nearest tag, else the commit,
# marked dirty when the tree has uncommitted changes.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION) -X main.releaseRepo=$(RELEASE_REPO)

# Release targets: shoal is pure Go, so every one cross-compiles from any
# machine with CGO disabled. Windows is a planned update (see the plan).
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 freebsd/amd64
DIST := dist

.PHONY: build test vet race check demo iface setcap notices release clean

build:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/shoal

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
# or run with sudo. See "Installing" in README.md.
setcap: build
	sudo setcap cap_net_raw+ep $(BIN)

# The licence texts of everything compiled in, which the MIT and BSD
# licences of shoal's dependencies ask to travel with the binary.
notices:
	GO=$(GO) PLATFORMS="$(PLATFORMS)" ./scripts/third-party-notices.sh THIRD_PARTY_NOTICES.md

# One archive per platform, each holding the binary, the README, the
# licence and the third-party notices, plus a SHA256SUMS file to check a
# download against and the installer. Archive names carry no version, so
# install.sh can always ask GitHub for "the latest release's
# shoal-linux-amd64.tar.gz"; the folder inside, and `shoal version`, say
# which build it is.
#
# To publish: tag the commit (git tag v1.0.0), make release, then
#   gh release create v1.0.0 dist/* --title "Shoal v1.0.0" --latest
# The installer's one-liner works once the repository is public. For a beta
# from a public download-only repository:
#   make release RELEASE_REPO=BT10011/shoal-beta
#   gh release create v1.0.0-beta.1 dist/* --repo BT10011/shoal-beta --latest --title "Shoal v1.0.0-beta.1"
# Mark it latest, not a pre-release: the installer fetches "latest".
release: check notices
	rm -rf $(DIST) && mkdir -p $(DIST)
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; folder=shoal-$(VERSION)-$$os-$$arch; \
		echo "building shoal-$$os-$$arch ($(VERSION))"; \
		mkdir -p $(DIST)/$$folder; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$$folder/shoal ./cmd/shoal; \
		cp README.md LICENSE THIRD_PARTY_NOTICES.md $(DIST)/$$folder/; \
		tar -C $(DIST) -czf $(DIST)/shoal-$$os-$$arch.tar.gz $$folder; \
		rm -rf $(DIST)/$$folder; \
	done
	cd $(DIST) && sha256sum *.tar.gz > SHA256SUMS
	sed -e 's|repo="BT10011/shoal"|repo="$(RELEASE_REPO)"|' \
	    -e 's|github.com/BT10011/shoal/|github.com/$(RELEASE_REPO)/|g' install.sh > $(DIST)/install.sh
	@echo "installer in $(DIST)/ downloads from github.com/$(RELEASE_REPO)"
	@echo "release files in $(DIST)/:" && ls -1 $(DIST)

clean:
	rm -rf bin $(DIST)
