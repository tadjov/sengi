# Search API

The routes live in `internal/api/router/search.go`. The OpenAPI spec is in `api/openapi-spec/` and is served at `/swagger/index.html`.

## Contract

- A stated parameter is honoured by the engine or the request is a 400 that names the parameter and the engine. Nothing is silently dropped: an unknown query parameter or JSON key is a 400 too.
- An omitted `operator` takes the engine's default: AND on PostgreSQL, OR on Elasticsearch.
- A boost the engine cannot apply only changes ranking, so it never fails a request. It is listed in `ignored`.
- With the header `Prefer: handling=lenient` an unsupported parameter is removed before the search runs and listed in `ignored`. The response then carries `Preference-Applied: handling=lenient`. A request whose every field is unsupported is still a 400.
- `GET /v1/capabilities` says what the running backend serves and honours.

## Endpoints

| Method and path | Serves | Routed |
|---|---|---|
| `GET /v1/articles/search` | free-text search box | when the backend serves query strings |
| `POST /v1/articles/_search` | `match`, `multi_match`, `phrase`, `boolean`, `hybrid` | always |
| `GET /v1/articles/semantic_search` | vector similarity search | when embeddings are enabled |
| `GET /v1/capabilities` | supported query types and parameters | always |
| `GET /health` | backend connectivity | always |

A paradigm the backend does not serve answers 404 on its GET route and 400 on `_search`.

### `GET /v1/articles/search`

Searches title, description and content. Operators and quotes are read as words.

| Parameter | Required | Meaning |
|---|---|---|
| `q` | yes | query text, at most 1000 characters |
| `size` | no | results per page, 1 to 10000, default 100 |
| `cursor` | no | `next_cursor` of the previous page |
| `lang` | no | ISO 639-3 code, only `eng` today, default `eng` |

```bash
curl 'http://localhost:8080/v1/articles/search?q=climate%20change&size=10'
```

### `POST /v1/articles/_search`

The body needs `Content-Type: application/json` and holds exactly one query type under `query`:

```json
{
  "size": 10,
  "cursor": "...",
  "query": { "match": { "field": "title", "query": "climate change", "operator": "and" } }
}
```

`size` and `cursor` behave as on the search box. Every query type takes an optional `language`.

| Query type | Fields |
|---|---|
| `match` | `query`, `field`, optional `operator` (`and`, `or`), optional `fuzziness` (`NONE`, `AUTO`, `0`, `1`, `2`) |
| `multi_match` | `query`, `fields` (each optionally boosted, `"title^3"`), optional `operator` |
| `phrase` | `query`, `fields`, optional `slop` (0 to 3) |
| `boolean` | `expression` with `AND`, `OR`, `NOT` and parentheses |
| `hybrid` | `query`, optional `k` (the RRF constant, default 60) |

A field is one of `title`, `description`, `content`.

```bash
curl -X POST http://localhost:8080/v1/articles/_search \
  -H 'Content-Type: application/json' \
  -d '{"query":{"multi_match":{"query":"renewable energy","fields":["title^3","content"]}}}'
```

### `GET /v1/articles/semantic_search`

Takes `q`, `size` and `cursor` as the search box does, and no other parameter. The query is embedded with the model the corpus was loaded with.

### `GET /v1/capabilities`

```json
{
  "query_types": {
    "query_string": true, "match": true, "multi_match": true, "phrase": true,
    "boolean": true, "semantic": false, "hybrid": false
  },
  "full_text": {
    "engine": "postgres", "default_operator": "AND", "fuzziness": false,
    "phrase_slop": true, "boosts": true, "fields": ["title", "description", "content"]
  }
}
```

The `full_text` values above are an example. Each engine reports its own.

## Responses

The search box and `_search` return:

```json
{
  "hits": [
    { "article": { "id": "...", "url": "...", "title": "...", "content": "...", "language": "eng" }, "score": 0.42, "score_normalized": 1 }
  ],
  "next_cursor": "...",
  "has_more": true,
  "max_score": 0.42,
  "page_max_score": 0.42,
  "total_matches": 1523,
  "effective": { "operator": "AND", "fields": ["title", "description", "content"], "language": "eng" },
  "ignored": []
}
```

`effective` is the contract the search ran under, with every omitted parameter filled in by the engine. `ignored` lists the stated parameters the engine did not honour, each with `param`, `engine` and `reason`. To page, send `next_cursor` back as `cursor` while `has_more` is true.

Semantic search returns `hits` as bare articles with `next_cursor` and `has_more`, and no scores.

## Status codes

| Status | When | Body |
|---|---|---|
| 200 | the search ran | see above |
| 400 | invalid, unknown or unsupported parameter, or a query type the backend does not serve | `{"error": "...", "title": "validation error"}` |
| 404 | a GET route the backend does not serve | `{"error": "Not Found"}` |
| 413 | body over `HTTP_BODY_LIMIT` | `{"error": "..."}` |
| 415 | `_search` without `Content-Type: application/json` | `{"error": "..."}` |
| 500 | the backend failed | `{"error": "internal server error"}` |

`GET /health` answers 200 `{"status": "healthy"}` or 503 `{"status": "unhealthy"}`.
