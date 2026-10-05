.PHONY: dev check generate test build lint vet fmt

dev:
	templ generate --watch & go run ./cmd/app --vault fixtures/p01

generate:
	templ generate

check: generate
	git diff --exit-code
	go vet ./...
	gofmt -l .
	golangci-lint run
	go test ./...

build:
	go build ./...

test:
	go test ./...

lint:
	golangci-lint run

vet:
	go vet ./...

fmt:
	gofmt -l .

tidy:
	go mod tidy