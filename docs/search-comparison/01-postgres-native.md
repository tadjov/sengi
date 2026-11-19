# PostgreSQL Native Full-Text Search (tsvector + GIN)

Built-in FTS shipped with core PostgreSQL. No extension required.

## Overview

| Field | Value |
|---|---|
| Type | Core feature (every PG version) |
| Ranking model | `ts_rank` / `ts_rank_cd` (frequency + proximity, **not** BM25) |
| Index | GIN (default) or GiST on `tsvector` |
| License | PostgreSQL License |
| PG versions | All supported |

## Data Types

| Type | Purpose |
|---|---|
| `tsvector` | Document as normalized, deduped lexemes + positions |
| `tsquery` | Parsed query: lexemes + boolean/proximity operators |

## Core Functions

| Function | Use |
|---|---|
| `to_tsvector(cfg, text)` | Document → lexemes (stemming, stopwords) |
| `to_tsquery(cfg, text)` | Query with explicit operators (`&` `\|` `!` `<->`) |
| `plainto_tsquery` | Plain words, AND-combined |
| `phraseto_tsquery` | Phrase, lexemes via `<->` |
| `websearch_to_tsquery` | Google-style (`"..."`, `or`, `-`) |
| `ts_rank` / `ts_rank_cd` | Relevance score (cd = cover density / proximity) |
| `ts_headline` | Snippet highlighting |
| `setweight` | Tag lexemes A/B/C/D for field weighting |

## Query Operators (tsquery)

| Op | Meaning |
|---|---|
| `&` | AND |
| `\|` | OR |
| `!` | NOT |
| `<->` | FOLLOWED BY (adjacent) |
| `<N>` | distance N |

## Match

```sql
SELECT title, ts_rank(tsv, q) AS rank
FROM articles, websearch_to_tsquery('english','climate change') q
WHERE tsv @@ q
ORDER BY rank DESC LIMIT 10;
```

`@@` is the boolean match operator (`tsvector @@ tsquery`).

## Indexing

```sql
ALTER TABLE articles ADD COLUMN tsv tsvector
  GENERATED ALWAYS AS (to_tsvector('english', title || ' ' || body)) STORED;
CREATE INDEX articles_tsv_idx ON articles USING gin (tsv);
```

| Index | Trade-off |
|---|---|
| GIN | Fast search, slower writes, larger — **default choice** |
| GiST | Smaller, lossy, slower search, faster updates |

## Text Search Config / Dictionaries

- Parser tokenizes → dictionaries normalize (stemming via Snowball/Ispell, stopwords, synonyms, thesaurus).
- Per-language configs (`\dF`); set via `default_text_search_config`, per-DB or per-session.

## Strengths

- Zero install, fully transactional, joins/filters in same query.
- Weighting (A–D), phrase + proximity search, highlighting, multilingual configs.

## Limitations

- **No BM25** — `ts_rank` lacks IDF/corpus stats; relevance diverges from user expectation.
- Ranking recomputed per-query → top-k expensive at scale (no early termination).
- No native fuzzy/typo tolerance (needs `pg_trgm`).
- GIN index doesn't store ranking info; rank step reads heap.
