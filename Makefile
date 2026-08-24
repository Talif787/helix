.PHONY: build demos run test race vet fmt tidy clean check

BINARY := bin/kvnode

build:
	go build -o $(BINARY) ./cmd/kvnode

demos:
	go build -o bin/sstdemo ./cmd/sstdemo
	go build -o bin/clusterdemo ./cmd/clusterdemo
	go build -o bin/swimdemo ./cmd/swimdemo

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

clean:
	rm -rf bin data

check: fmt vet race
	@echo "check passed"
