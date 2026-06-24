APP_NAME := alertmesh
BUILD_DIR := bin
GO := go
GOFLAGS := -trimpath
LDFLAGS := -s -w

.PHONY: all build run clean test lint migrate-up migrate-down docker-build docker-up docker-down \
        web-install web-dev web-build

all: build

build:
	CGO_ENABLED=0 $(GO) build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $(BUILD_DIR)/$(APP_NAME) ./cmd/alertmesh

run:
	$(GO) run ./cmd/alertmesh

clean:
	rm -rf $(BUILD_DIR)

test:
	$(GO) test ./... -v -cover

lint:
	golangci-lint run ./...

migrate-up:
	migrate -path migrations -database "$(ALERTMESH_DATABASE_DSN)" up

migrate-down:
	migrate -path migrations -database "$(ALERTMESH_DATABASE_DSN)" down

docker-build:
	docker build -t $(APP_NAME):latest -f deploy/docker/Dockerfile .

docker-up:
	docker compose -f deploy/docker/docker-compose.yml up -d

docker-down:
	docker compose -f deploy/docker/docker-compose.yml down

# ─── Frontend ─────────────────────────────────────────────────────────────────

web-install:
	cd web && npm install

web-dev:
	cd web && npm run dev

web-build:
	cd web && npm run build
