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

# Stage the website for Vercel. It is served under /clam so that duy.run/clam
# can proxy to it with the same paths; docs/vercel.json sits at the root.
VERCEL_PROJECT_ID ?= prj_06MB4C7Svb58oxsFSXWIerzsYV10
VERCEL_ORG_ID ?= team_0Tu1XobSVZ3i8Ur6OVJQEI8Z
SITE_FILES := index.html llms.txt logo.svg social.png demo.gif

site: $(addprefix docs/,$(SITE_FILES)) docs/vercel.json
	rm -rf site && mkdir -p site/clam
	cp docs/vercel.json site/
	cp $(addprefix docs/,$(SITE_FILES)) site/clam/

# Deploy the staged site to the Vercel project "clam" (needs VERCEL_TOKEN).
deploy-site: site
	mkdir -p site/.vercel
	printf '{"projectId":"%s","orgId":"%s"}' "$(VERCEL_PROJECT_ID)" "$(VERCEL_ORG_ID)" > site/.vercel/project.json
	cd site && npx -y vercel@latest deploy --prod --yes --token "$$VERCEL_TOKEN"

clean:
	rm -rf bin dist site

.PHONY: test dist site deploy-site clean
