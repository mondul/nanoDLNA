# nanoDLNA

BINARY  := nanoDLNA
VERSION := $(shell sed -n 's/.*Version = "\(.*\)".*/\1/p' internal/version/version.go)
LDFLAGS := -s -w
PLATFORMS := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64

.PHONY: all build test race vet fmt check cross clean run tidy

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

tidy:
	go mod tidy

cross: $(PLATFORMS)

$(PLATFORMS):
	@mkdir -p dist
	GOOS=$(word 1,$(subst /, ,$@)) GOARCH=$(word 2,$(subst /, ,$@)) \
		go build -trimpath -ldflags "$(LDFLAGS)" -o dist/$(BINARY)-$(subst /,-,$@)$(if $(findstring windows,$@),.exe,) .
	@echo "built dist/$(BINARY)-$(subst /,-,$@)"

clean:
	rm -rf dist $(BINARY)

print-version:
	@echo $(VERSION)
