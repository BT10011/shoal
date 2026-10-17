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
# machine with CGO disabled. Windows on Arm is built but has only been tried
# on x86-64 Windows.
PLATFORMS := linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 freebsd/amd64 windows/amd64 windows/arm64
DIST := dist

.PHONY: build build-windows test vet race check demo iface setcap notices release snapshot clean

build:
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/shoal

# A Windows build from any machine, for trying on a Windows box.
build-windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN).exe ./cmd/shoal

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
# bin/shoal --setup does the same, and on macOS opens the BPF devices to the
# access_bpf group. See "Full network access" in README.md.
setcap: build
	sudo setcap cap_net_raw+ep $(BIN)

# The licence texts of everything compiled in, which the MIT and BSD
# licences of shoal's dependencies ask to travel with the binary.
notices:
	GO=$(GO) PLATFORMS="$(PLATFORMS)" ./scripts/third-party-notices.sh THIRD_PARTY_NOTICES.md

# Releases are built and published by .github/workflows/release.yml, with
# GoReleaser (.goreleaser.yaml), when a version tag is pushed:
#   git tag -a v2.0.0 -m "Shoal v2.0.0" && git push origin v2.0.0
# That is the only way a release is made, so every file in one can be
# traced to the workflow and the tag. `make snapshot` builds the same files
# locally, packages and all, without publishing anything.
#
# `make release` is the older local build: one archive per platform, each
# holding the binary, the README, the licence and the third-party notices,
# plus SHA256SUMS and the installer, named exactly as the workflow names
# them. It stays for a beta from a public download-only repository:
#   make release RELEASE_REPO=BT10011/shoal-beta
#   gh release create v2.1.0-beta.1 dist/* --repo BT10011/shoal-beta --latest --title "Shoal v2.1.0-beta.1"
# Mark it latest, not a pre-release: the installer fetches "latest". A
# beta has no record of its build, so install it with SHOAL_SKIP_PROVENANCE=1.
release: check notices
	rm -rf $(DIST) && mkdir -p $(DIST)
	@set -e; for p in $(PLATFORMS); do \
		os=$${p%/*}; arch=$${p#*/}; folder=shoal-$(VERSION)-$$os-$$arch; \
		echo "building shoal-$$os-$$arch ($(VERSION))"; \
		mkdir -p $(DIST)/$$folder; \
		exe=shoal; [ $$os = windows ] && exe=shoal.exe; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/$$folder/$$exe ./cmd/shoal; \
		cp README.md LICENSE THIRD_PARTY_NOTICES.md $(DIST)/$$folder/; \
		if [ $$os = windows ]; then \
			(cd $(DIST) && zip -qrX shoal-$$os-$$arch.zip $$folder); \
		else \
			tar -C $(DIST) -czf $(DIST)/shoal-$$os-$$arch.tar.gz $$folder; \
		fi; \
		rm -rf $(DIST)/$$folder; \
	done
	cd $(DIST) && sha256sum *.tar.gz *.zip > SHA256SUMS
	sed -e 's|repo="BT10011/shoal"|repo="$(RELEASE_REPO)"|' \
	    -e 's|github.com/BT10011/shoal/|github.com/$(RELEASE_REPO)/|g' install.sh > $(DIST)/install.sh
	@echo "installer in $(DIST)/ downloads from github.com/$(RELEASE_REPO)"
	@echo "release files in $(DIST)/:" && ls -1 $(DIST)

# The workflow's build, run here with nothing published. Needs goreleaser
# (https://goreleaser.com); dist/ ends up holding what a release would.
snapshot:
	goreleaser release --snapshot --clean

clean:
	rm -rf bin $(DIST)
