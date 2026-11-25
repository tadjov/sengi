# FUTURE_WORK.md

Roadmap for unifying and improving PostgreSQL vs Elasticsearch search results.
Items are tagged **DONE**, **IN PROGRESS**, or **NOT BUILT** to reflect actual state.

## Observed Differences (PG vs ES)

- **Date fields**: PG returns proper timestamps; some ES results have shown zero dates
  (`0001-01-01T00:00:00Z`). Likely a mapping/ingest issue — verify against current ES mapping.
- **Missing fields**: ES results have lacked `language` / some `sourceId` values.
- **Score scales differ**: PG `ts_rank` (0–1) vs ES BM25 (≈1–30+). No native cross-engine
  comparability — handled in evaluation by rank-based metrics, not raw scores.

PG tends to be more complete on metadata; ES tends to rank relevance better. The goal is to
keep both honest and comparable.

---

## Roadmap

### 1. Search comparison tooling — NOT BUILT
Proposed `cmd/search_compare/` CLI to query both backends with identical params and emit a
side-by-side diff (data discrepancies, missing fields, ranking drift). Does not exist yet.

> Note: cross-engine **relevance** comparison already exists via the bench CLI (see below).
> `search_compare` would be a lighter, ad-hoc field/result diff tool, not a replacement.

### 2. Data consistency — IN PROGRESS / investigate
- Verify identical source data reaches both backends; audit `configs/mappings/` for drift.
- Root-cause ES zero dates and missing `language`/`sourceId`; add ingest-time validation.
- Confirm timezone handling and analyzer/FTS-config parity.

### 3. Unified ranking / score normalization — NOT BUILT
Proposed `internal/ranking/` service: normalize engine scores to 0–1, optional composite
scoring (text relevance + recency + source authority) with configurable weights and an
explanation breakdown. Not implemented; engine scores are currently surfaced as-is and
compared via rank-based IR metrics.

### 4. IR evaluation framework — DONE (bench CLI / tracks/)
A TREC-style evaluation pipeline already exists:
- **Tooling**: `cmd/bench/` + `internal/bench/` — init → validate → pool → judge → run →
  export → show (also status, diff, clean). See `docs/bench.md`.
- **Metrics**: NDCG, MAP, MRR, Bpref, Precision/Recall/F1.
- **Tracks**: `tracks/` (`fts_quality`, `news_fuzzy`, `news_semantic`, `news_hybrid`), each
  with `spec.yaml`, `suite.yaml`, `trec/` (pool + judgments), and `reports/`.
- **Judgment strategies**: lexical, BM25, vector, hybrid, claude-cli/claude-api, manual.

Remaining ideas here: CI integration, automated ranking-drift alerts, historical tracking.

### 5. Production optimization — NOT BUILT (later)
Query routing by characteristics, per-engine index tuning, cache warming, A/B testing of
ranking changes, runtime quality monitoring.

### 6. DiskANN on tiger — NOT BUILT
pgvectorscale ships in the tiger image (0.9.1) but is not installed; the cc-news runs use pgvector
HNSW on every Postgres engine. A DiskANN arm on tiger would be compared at matched recall, on the
measurement host.

### 7. Query rewriting — NOT BUILT
Description and question queries name a topic in words the articles don't use: "workers paid per
task through apps without employee benefits" is the gig economy. In `experiments/embedding-width`
that query has the lowest cosines of the 30 semantic queries (its top 10 between 0.57 and 0.50) and
the least stable top 10 when the vectors are cut from 1024 to 512 dimensions (3 of 10 kept).
Rewriting the query before any engine sees it targets that gap:
- an LLM rewrites the query into the terms news writing uses, as one or a few keyword queries, for
  the lexical engines
- HyDE (Gao et al., 2022) and query2doc (Wang et al., 2023): the model writes a hypothetical article
  lead and that text is embedded, matching the documents' `lead-v1` recipe

Rewriting is query-side, so every engine gets the same rewritten query and the engine comparison
stays fair. Judging keeps the original query, since relevance is to what the user typed. The model
call adds latency, which would be reported apart from the engines' own.
---

## Technical Debt
- Engine-specific scoring logic embedded in storage implementations.
- Error-response shapes differ between backends.
- Limited cross-backend integration tests.
- Storage settings not centrally managed.

---

*Status: evaluation framework built; comparison tooling and ranking normalization still planned.*
