.PHONY: build demos run test race vet fmt tidy clean check proto proto-tools

BINARY := bin/kvnode

PROTO_FILES := $(shell find proto -name '*.proto' 2>/dev/null)

build:
	go build -o $(BINARY) ./cmd/kvnode
	go build -o bin/helixcert ./cmd/helixcert

demos:
	go build -o bin/sstdemo ./cmd/sstdemo
	go build -o bin/clusterdemo ./cmd/clusterdemo
	go build -o bin/swimdemo ./cmd/swimdemo
	go build -o bin/hintdemo ./cmd/hintdemo
	go build -o bin/repairdemo ./cmd/repairdemo

run: build
	./$(BINARY)

test:
	go test ./...

race:
	go test -race ./...

vet:
	go vet ./...

fmt:
	gofmt -s -w .

tidy:
	go mod tidy

# proto-tools installs the protoc plugins into $(go env GOPATH)/bin, which must be on PATH.
proto-tools:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.34.2
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1

# proto regenerates Go from the .proto contracts. Requires protoc plus the plugins from
# proto-tools. The module option routes output to the path in each file's go_package.
proto:
	protoc \
	  --go_out=. --go_opt=module=github.com/talifpathan/helix \
	  --go-grpc_out=. --go-grpc_opt=module=github.com/talifpathan/helix \
	  $(PROTO_FILES)

clean:
	rm -rf bin data

check: fmt vet race
	@echo "check passed"
