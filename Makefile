BINARY=server
GO=go
PORT?=8080

.PHONY: run build test test-integration test-e2e test-all migrate-up migrate-down docker-up docker-down clean lint

run:
	@echo "Starting server..."
	@export $$(cat .env | grep -v '^#' | xargs) 2>/dev/null; \
	DATABASE_URL=$${DATABASE_URL:-postgres://tabletop:tabletop@localhost:5432/tabletop?sslmode=disable} \
	JWT_SECRET=$${JWT_SECRET:-dev-secret} \
	PORT=$${PORT:-8080} \
	$(GO) run ./cmd/server

build:
	CGO_ENABLED=0 $(GO) build -ldflags="-s -w" -o $(BINARY) ./cmd/server

test:
	$(GO) test ./... -v -count=1

test-integration:
	$(GO) test ./... -v -count=1 -tags=integration

test-e2e:
	cd tests && npx playwright test

seed:
	@echo "Seeding top-10 RU board games from BGG..."
	@export $$(cat .env | grep -v '^#' | xargs) 2>/dev/null; \
	DATABASE_URL=$${DATABASE_URL:-postgres://tabletop:tabletop@localhost:5432/tabletop?sslmode=disable} \
	$(GO) run ./cmd/seed

test-rag:
	$(GO) test -v -tags=rag_integration -timeout=300s ./internal/service/ -run TestCatanRAG

test-all: test test-integration test-e2e

docker-up:
	docker compose up -d

docker-down:
	docker compose down

docker-build:
	docker compose build

migrate-up:
	@echo "Migrations run automatically on startup"

clean:
	rm -f $(BINARY)
	$(GO) clean ./...

lint:
	$(GO) vet ./...

deps:
	$(GO) mod tidy
	$(GO) mod download

.env:
	cp .env.example .env
	@echo "Created .env from .env.example - edit it with your values"
