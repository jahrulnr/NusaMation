BINARY := bin/nusamation
FAKE := bin/fake-acp-agent

.PHONY: all build fake run test vet fmt check clean

all: build

build:
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o $(BINARY) ./cmd/nusamation

fake:
	@mkdir -p bin
	CGO_ENABLED=0 go build -trimpath -o $(FAKE) ./cmd/fake-acp-agent

run:
	go run ./cmd/nusamation

test:
	go test ./...

vet:
	go vet ./...

fmt:
	go fmt ./...

check: fmt vet test

clean:
	rm -rf bin
