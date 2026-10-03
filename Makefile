export PATH := $(shell go env GOPATH)/bin:$(PATH)

GO      ?= go
SERVICES = identity catalog order payment logistics gateway
COMPOSE   = docker compose -f deploy/docker-compose.yml

.PHONY: all build test vet lint proto fmt tidy up down logs smoke clean

all: lint test build

build:
	@mkdir -p bin
	@for s in $(SERVICES); do \
		echo "building $$s"; \
		$(GO) build -o bin/$$s ./services/$$s/cmd/server || exit 1; \
	done

test:
	$(GO) test ./...

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

lint: lint-proto lint-go

lint-proto:
	buf lint

lint-go:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./... ; \
	else \
		echo "golangci-lint not installed; running go vet instead"; \
		$(GO) vet ./... ; \
	fi

proto:
	buf lint
	buf generate

fmt:
	gofmt -w pkg services gen
	$(GO) mod tidy

tidy:
	$(GO) mod tidy

up:
	$(COMPOSE) up -d --build

down:
	$(COMPOSE) down -v

logs:
	$(COMPOSE) logs -f --tail=100

smoke:
	bash scripts/e2e.sh

clean:
	rm -rf bin
