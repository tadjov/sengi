# ParadeDB (pg_search) — BM25 Search for Postgres

Postgres extension embedding the **Tantivy** (Rust/Lucene-like) search engine for Elastic-quality BM25 search.

## Overview

| Field | Value |
|---|---|
| Extension | `pg_search` |
| Engine | Tantivy (Rust) + Apache DataFusion (analytics) via pgrx |
| Ranking model | **BM25** |
| Index | `USING bm25` (custom Tantivy index, not GIN) |
| License | AGPL-3.0 (community); enterprise available |
| PG versions | 14–17 |
| Deployment | Extension, Docker image, ParadeDB Cloud, BYOC; logical-replica of RDS/Aurora |

## Index Creation

```sql
CREATE INDEX search_idx ON articles
USING bm25 (id, title, body, category)
WITH (key_field = 'id');
```

- `key_field` **mandatory**, must be UNIQUE/PK, listed first, untokenized.
- Multi-column in one index. Indexes text, JSON, numeric, timestamp, range, boolean, arrays.

## Tokenizers (per-column cast `::pdb.<tok>`)

| Tokenizer | Use |
|---|---|
| Unicode (default) | Word boundaries + lowercase |
| `pdb.simple('stemmer=english')` | Stemming |
| `pdb.icu` | Multilingual / non-Western scripts |
| Lindera | CJK (Chinese/Japanese/Korean) |
| ngram / regex / whitespace | Specialized matching |

Token filters: stemmer, stopwords, ASCII folding, lowercase — applied at index **and** query time. Multiple tokenizers per field via aliases.

## Querying — `@@@` operator

```sql
-- simple
SELECT id, paradedb.score(id) FROM articles
WHERE id @@@ 'title:climate AND body:warming'
ORDER BY paradedb.score(id) DESC LIMIT 10;
```

| Builder | Query type |
|---|---|
| `paradedb.match` | Analyzed full-text match |
| `paradedb.phrase` | Phrase / proximity (slop) |
| `paradedb.fuzzy_term` | Typo tolerance (Levenshtein) |
| `paradedb.boolean` | must / should / must_not |
| `paradedb.range`, `paradedb.term` | Filters / exact |
| `paradedb.boost` | Per-clause weighting |

## Ranking & Analytics

- `paradedb.score(key_field)` → BM25 score.
- `ts_headline`-style highlighting via `paradedb.snippet`.
- **Aggregations / facets**: bucket + metric aggregations (DataFusion) for faceted navigation.

## Hybrid Search

Combine with `pgvector` (separate index) and fuse via **Reciprocal Rank Fusion (RRF)** in SQL.

## Strengths

- True BM25, fast top-k (block-WAND style), faceting, fuzzy, phrase — "Elasticsearch in Postgres".
- Rich query DSL; analytics via columnar engine.

## Limitations

- AGPL license (copyleft) — consideration for some deployments.
- Heavier extension (Rust runtime); index is separate structure to maintain.
- Tantivy index rebuild/maintenance cost; write amplification on heavy updates.
