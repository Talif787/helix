.PHONY: build demos run test race vet fmt tidy clean check proto proto-tools docker-build docker-up docker-down docker-logs

BINARY := bin/kvnode

PROTO_FILES := $(shell find proto -name '*.proto' 2>/dev/null)

build:
	go build -o $(BINARY) ./cmd/kvnode
	go build -o bin/helixcert ./cmd/helixcert
	go build -o bin/helixctl ./cmd/helixctl

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
	gofmt -s -w $(shell find . -name '*.go' -not -path './vendor/*')

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

# docker-build builds the runtime image; docker-up brings up the three-node cluster defined in
# docker-compose.yml; docker-down stops it and removes the data volumes. The image builds from
# the committed vendor/ tree, so it needs no network at build time.
docker-build:
	docker build -t helix:local .

docker-up:
	docker compose up --build -d

docker-down:
	docker compose down -v

docker-logs:
	docker compose logs -f

check: fmt vet race
	@echo "check passed"
