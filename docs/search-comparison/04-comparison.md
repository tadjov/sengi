# Comparison — PG Native FTS vs ParadeDB vs pg_textsearch

Three approaches to full-text search in PostgreSQL, for the thesis *"PostgreSQL as a Search Engine"*.

## At a Glance

| Dimension | PG Native (GIN) | ParadeDB (`pg_search`) | Tiger `pg_textsearch` |
|---|---|---|---|
| Nature | Core feature | Extension (Tantivy/Rust) | Extension (native pages) |
| Ranking | `ts_rank` (TF + proximity) | **BM25** | **BM25** |
| Index | GIN/GiST on `tsvector` | `USING bm25` (Tantivy) | `USING bm25` (memtable/LSM) |
| Match operator | `@@` | `@@@` | `<@>` |
| License | PostgreSQL | **AGPL-3.0** | PostgreSQL (OSS) |
| PG versions | All | 14–17 | 17–18 |
| Maturity | Decades, stable | Mature, widely used | v1.0 GA (Mar 2026), newest |
| GA date | — | — | 2026-03-31 |

## Capabilities

| Feature | PG Native | ParadeDB | pg_textsearch |
|---|---|---|---|
| BM25 / IDF | ❌ | ✅ | ✅ |
| Multi-column index | ✅ (concat) | ✅ | ❌ (1 text col) |
| Phrase / proximity | ✅ (`<->`) | ✅ | ❌ |
| Fuzzy / typo | ❌ (needs pg_trgm) | ✅ | ❌ |
| Boolean queries | ✅ | ✅ | partial |
| Faceting / aggregations | ❌ | ✅ (DataFusion) | ❌ |
| Highlighting / snippets | ✅ `ts_headline` | ✅ `paradedb.snippet` | ❌ |
| Field weighting (A–D) | ✅ | ✅ (boost) | ❌ |
| Stemming / stopwords | ✅ | ✅ | ✅ |
| CJK / multilingual tokenizers | configs | ✅ ICU/Lindera | `text_config` langs |
| Hybrid (vector) | manual + pgvector | pgvector + RRF | pgvector/pgvectorscale |
| Top-k early termination | ❌ | ✅ | ✅ (Block-Max WAND) |

## Operational

| Aspect | PG Native | ParadeDB | pg_textsearch |
|---|---|---|---|
| Install | none | extension + Rust runtime | `shared_preload_libraries` |
| Managed offering | any PG host | ParadeDB Cloud / BYOC | Tiger Cloud |
| Write overhead | low (GIN) | higher (Tantivy rebuild) | medium (memtable spill) |
| Index size | medium | larger | **compressed (−41%)** |
| Build speed | serial | parallel | parallel (4×+) |

## Relevance Quality (ranking realism)

`pg_textsearch ≈ ParadeDB (BM25)` > `PG Native (ts_rank)`

Both extensions implement corpus-aware BM25 (IDF, TF saturation, length normalization). Native `ts_rank` has no IDF/corpus stats → weakest relevance, but most portable.

## When to Use

| Choose | If |
|---|---|
| **PG Native** | Zero deps, transactional, modest corpus, portability matters, ranking quality secondary |
| **ParadeDB** | Need Elasticsearch-grade features: fuzzy, facets, phrase, rich DSL; AGPL acceptable |
| **pg_textsearch** | Want BM25 with permissive OSS license, large corpus top-k at scale, on PG 17/18; can live without phrase/facets today |

## Thesis Notes

- **Baseline**: PG Native = the "PostgreSQL as search engine" baseline to beat.
- **BM25 contenders**: ParadeDB (feature-rich, AGPL) vs pg_textsearch (lean, OSS, newest) — direct head-to-head on relevance + latency.
- **vs Elasticsearch**: both BM25 extensions explicitly position against ES; benchmark NDCG/MAP/MRR + p95 latency across all four engines.
- **Watch**: pg_textsearch lacks phrase/multi-field/facets at v1.0 — affects query coverage in the `fts_quality` suite.

## Sources

- [PostgreSQL FTS docs](https://www.postgresql.org/docs/17/textsearch-intro.html)
- [ParadeDB docs](https://docs.paradedb.com/) · [GitHub](https://github.com/paradedb/paradedb)
- [pg_textsearch docs](https://www.tigerdata.com/docs/deploy/tiger-cloud/tiger-cloud-aws/tiger-cloud-extensions/pg-textsearch) · [GitHub](https://github.com/timescale/pg_textsearch)
