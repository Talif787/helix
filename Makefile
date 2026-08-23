.PHONY: build run test race vet fmt tidy clean check

BINARY := bin/kvnode

build:
	go build -o $(BINARY) ./cmd/kvnode

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
