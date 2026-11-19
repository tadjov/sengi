# Tiger Data `pg_textsearch` — BM25 on Postgres Pages

BM25 full-text search extension by Tiger Data (TimescaleDB). Built on a native Postgres-page **memtable** architecture (no external engine).

## Overview

| Field | Value |
|---|---|
| Extension | `pg_textsearch` |
| Version | v1.0.0 GA — March 31, 2026 (production-ready on Tiger Cloud); v1.5.1 used here |
| Engine | Native Postgres pages + memtable (LSM-like segments) |
| Ranking model | **BM25** (Block-Max WAND for top-k) |
| Index | `USING bm25` |
| License | PostgreSQL OSS license (open source) |
| PG versions | 17, 18 |
| Deployment | Tiger Cloud (managed); self-hosted needs `shared_preload_libraries` |

## Install

```sql
-- self-hosted: shared_preload_libraries = 'pg_textsearch' then restart
CREATE EXTENSION pg_textsearch;
```

## Index Creation

```sql
CREATE INDEX products_idx ON products
USING bm25(description)
WITH (text_config = 'english');
```

- **Single text column per index** (no expressions → use generated columns).
- `text_config`: `'english'`, `'french'`, `'simple'` (no stemming), etc.

## Querying — `<@>` operator

```sql
SELECT name, description <@> 'ergonomic work' AS score
FROM products ORDER BY score LIMIT 3;
```

- **Scores are negative**; lower (more negative) = better match; `0` = no match.
- In PL/pgSQL use explicit form: `to_bm25query('query', 'index_name')`.

## Configuration GUCs

| Setting | Purpose |
|---|---|
| `pg_textsearch.memtable_spill_threshold` | Memtable size before flush |
| `pg_textsearch.bulk_load_threshold` | Bulk-load path trigger |
| `pg_textsearch.segments_per_level` | LSM merge fan-out |
| `pg_textsearch.default_limit` | Default top-k cap |
| `pg_textsearch.enable_bmw` | Block-Max WAND (default on) |
| `bm25_force_merge('idx')` | Consolidate segments after bulk load |

## Performance Claims

| Metric | Claim |
|---|---|
| Top-k queries | up to **4× faster** vs native BM25 (Block-Max WAND) |
| Index build | **4×+** faster via parallel builds |
| Index size | **41% smaller** (delta + bitpacking); +10–20% query speed (short queries) |

## Strengths

- Real BM25 with corpus-aware IDF, TF saturation, length normalization.
- Permissive OSS license; no external engine; integrates with pgvector/pgvectorscale for hybrid.
- Parallel builds, compression, efficient top-k.

## Limitations

- **Phrase and boolean only as a filter**: since 1.5 the bm25 index serves `@@` tsquery filters (`&`, `|`, `!`, `<->`), but `<@>` ranking treats every query word as OR, and filtered ranking is not one index scan yet, so it slows down as matches grow.
- An `@@` filter takes at most 64 terms ("BM25 Boolean queries support at most 64 exact term operands").
- One text expression per index: concatenate columns to search several, with no per-field weights.
- No compressed-data path; no background compaction (synchronous memtable spills).
- Implicit `<@>` fails in PL/pgSQL (use `to_bm25query`).
- Partitioned tables: per-partition stats → scores not comparable across partitions.
- Newest of the three; PG 17/18 only.
