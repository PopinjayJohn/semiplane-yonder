.PHONY: dev check generate test build lint vet fmt tools fuzz-short bench budgets snapshot smoke

TEMPL_VERSION := v0.3.1070
GOLANGCI_VERSION := v2.14.0
FUZZ_TIME ?= 20s

dev:
	templ generate --watch & go run ./cmd/app --vault fixtures/p01

generate:
	templ generate

# Quality gate (spec §8, P10): keep this green on every PR. Bench runs inside
# `go test ./...` (numbers in the log); `make bench` additionally records
# internal/bench/results.json.
check: generate
	git diff --exit-code
	go vet ./...
	test -z "$$(gofmt -l .)"
	golangci-lint run ./...
	go test ./...
	$(MAKE) budgets

build:
	go build ./...

test:
	go test ./...

# fuzz-short runs each Go fuzz target for $(FUZZ_TIME) (P10 gate).
fuzz-short:
	FUZZ_TIME="$(FUZZ_TIME)" ./tools/fuzz-short.sh

# bench runs the P04 5k harness and records internal/bench/results.json.
# Override size locally with BENCH_FILES=500 (CI runs the full 5k via go test).
bench:
	BENCH_RECORD=1 go test ./internal/bench/ -run TestBench5k -v -count=1

budgets:
	go run ./tools/budgets

# snapshot builds all 3 OS binaries without publishing (spec §8).
snapshot:
	goreleaser release --snapshot --clean --skip=publish

# smoke boots serve after a fresh init --bare and asserts the M1
# guest/owner/GM matrix over HTTP (G2 amend regression cover). Not part of
# `check`: it binds a test port and takes a full boot cycle; CI runs it as
# the serve-smoke job.
smoke:
	./tools/serve-smoke.sh

lint:
	golangci-lint run ./...

vet:
	go vet ./...

fmt:
	gofmt -l .

# tools installs the pinned dev toolchain (CI installs its own copies).
tools:
	go install github.com/a-h/templ/cmd/templ@$(TEMPL_VERSION)
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_VERSION)

tidy:
	go mod tidy
