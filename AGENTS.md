# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Sengi is a full-text and semantic search engine for exploring multilingual news headlines and articles. The project uses Go 1.25 and focuses on importing, processing, and storing news data from various sources (currently Kaggle datasets).

## Research Goals

**Master Thesis**: "PostgreSQL as a Search Engine"

This project is part of a master thesis research that explores PostgreSQL's comprehensive search capabilities and compares its performance with Elasticsearch through extensive benchmarks across multiple search paradigms.

**Search Paradigms to Explore**:
1. **Full-Text Search**: Traditional keyword-based full-text search with relevance ranking
2. **Structured Search**: Boolean operators and field-specific queries
3. **Fuzzy/Approximate Search**: Trigram similarity (pg_trgm), Levenshtein distance for typo tolerance
4. **Semantic Search**: Vector-based similarity search using embeddings
5. **Hybrid Search**: Combining multiple search approaches for optimal results

**Key Goal**: Evaluate PostgreSQL as a comprehensive alternative to dedicated search engines like Elasticsearch, exploring capabilities beyond basic exact matching and simple filtering.


## Design Principles
- You MUST write clean, idiomatic Go code following best practices.
- You MUST organize code into clear packages with single responsibility.
- You MUST NOT write unnecessary comments; code should be self-explanatory.
- You MUST write unit tests for all core logic with high coverage.

## Architecture

The project follows a layered architecture pattern:

- **cmd/**: Entry points for different operations
    - `datapipe/`: Unified data-pipeline CLI. The core path is **preprocess → embed → load**; every stage after the first reads the canonical package, which is what keeps article IDs identical across engines.
        - `datapipe preprocess`: Maps a raw data package (`.csv`, `.jsonl`, `.parquet` resources) through `datasets/<ds>/mapping.yaml` into a canonical data package at `datasets/<ds>/canonical/<version>/` (`--version`, default `v1`), deduplicated by the mapping's `identity` rules; a rerun that yields the same articles is a no-op, and one that yields different articles under an existing version is refused
        - `datapipe embed <canonical-descriptor> [--version v1] [--batch-size 64] [--max-tokens 512] [--limit N]`: Embeds each article's `lead-v1` text through Ollama (`EMBEDDING_BASE_URL`, `EMBEDDING_MODEL`, `EMBEDDING_DIM`) into an embeddings package at `datasets/<ds>/embeddings/<tag-dir>/<version>/`, the format `embed_corpus.py` writes on a GPU; `producedBy` tells the two apart, and an existing version is refused before anything is embedded
        - `datapipe embed --embeddings <descriptor> --truncate-to N [--version v1] [--out <dir>]`: Cuts an existing embeddings package's vectors to their first N dimensions, L2-renormalised, with no model run, into `datasets/<ds>/embeddings/<model>@N/<version>/`, or a track's query vectors into `--out`; the descriptor records the source under `derivedFrom`
        - `datapipe load <canonical-descriptor> --engine <native|paradedb|tiger|es> [--embeddings <descriptor>]`: Verifies the canonical package and the embeddings package built from it, loads both into an empty engine, builds the indexes afterwards as timed stages, and writes the load record to the engine and to `datasets/<ds>/loads/<engine>/` — see [docs/embeddings.md](docs/embeddings.md)
    - `news_api/`: HTTP API server for search functionality
    - `schemagen/`: Schema generation utilities
    - `bench/`: IR benchmark CLI — see [docs/bench.md](docs/bench.md)

- **internal/**: Core business logic organized by domain
    - `types/`: Core type definitions organized by bounded contexts
        - `document/`: Document types (Article, Metadata). A field is promoted onto `Article` — and gets its own column — only when an engine must filter, sort or rank on it; `PublishedAt` and `PublisherDomain` are the current examples. Everything else lives in `Metadata`, which pairs structured fields (`PublisherName`, `Authors`, `Section`, `Keywords`, `Image`, `DateModified`) with an open `Extra map[string]any` for dataset-specific values.
        - `language/`: The ISO 639-3 corpus language (`language.Code`, only `eng` today) and the text analysis each engine runs for it.
        - `query/`: Query types (search query types, language, scoring, cursor)
        - `operator/`: Operator value object (AND/OR logic)
    - `ingest/`: Data ingestion pipeline
        - `reader/`: CSV/JSONL/Parquet/YAML data reading and parsing
        - `datamapping/`: Data mapping type definitions
    - `api/`: API layer for HTTP server (`router/` routes, `dto/` request and response shapes, `middleware/` request logging, `server/` Echo setup and config)
    - `schema/`: JSON schema generation from Go struct tags, used by `cmd/schemagen`
    - `env/`: `.env` loading (`ENV_PATHS`) and duration settings
    - `testcontainer/`: Postgres and Elasticsearch for integration tests: one server per engine shared by a test run, a database per test; `PGConfig{Isolated: true}` for a test that stops or reconfigures its server
    - `storage/`: Storage abstractions and implementations
        - `factory/`: Storage config from the environment, and the corpus loader, article reader and vector store built from it
        - `pg/`: PostgreSQL storage implementation with full-text search
        - `es/`: Elasticsearch storage implementation
    - `progress/`: The counter long-running domain code reports to (records read, rows copied, queries run); `internal/cli` draws it as one status line on stderr
    - `resilience/`: Retry policy for HTTP calls: 408, 429 and 5xx or a dropped connection, honouring `Retry-After`, else backing off with jitter; only the `jev` judge uses it today

- **api/**: API schemas and examples
    - Data mapping configuration examples and JSON schemas


- **tracks/**: Benchmark tracks — each a self-contained folder with `spec.yaml`, `suite.yaml`, `trec/` (pool + judgments), and `reports/`
- **db/**: Database-related files
    - `migrations/`: SQL migrations per Postgres engine, `<engine>/schema/` (golang-migrate) and `<engine>/indexes.sql` (the search indexes, and on native and tiger the `corpus_words` table fuzzy search expands query words against, built after the load as timed stages), embedded by the `migrations` Go package. On native and tiger, `schema/002_fuzzy_functions` holds the `fuzzy_*` functions the fuzzy templates call: `fuzzy_expansions` (each word's corpus words within Elasticsearch's AUTO budget, at most 50), `fuzzy_tsquery` on native and `fuzzy_words` on tiger
    - `query/`: SQL query files for database operations
- **datasets/**: Per-dataset mappings, canonical packages, embeddings and load records
- **scripts/**: Utility scripts for setup and maintenance

## Key Components

### Type System Organization

The type system is organized into bounded contexts, each with its own package:

**Benefits of this organization**:
- Clear separation of concerns between document and query types
- Reduced import cycles through proper package boundaries
- Clean namespacing (e.g., `query.String`, `document.Article`)
- Easier to understand and navigate type definitions
- Follows DDD bounded context principles
- Query API design follows industry standards (Elasticsearch `query_string` terminology)

### Data Mapping System
The project uses YAML configuration files to map source data fields to internal Article structure. Configuration files follow the DataMapping schema (`api/mapping-spec/datamapping-v1.json`) with fieldMappings that specify source/target fields and their source types, a `constants:` block (`Language: eng`) and `output.format` (`parquet` or `jsonl`). An unknown key, two sources on one target, or a target outside the list fails the load. `Title` and `URL` are required on every mapping, and `PublisherDomain` is always derived from the url.

Every mapping has an `identity` block that decides which records are one article and how each article gets its id. Preprocess reads records in input order and keeps the first of those that are one article. Records with the same url, as normalised, are always one article, and the url a duplicate carries stays with the article it repeats. No other url spelling is merged, since two spellings can hold different stories.

- `unique` (optional) lists rules, each a `name` and the `fields` no two articles share, compared with whitespace collapsed. A record that repeats a kept article's fields is dropped, as an insert would be under SQL `UNIQUE`, and one with any of those fields empty never matches. The fields are `Title`, `Description`, `Content`, `PublisherDomain`, `Metadata.ExternalID`, `Metadata.PublisherName` and `Metadata.Section`.
- `idKind` (required) is `urlHash`, the uuidv5 of the url, so the same input always yields the same ids and preprocess can be re-run without orphaning pools, qrels, or embeddings. `random` mints a new uuidv7 per article on every run, so a rerun never reproduces a published version.

The report counts each drop under `deduped`, keyed by the rule that matched (`url` for a repeated url), and the canonical descriptor records the block with the id algorithm. No mapping sets the id directly: a dataset's own id, or any column unique per record, maps to `Metadata.ExternalID` so a canonical row can be traced back to its raw record.

```yaml
identity:
  unique:
    - name: same-page
      fields: [PublisherDomain, Title, Content]
  idKind: urlHash
```

### Storage Layer
Follows idiomatic Go patterns with sub-package organization:
- **Interfaces**: Storage contracts define clear responsibilities
    - `FullTextBackend` plus one searcher per full-text paradigm: `StringQuerySearcher`, `FieldSearcher`, `MultiFieldSearcher`, `PhraseSearcher`, `BooleanSearcher`
    - `CorpusLoader`: Loads a canonical package, and its embeddings, into an empty engine; what `datapipe load` uses
    - `Indexer`, `EmbedIndexer`: Write one article, or one article's vector, at a time; nothing in the CLIs calls them today
    - `Reader`: Document retrieval operations
- **PostgreSQL**: `storage/pg` holds what every Postgres engine shares (pool, corpus loader, reader, index description); `storage/pg/native` holds the searchers the API serves from the native schema
- **Elasticsearch**: `storage/es` - corpus loader, per-document indexer and searchers
- **In-memory**: `storage/in_mem` - a placeholder backend for development

**Key Features**:
- SearchResult with pagination metadata (total, hasMore, page info)
- PostgreSQL uses native tsvector with ts_rank for relevance scoring
- Separate interfaces for search and loading

### Bench CLI (`cmd/bench/`)

TREC-style IR evaluation pipeline. Full docs: [docs/bench.md](docs/bench.md).

**Package layout** (`internal/bench/`):
- `trackctx/` — resolves track folder + all artifact paths; single source of truth for every subcommand
- `spec/` — `BenchSpec` YAML: engines, jobs, metrics config, `defaults.judgments`
- `suite/` — `TestSuite` YAML: queries, per-engine templates
- `dialect/` — the syntax each engine type reads suite params in (postgres positional, elasticsearch Mustache, api literal): load-time block checks and the request each executor receives
- `pool/` — TREC-style candidate pooling, and the pool file assembled from a pooling run
- `judgment/` — strategy taxonomy (keyword / claude-cli / claude-api / manual); incremental writer
- `runner/` — orchestration: warmup + measured iterations, per-query metrics
- `rungate/` — decides whether a finished run can stand as its track's result (every cell answered, ranked stably, scored against judgments graded on the current pool)
- `report/` — aggregation, JSON output, and one table view rendered to the terminal, Markdown and HTML
- `metrics/` — NDCG, MAP, MRR, Bpref, P/R/F1
- `agreement/` — how alike judgment sets grade the same pairs and rank a run's engines (`bench diff judgments`)
- `meta/` — provenance block embedded in every artifact (run_id, tool, generated_at, sources)
- `version/` — schema version constant + checker
- `artifact/` — YAML reading with the schema check, and atomic writes, for pool and judgment files

**Track convention**: a track is any folder holding `spec.yaml` + `suite.yaml` + `trec/` (+ `reports/`) — the layout above it is not enforced; this repo groups them as `tracks/<dataset>/<paradigm>/`. Track args are ordinary paths: absolute, relative to the track root (`--track-root` / `BENCH_TRACK_ROOT`, default cwd), a glob, or omitted to walk up from CWD.

**Strategy taxonomy**: `keyword` (token-overlap), `bm25` (pool-local Okapi BM25), `vector` (embedding cosine), `hybrid` (BM25 + vector fusion), `claude-cli` / `claude-api` (LLM, one doc per request), `jev` (TypeSafe's Jev, one doc per request, a non-Anthropic cross-judge), `manual` (human placeholders). `vector`/`hybrid` read doc vectors from a storage-agnostic `storage.VectorStore` (PG `article_embeddings` now, ES stubbed; PG precedence) and take query vectors from the track's `trec/query-vectors/` package when it has one, from Ollama otherwise (`--pg` + `EMBEDDING_BASE_URL`). `validate`/`pool`/`run` fill the reserved `query_vector` arg from the same source.

**Schema v1**: every artifact has `schema_version: 1` + `meta:` block. Loading without it is a hard error — no backward-compat tolerance.

### HTTP API Server
Built with Echo framework providing:
- **Search API**: Comprehensive search capabilities with multiple query types
- **Health Checks**: Database connectivity and service health monitoring
- **Middleware**: CORS support, request logging, and error recovery
- **Configuration**: Environment-based config with validation
- **Graceful Shutdown**: Proper resource cleanup on termination

**Search API Endpoints** (`internal/api/router/search.go`; request shapes and status codes in [docs/API_DOCUMENTATION.md](docs/API_DOCUMENTATION.md)):

| Method and path | Serves |
|---|---|
| `GET /v1/articles/search?q=...` | free-text search box over title, description and content; takes `q`, `size`, `cursor`, `lang` |
| `POST /v1/articles/_search` | structured search: the body holds one of `match`, `multi_match`, `phrase`, `boolean`, `hybrid` under `query` |
| `GET /v1/articles/semantic_search?q=...` | vector similarity search; routed only when embeddings are enabled |
| `GET /v1/capabilities` | the query types the backend serves and the full-text parameters its engine honours |
| `GET /health` | backend connectivity |

**Search contract**:
- A stated parameter is honoured or the request is a 400; an unknown query parameter or JSON key is a 400 too
- An omitted operator takes the engine's default (PostgreSQL AND, Elasticsearch OR); every response echoes the contract it ran under in `effective`
- `Prefer: handling=lenient` drops an unsupported parameter and lists it in `ignored` instead of a 400
- Cursor-based pagination: `next_cursor`, `has_more`, `total_matches`
- Query language is an ISO 639-3 code: `eng` only (the default); any other value returns 400
- PostgreSQL: tsvector with ts_rank scoring. Elasticsearch: BM25 scoring, and the only engine with fuzziness

**API Design Pattern**:
- **Simple API**: Application-optimized (`query.String`)
- **Explicit API**: User-controlled (`Match`, `MultiMatch`, `Phrase`, `Boolean`, `Hybrid`)
- **DTO Layer**: Clean separation between API and domain
- **Optional Interfaces**: A backend serves the paradigms whose searcher interfaces it implements; the API answers the rest with 404 (GET) or 400 (`_search`)
- **Extensible**: Easy to add new query types in the future

## Development Commands

### Database
```bash
# Start PostgreSQL container
docker compose up -d

# Database runs on port 54320 with:
# - Database: news_db
# - User: news_user
# - Password: news_password
```

### Building and Running
```bash
# Build specific command
go build -o bin/datapipe ./cmd/datapipe

# Run with environment variables (stages: preprocess, embed, load)
ENV_PATHS=cmd/datapipe/pg.env go run ./cmd/datapipe load <canonical-descriptor> --engine native

# Build other commands
go build -o bin/schemagen ./cmd/schemagen
go build -o bin/news-api ./cmd/news_api

# Run tests
go test ./...

# Run tests for specific package
go test ./internal/ingest/reader

# Format code
go fmt ./...

# Vet code
go vet ./...
```

### Environment Setup
Commands expect environment variables (typically in `.env` files):

Every command loads the files named in `ENV_PATHS` (comma-separated) and then its own default file if it exists; a variable that is already set wins.

**Storage (`cmd/datapipe/*.env`, `cmd/news_api/pg.env`, `cmd/news_api/es.env`)**:
- `STORAGE_TYPE`: Storage backend the commands accept (`pg`, `es`)
- `PG_CONNECTION_STRING`: PostgreSQL connection string
- `ES_ADDRESSES`: Elasticsearch cluster addresses (comma-separated)
- `ES_INDEX_NAME`: Elasticsearch index name
- `ES_DIAL_TIMEOUT`, `ES_RESPONSE_HEADER_TIMEOUT`: Go durations bounding each Elasticsearch request, bulk flushes included (default 5s, 30s); a timed-out request is not retried

**Bench (`cmd/bench/.env`)**, loaded at startup from the repo root, or name it in `ENV_PATHS`; copy `cmd/bench/.env.example`:
- `TYPESAFE_API_KEY`: TypeSafe key for `bench judge --strategy jev`
- `PG_CONNECTION_STRING`: Postgres the judges read pooled articles and document vectors from
- `EMBEDDING_BASE_URL`: Ollama endpoint for query vectors when a track has no `trec/query-vectors/`
- `BENCH_TRACK_ROOT`: where relative track paths start
- Never `ANTHROPIC_API_KEY`: `claude -p` inherits the environment and would bill the API instead of the claude.ai login

**Search API (`cmd/news_api/.env`, loaded by default; copy `cmd/news_api/.env.example`)**:
- `PORT`: HTTP server port (default: 8080)
- `USE_HTTP2`: Enable HTTP/2 support (`true`/`false`)
- `CORS_ORIGINS`: Allowed CORS origins (comma-separated)
- `HTTP_READ_HEADER_TIMEOUT`, `HTTP_READ_TIMEOUT`, `HTTP_WRITE_TIMEOUT`, `HTTP_IDLE_TIMEOUT`: Go durations (default 5s, 30s, 120s, 120s)
- `HTTP_REQUEST_TIMEOUT`: Go duration after which a handler's context is cancelled (default 90s); must be shorter than `HTTP_WRITE_TIMEOUT`
- `HTTP_BODY_LIMIT`: Largest request body, e.g. `1M` (default); larger bodies get 413

## Testing
Tests are located alongside source files with `_test.go` suffix. Use standard Go testing patterns:
- `go test ./...` - Run all tests
- `go test -v ./internal/ingest/reader` - Run specific package tests with verbose output
- `go test -short ./...` - Skip every test that needs Docker
- Container-backed tests go through `internal/testcontainer` and share one server per engine; run them with `-p 1` so one package's containers are up at a time

**Type System Purity**:
- Type definitions should represent search concepts, not implementation details
- Storage layer translates query types to engine-specific queries
- Type validation ensures type safety before reaching storage layer