# Build variables
APP_NAME := sengi
CMD_DIR := ./cmd
BIN_DIR := ./bin
PKG := github.com/tadjov/sengi
MIGRATIONS_DIR := ./db/migrations
PG_ENGINES := native paradedb tiger
LOAD_ENGINES := $(PG_ENGINES) es
# Native Postgres on 54320, ParadeDB (BM25 via @@@) on 54321 and pg_textsearch
# (TimescaleDB-HA) on 54322; see docker-compose.yml.
DB_CONN_native := postgresql://news_user:news_password@localhost:54320/news_db?sslmode=disable
DB_CONN_paradedb := postgresql://news_user:news_password@localhost:54321/news_db?sslmode=disable
DB_CONN_tiger := postgresql://news_user:news_password@localhost:54322/news_db?sslmode=disable
DB_CONN := "$(DB_CONN_native)"

# Go variables
GOOS ?= $(shell go env GOOS)
GOARCH ?= $(shell go env GOARCH)

ARGS ?=
# The model the LLM judge targets grade with; bench judge needs one named.
JUDGE_MODEL ?= sonnet

# Build commands
.PHONY: build-all build-datapipe build-news-api build-schemagen clean deps dev dc-up dc-down run-api run-api-pg run-api-es test fmt vet lint lint-fix install-lint schema-gen build-bench bench-validate bench-run bench-pool bench-judge-keyword bench-judge-cli bench-judge-api bench-qrels bench-show-spec bench-show-pool bench-show-judgments $(addprefix migrate-schema-,$(PG_ENGINES)) $(addprefix migrate-down-,$(PG_ENGINES)) migrate-schema-all migrate-down-all db-refresh-collation db-volumes run-datapipe-preprocess $(addprefix run-datapipe-load-,$(LOAD_ENGINES)) run-datapipe-load-es-text run-datapipe-load-all

$(addprefix migrate-schema-,$(PG_ENGINES)): migrate-schema-%:
	@echo "Migrating the schema ($*)..."
	@migrate -path $(MIGRATIONS_DIR)/$*/schema -database "$(DB_CONN_$*)" up

# Dropping the tables drops their indexes too. `migrate down` prompts for confirmation.
$(addprefix migrate-down-,$(PG_ENGINES)): migrate-down-%:
	@echo "Dropping the schema ($*)..."
	@migrate -path $(MIGRATIONS_DIR)/$*/schema -database "$(DB_CONN_$*)" down

migrate-schema-all: $(addprefix migrate-schema-,$(PG_ENGINES))

migrate-down-all: $(addprefix migrate-down-,$(PG_ENGINES))

# Clears the collation version mismatch warning. Re-run the FTS track afterwards
# and confirm the metrics are unchanged.
db-refresh-collation:
	@echo "Reindexing and refreshing collation version (native pg)..."
	@psql $(DB_CONN) -c "REINDEX DATABASE news_db;"
	@psql $(DB_CONN) -c "ALTER DATABASE news_db REFRESH COLLATION VERSION;"

# Lists every engine volume on disk and marks the namespace currently selected
# by NEWS_VOLUME_NS in .env (see docker-compose.yml).
db-volumes:
	@echo "Selected namespace: $$(docker compose config --format json | python3 -c 'import json,sys; print(json.load(sys.stdin)["volumes"]["pg_news_data"]["name"].removesuffix("_pg_news_data"))')"
	@docker volume ls --format '{{.Name}}' | grep -E '_(pg_news|es_news)' | sort

# Build all commands
build-all: build-datapipe build-news-api build-schemagen build-bench

build-datapipe:
	@echo "Building datapipe..."
	@mkdir -p $(BIN_DIR)
	@go build -o $(BIN_DIR)/datapipe $(CMD_DIR)/datapipe

build-news-api:
	@echo "Building news-api..."
	@mkdir -p $(BIN_DIR)
	@go build -o $(BIN_DIR)/news-api $(CMD_DIR)/news_api

build-schemagen:
	@echo "Building schema generator..."
	@mkdir -p $(BIN_DIR)
	@go build -o $(BIN_DIR)/schemagen $(CMD_DIR)/schemagen

# Generate the mapping and data package JSON schemas from Go structs
schema-gen: build-schemagen
	@echo "Generating schemas..."
	@./$(BIN_DIR)/schemagen -output=api/mapping-spec -datapackage-output=api/datapackage-spec

# Development commands
test:
	@echo "Running tests..."
	@go test -v ./...

fmt:
	@echo "Formatting code..."
	@go fmt ./...

vet:
	@echo "Vetting code..."
	@go vet ./...

GOLANGCI_LINT_VERSION ?= v2.12.2

install-lint:
	@echo "Installing golangci-lint $(GOLANGCI_LINT_VERSION)..."
	@go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION)

lint:
	@echo "Linting code..."
	@golangci-lint run ./...

lint-fix:
	@echo "Linting code (with autofix)..."
	@golangci-lint run --fix ./...

# Database commands
dc-up:
	@echo "Starting database..."
	@docker compose up -d

dc-down:
	@echo "Stopping database..."
	@docker compose down

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	@rm -rf $(BIN_DIR)

# Install dependencies
deps:
	@echo "Installing dependencies..."
	@go mod download
	@go mod tidy

# Preprocess a raw data package into a canonical package:
#   make run-datapipe-preprocess DATASET=cc-news
DATASET ?=
PREPROCESS_FLAGS ?=

run-datapipe-preprocess: build-datapipe
	@echo "Running datapipe preprocess ($(DATASET))..."
	@./$(BIN_DIR)/datapipe preprocess "datasets/$(DATASET)/raw" \
		--mapping "datasets/$(DATASET)/mapping.yaml" --output "datasets/$(DATASET)/canonical" $(PREPROCESS_FLAGS)

# Load a canonical package, and optionally its embeddings, into one engine:
#   make run-datapipe-load-native CANONICAL=datasets/cc-news/canonical/<version>/datapackage.json \
#       EMBEDDINGS=datasets/cc-news/embeddings/<tag-dir>/<version>/datapackage.json
# LOAD_FLAGS passes extra flags, e.g. LOAD_FLAGS="--batch-size 5000 --trial".
ENGINE_ENV_native := cmd/datapipe/pg.env
ENGINE_ENV_paradedb := cmd/datapipe/parade.env
ENGINE_ENV_tiger := cmd/datapipe/tiger.env
ENGINE_ENV_es := cmd/datapipe/es.env
CANONICAL ?=
EMBEDDINGS ?=
LOAD_FLAGS ?=

$(addprefix run-datapipe-load-,$(LOAD_ENGINES)): run-datapipe-load-%: build-datapipe
	@echo "Running datapipe load ($*)..."
	@ENV_PATHS="$(ENGINE_ENV_$*)" ./$(BIN_DIR)/datapipe load "$(CANONICAL)" --engine $* \
		$(if $(EMBEDDINGS),--embeddings "$(EMBEDDINGS)") $(LOAD_FLAGS)

# ES keeps each vector inside its article, so its vector cost is a full load minus this one.
run-datapipe-load-es-text: build-datapipe
	@echo "Running datapipe load (es, without embeddings)..."
	@ENV_PATHS="$(ENGINE_ENV_es)" ./$(BIN_DIR)/datapipe load "$(CANONICAL)" --engine es $(LOAD_FLAGS)

# Every engine loads the same packages, one after another, so the corpora stay
# identical and no load competes with another for the machine.
run-datapipe-load-all:
	@for engine in $(LOAD_ENGINES); do $(MAKE) --no-print-directory run-datapipe-load-$$engine || exit 1; done

run-api: build-news-api
	@echo "Running news search service..."
	@ENV_PATHS="cmd/news_api/.env" ./$(BIN_DIR)/news-api

run-api-pg: build-news-api
	@echo "Running news search service..."
	@ENV_PATHS="cmd/news_api/.env,cmd/news_api/pg.env" ./$(BIN_DIR)/news-api

run-api-es: build-news-api
	@echo "Running news search service..."
	@ENV_PATHS="cmd/news_api/.env,cmd/news_api/es.env" ./$(BIN_DIR)/news-api

# Benchmark commands
build-bench:
	@echo "Building bench..."
	@mkdir -p $(BIN_DIR)
	@go build -o $(BIN_DIR)/bench $(CMD_DIR)/bench

# BENCH_TRACK_ROOT is where bench resolves relative track paths, so targets name
# the track alone. Override either half, e.g.
#   make bench-run TRACK=semantic
#   make bench-run BENCH_TRACK_ROOT=tracks/global-news-dataset TRACK=fts_quality
BENCH_TRACK_ROOT ?= tracks/cc-news
TRACK ?= fts
export BENCH_TRACK_ROOT

bench-validate: build-bench
	@./$(BIN_DIR)/bench validate $(TRACK)

bench-run: build-bench
	@./$(BIN_DIR)/bench run $(TRACK)

bench-pool: build-bench
	@./$(BIN_DIR)/bench pool $(TRACK)

bench-judge-keyword: build-bench
	@./$(BIN_DIR)/bench judge $(TRACK) --strategy keyword

bench-judge-cli: build-bench
	@./$(BIN_DIR)/bench judge $(TRACK) --strategy llm --provider claude-cli --model "$(JUDGE_MODEL)" --resume

bench-judge-api: build-bench
	@./$(BIN_DIR)/bench judge $(TRACK) --strategy llm --provider claude-api --model "$(JUDGE_MODEL)" --resume

bench-show-spec: build-bench
	@./$(BIN_DIR)/bench show spec $(TRACK)

bench-show-pool: build-bench
	@./$(BIN_DIR)/bench show pool $(TRACK)

bench-show-judgments: build-bench
	@./$(BIN_DIR)/bench show judgments $(TRACK) --judgments keyword

bench-qrels: build-bench
	@./$(BIN_DIR)/bench export $(TRACK) --format qrels --judgments keyword

# Development workflow
dev: fmt vet lint test build-all

.DEFAULT_GOAL := build-all