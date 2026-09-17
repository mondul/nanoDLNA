# nanoDLNA

BINARY  := nanoDLNA
MODULE  := nanodlna

# A release build passes VERSION explicitly. Otherwise take the tag this commit
# sits on, and fall back to the development default in the source when there is
# no tag yet.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null | sed 's/^v//')
ifeq ($(strip $(VERSION)),)
VERSION := $(shell sed -n 's/.*Version = "\(.*\)".*/\1/p' internal/version/version.go)
endif

LDFLAGS := -s -w -X $(MODULE)/internal/version.Version=$(VERSION)

# The platforms a release ships, matching .github/workflows/release.yml.
PLATFORMS := linux/amd64 linux/arm64 darwin/arm64 windows/amd64 windows/arm64

.PHONY: all build test race vet fmt check lint hooks cross clean run tidy print-version $(PLATFORMS)

all: build

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BINARY) .

run: build
	./$(BINARY) -log debug

test:
	go test ./...

race:
	go test -race -count=1 ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w .

# check fails if any file needs reformatting.
check:
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "not gofmt'd:"; echo "$$out"; exit 1; fi
	go vet ./...
	go test -count=1 ./...

# lint checks that the commits follow Conventional Commits, which the release
# workflow depends on to work out the next version.
lint:
	./scripts/commit-lint.sh --all

# hooks installs the commit-msg hook for this clone.
hooks:
	git config core.hooksPath .githooks
	@echo "commit-msg hook enabled; bypass one commit with: git commit --no-verify"

tidy:
	go mod tidy

cross: $(PLATFORMS)

$(PLATFORMS):
	@mkdir -p dist
	GOOS=$(word 1,$(subst /, ,$@)) GOARCH=$(word 2,$(subst /, ,$@)) \
		go build -trimpath -ldflags "$(LDFLAGS)" \
		-o dist/$(BINARY)-$(VERSION)-$(subst /,-,$@)$(if $(findstring windows,$@),.exe,) .
	@echo "built dist/$(BINARY)-$(VERSION)-$(subst /,-,$@)"

clean:
	rm -rf dist $(BINARY)

print-version:
	@echo $(VERSION)
