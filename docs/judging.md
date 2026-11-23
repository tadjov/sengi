# Judging

`bench judge <track> --strategy <S>` grades every `(query, doc)` pair of the pool into `trec/annotations.<set>.yaml`. Flags, resume, carry-over and the LLM judge's request details are in [bench.md](bench.md). This page says how each judge decides a grade.

## Scale

| Grade | Meaning |
|------:|---------|
| 3 | Highly relevant: centrally about the topic |
| 2 | Relevant: clearly covers the topic |
| 1 | Marginal: mentioned, not the focus |
| 0 | Not relevant |
| -1 | Unjudged: a placeholder a judge writes when it could not grade |

NDCG uses the grade itself. Precision, recall, MAP and MRR count a doc as relevant at `spec.metrics.relevance_threshold` and above.

## Judges

| `--strategy` | Set name | Grades | Needs |
|---|---|---|---|
| `llm` | `claude-cli` or `claude-api` (the `--provider`) | one doc per request | `claude` login, or an API key |
| `jev` | `jev` | one doc per request | `TYPESAFE_API_KEY` |
| `keyword` | `keyword` | one doc at a time | nothing |
| `bm25` | `bm25` | the query's whole pool | nothing |
| `vector` | `vector` | the query's whole pool | Postgres, query vectors |
| `hybrid` | `hybrid` | the query's whole pool | Postgres, query vectors |
| `manual` | `manual` | writes `-1` placeholders to fill by hand | nothing |

The set name is what `--judgments`, `spec.defaults.judgments` and the annotations filename use.

The LLM judge is the primary judge on every cc-news track. The others are cross-checks: compare them with `bench diff judgments`.

## The gate

`llm`, `jev`, `keyword` and `bm25` share one gate, decided in code before any grading. A doc that breaks the query's phrase, its boolean expression or a declared filter grades 0. `vector` and `hybrid` apply no gate.

Every judge grades the query's `text`. A query whose text leaves no usable terms fails for the term judges and leaves its docs unjudged.

## How each judge grades

**`llm`** sends the query text and the article (title and description in full, content up to 1,744 characters) with one rubric: grade what the article is about, pick the lower grade when in doubt, a passing mention is 1 at most. The default model is `claude-sonnet-5-5` at effort `low`. A reply without a usable grade leaves the doc unjudged.

**`jev`** asks one question with the four grade levels as criteria and takes the most probable level, the lower one on a tie. The model is pinned (`jev-1.14.0` by default) and a reply from another model is an error.

**`keyword`** counts how many query terms the doc holds:

| Condition | Grade |
|---|---|
| half of the terms or more are in the title | 3 |
| half or more are anywhere in title, description or content | 2 |
| a quarter or more are anywhere | 1 |
| otherwise | 0 |

**`bm25`** scores each doc with Okapi BM25 over the query's pool and divides by the score's upper bound for that query:

| Normalised score | Grade | Roughly |
|---|---|---|
| 0.50 and above | 3 | every term twice |
| 0.30 and above | 2 | every term once |
| 0.15 and above | 1 | half of the terms once |
| below | 0 | |

**`vector`** grades the pool's 10 docs nearest the query by cosine 3 and the rest 0. On the semantic track this measures recall@10 against exact search, not topical relevance.

**`hybrid`** adds half the doc's BM25 score, divided by the pool's best, to half its cosine (a negative cosine counts as 0):

| Combined | Grade |
|---|---|
| 0.66 and above | 3 |
| 0.40 and above | 2 |
| 0.15 and above | 1 |
| below | 0 |

A doc with no stored vector stays unjudged.

## The judge block

Every annotations file records who graded it, and `--resume` refuses a file whose block differs from the judge being run:

```yaml
meta:
  judge:
    strategy: llm
    provider: claude-cli
    model: claude-sonnet-5-5
    prompt_version: v3
    effort: low
    max_output_tokens: 16000
    gate_version: v1
```

The deterministic judges record a `rubric_version`, bumped when their thresholds change, and the gated ones the `gate_version`. Bump `PromptVersion` (`internal/bench/judgment/prompt.go`) whenever the rubric or the prompt changes in a way that can move a grade; a golden test pins the current prompt.

## Circularity

A term judge rewards the docs a term engine retrieves. Never use `keyword` or `bm25` as the primary judge of a semantic or hybrid track.
