# Get the latest commit branch, hash, and date
TAG=$(shell git describe --tags --abbrev=0 --exact-match 2>/dev/null)
BRANCH=$(if $(TAG),$(TAG),$(shell git rev-parse --abbrev-ref HEAD 2>/dev/null))
HASH=$(shell git rev-parse --short=7 HEAD 2>/dev/null)
TIMESTAMP=$(shell git log -1 --format=%ct HEAD 2>/dev/null | xargs -I{} date -u -r {} +%Y%m%dT%H%M%S)
GIT_REV=$(shell printf "%s-%s-%s" "$(BRANCH)" "$(HASH)" "$(TIMESTAMP)")
REV=$(if $(filter --,$(GIT_REV)),latest,$(GIT_REV))

all: test build

build:
	go build -ldflags "-X main.revision=$(REV) -s -w" -o .bin/revmux.$(BRANCH) ./app
	cp .bin/revmux.$(BRANCH) .bin/revmux

test:
	go clean -testcache
	go test -race -coverprofile=coverage.out ./...
	grep -v "_mock.go" coverage.out | grep -v mocks > coverage_no_mocks.out
	go tool cover -func=coverage_no_mocks.out
	rm coverage.out coverage_no_mocks.out

lint:
	golangci-lint run --max-issues-per-linter=0 --max-same-issues=0

fmt:
	gofmt -s -w $(shell find . -type f -name "*.go" -not -path "./vendor/*" -not -path "*/mocks/*")
	goimports -w $(shell find . -type f -name "*.go" -not -path "./vendor/*" -not -path "*/mocks/*")

# the executor tests re-exec the race-instrumented test binary once per supervised run, so this
# package alone costs ~40s on a fast machine; the timeout is headroom for slower hardware, not a budget
race:
	go test -race -timeout=300s ./...

version:
	@echo "branch: $(BRANCH), hash: $(HASH), timestamp: $(TIMESTAMP)"
	@echo "revision: $(REV)"

.PHONY: all build test lint fmt race version
