export PATH := $(shell go env GOPATH)/bin:$(PATH)

GO      ?= go
SERVICES = identity catalog order payment logistics gateway
COMPOSE   = docker compose -f deploy/docker-compose.yml

.PHONY: all build test vet lint proto fmt tidy up down logs smoke clean web web-dev web-build web-lint

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

# --- web app -------------------------------------------------------------
web:            ## build the SPA image (served by the compose stack on :8089)
	$(COMPOSE) up -d --build web

web-dev:        ## Vite dev server on :5173, /v1 proxied to gateway :8080
	cd web && npm run dev

web-build:      ## typecheck + production bundle into web/dist
	cd web && npm run build

web-lint:
	cd web && npm run lint

clean:
	rm -rf bin
