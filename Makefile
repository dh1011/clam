VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
GOFLAGS := -trimpath
export CGO_ENABLED := 0

# Release targets: every one is a static binary with no runtime to install.
PLATFORMS := linux/amd64 linux/arm64 linux/arm linux/386 \
	darwin/amd64 darwin/arm64 \
	windows/amd64 windows/arm64 \
	freebsd/amd64 freebsd/arm64

bin/clam: $(wildcard *.go) go.mod go.sum $(wildcard shell/*)
	go build $(GOFLAGS) -ldflags '$(LDFLAGS)' -o $@ .

test:
	go vet ./...
	go test ./...

# Cross-compile every release target into dist/.
dist:
	@mkdir -p dist
	@for platform in $(PLATFORMS); do \
		os=$${platform%/*}; arch=$${platform#*/}; ext=; \
		[ $$os = windows ] && ext=.exe; \
		echo "dist/clam-$$os-$$arch$$ext"; \
		GOOS=$$os GOARCH=$$arch GOARM=7 go build $(GOFLAGS) -ldflags '$(LDFLAGS)' \
			-o dist/clam-$$os-$$arch$$ext . || exit 1; \
	done
	@cd dist && rm -f SHA256SUMS && \
		{ command -v sha256sum >/dev/null && sha256sum clam-* || shasum -a 256 clam-*; } > SHA256SUMS
	@echo dist/SHA256SUMS

clean:
	rm -rf bin dist

.PHONY: test dist clean
