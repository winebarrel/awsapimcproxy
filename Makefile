.PHONY: all
all: vet test build

.PHONY: vet
vet:
	go vet ./...

.PHONY: test
test:
	go test -v $(TEST_OPTS) ./...

.PHONY: build
build:
	go build ./cmd/awsapimcproxy

.PHONY: install
install:
	go install ./cmd/awsapimcproxy

.PHONY: lint
lint:
	golangci-lint run
