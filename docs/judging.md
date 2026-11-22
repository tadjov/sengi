# Judging Strategies

`bench judge` grades every `(query, doc)` pair in the pool, producing
`trec/annotations.<strategy>.yaml`. Each grade is on a **4-point graded-relevance
scale** (TREC-style):

| Grade | Constant | Meaning |
|------:|----------|---------|
| `3` | `GradeHighly` | Highly relevant — centrally about the topic |
| `2` | `GradeRelevant` | Relevant — clearly covers the topic |
| `1` | `GradeMarginally` | Marginal — mentioned but not the focus |
| `0` | `GradeNotRelev` | Not relevant |
| `-1` | `GradeUnjudged` | Not yet judged (placeholder; excluded from metrics) |

The `relevance_threshold` in `spec.metrics` decides which grades count as
"relevant" for binary metrics (Precision/Recall/MAP/MRR); NDCG uses the full
graded scale.

A strategy is selected with `--strategy <name>`. Multiple strategies coexist
side by side in one track; `bench run --judgments <name>` picks which set scores
a given run.

---

## Strategy taxonomy

| Strategy | Provider | Set name | Class | Network | Determinism |
|----------|----------|----------|-------|---------|-------------|
| `lexical` | — | `lexical` | Heuristic | none | fully deterministic |
| `bm25` | — | `bm25` | Heuristic | none | fully deterministic |
| `vector` | — | `vector` | Heuristic | PG + Ollama | deterministic given fixed vectors |
| `hybrid` | — | `hybrid` | Heuristic | PG + Ollama | deterministic given fixed vectors |
| `llm` | `claude-cli` | `claude-cli` | LLM | local subprocess | non-deterministic |
| `llm` | `claude-api` | `claude-api` | LLM | HTTPS to Anthropic | non-deterministic |
| `manual` | — | `manual` | Human | none | n/a (placeholders) |

**Writing a judge is explicit; reading one is by name.**

```bash
bench judge tracks/global-news-dataset/fts_quality --strategy lexical
bench judge tracks/global-news-dataset/fts_quality --strategy llm --provider claude-cli
bench judge tracks/global-news-dataset/fts_quality --strategy llm --provider claude-api --model sonnet
```

`--strategy` names *how* to grade, `--provider` names *which endpoint* the `llm`
strategy talks to, `--model` names the model. Everything else — `--judgments`,
`spec.defaults.judgments`, `bench export --strategy`, the
`annotations.<name>.yaml` filename — refers to a judgment set by its **set
name**: the strategy for heuristic judges, the provider for LLM ones. A single
`llm` name would collide the moment two providers grade the same track.

**One LLM strategy, many endpoints.** `LLMStrategy` owns the rubric and
response parsing; an `LLMProvider` owns only "send this prompt, return the
text". Registering a provider in `provider.go` is
all it takes to add Codex or ChatGPT — the grading behaviour is unchanged, so
qrels stay comparable across vendors.

## Models

`--model` takes a full model id or a shorthand:

| Alias | Resolves to |
|-------|-------------|
| `haiku` | `claude-haiku-4-5-20251001` **(default)** |
| `sonnet` | `claude-sonnet-5` |
| `opus` | `claude-opus-5` |
| `fable` | `claude-fable-5` |

Haiku is the default for every provider: grading is a short, well-specified
classification task, so the cheapest capable model is the right baseline, and
one shared default keeps CLI- and API-graded qrels comparable.

Aliases are expanded at construction, so the judge block always records a
concrete id — an alias would silently re-point at a different model later.

## The judge block

Every annotations file attests who graded it:

```yaml
meta:
  judge:
    strategy: llm
    provider: claude-cli
    model: claude-haiku-4-5-20251001
    prompt_version: v1
```

Heuristic judges carry the same block with fewer fields (`strategy: lexical`).
`bench judge --resume` compares the whole block — strategy, provider, model and
prompt version — and refuses to append when any of them differs, since mixing
graders in one qrel set corrupts it.

Annotations without a judge block name no grader, so `--resume` and
`--carry-over` refuse them — re-grade instead.

---

## `llm` — LLM-as-judge

**Files:** `internal/bench/judgment/llm.go` + `provider_claude.go`

- **Grading prompt** (`prompt.go`): grade on the 0–3 scale, "grade what the
  article is ABOUT, not whether the query terms appear," and "when in doubt
  between two grades, pick the lower one." Returns strict JSON
  `{"doc_id","grade"}`.
- One doc per request; a reply without a usable grade leaves the doc unjudged.

### provider `claude-cli`

Runs `claude --model <id> -p <prompt>` as a subprocess; the system prompt is
prepended to the payload because `claude -p` takes a single positional message.
`--cli-binary` overrides the executable path.

The model is pinned on every invocation rather than inherited from the CLI's
ambient config, so the judge block names the model that actually graded.

**When to use:** the cheap path — it bills through an existing Claude
subscription rather than per-token API credit. This is the default provider.

### provider `claude-api`

Calls `POST /v1/messages` directly. Auth via `ANTHROPIC_API_KEY` or `--api-key`;
`--api-base` overrides the endpoint.

**When to use:** higher throughput (no subprocess churn), or CI
where no interactive `claude` login exists.

Both providers send identical prompts, so grades stay comparable across them.

---

## `lexical` — token-overlap baseline

**File:** `internal/bench/judgment/lexical.go`

A deterministic, zero-cost reference judge. It measures **query-term coverage**,
not term frequency, not semantics.

**Algorithm:**
1. Tokenize the query *description*: lowercase, split on non-alphanumeric, drop
   tokens < 3 chars, drop stopwords (`the`, `and`, … plus domain noise: `news`,
   `article`, `search`, `query`). Result = set of distinct query terms.
2. Tokenize the doc's title, description, and content into presence sets
   (`map[string]bool` — **presence only, occurrences are not counted**).
3. For each query term, count one hit: a **title** hit if present in the title,
   else an **other** hit if present in description or content.
4. Map ratios to a grade:

   | Condition | Grade |
   |-----------|-------|
   | `titleHits/total ≥ 0.5` | `3` Highly |
   | `(titleHits+otherHits)/total ≥ 0.5` | `2` Relevant |
   | overall ratio `≥ 0.25` | `1` Marginal |
   | otherwise | `0` Not relevant |

Title matches dominate: ≥50% of query terms in the **title alone** earns a 3.

**Errors** if the query description tokenizes to nothing (suggests
`--strategy manual`).

**What it is NOT:** no TF, no IDF, no length normalization, no embeddings. In IR
terms it is a binary set-overlap / query-term-recall measure, not TF-IDF or
BM25.

**When to use:** a reproducible weak baseline and a smoke test for the pipeline.

**Caveats (important for the thesis):**
- It grades the query **description string**, so verbose descriptions ("Simple
  keyword search for climate change articles") inject noise words that dilute
  the ratio and can *under-grade* good docs. Terser, topical descriptions give
  better signal.
- **Circularity:** judging lexical retrievers (PG `tsvector`, BM25/ParadeDB)
  with a lexical judge inflates their scores and penalizes semantic/vector
  engines that retrieve relevant-but-lexically-different docs. **Do not** use
  `lexical` as the primary judge for `news_semantic` / `news_hybrid`.

---

## `manual` — human grading placeholders

**File:** `internal/bench/judgment/manual.go`

Emits `grade: -1` (`GradeUnjudged`) for every doc, producing a template a human
fills in by hand. Needs no PG connection or network. Pairs with `bench show
judgments` to review what's still unjudged. See the `bench-annotate` skill for a
guided human-grading workflow.

---

## `bm25` / `vector` / `hybrid` — reserved

Names reserved in the taxonomy; not implemented. Intended future algorithmic
judges:

- **`bm25`** — BM25 score (TF + IDF + length normalization) from PG/ES, threshold
  → grade. The natural rung above `lexical`.
- **`vector`** — cosine similarity vs the query embedding, threshold → grade.
- **`hybrid`** — weighted combination of `lexical`/`bm25`/`vector`.
- **`codex-cli` / `chatgpt-api`** — register a `CLIProvider` / `APIProvider` in
  `provider.go`; the selectors and strategies come for free.

---

## Prompt versioning

The LLM grading rubric is versioned by `PromptVersion` (`prompt.go`, currently
`v1`). It is stamped into the judge block as `prompt_version`.
Bump it whenever the system prompt or grade scale changes in a way that could
shift grades. `bench judge --resume` refuses to mix grades across prompt
versions or models (an interrupted run with an empty meta block is exempt — it
is a valid resume target).

## Choosing a strategy

| Goal | Flags |
|------|-------|
| Smoke-test the pipeline, zero cost, reproducible | `--strategy lexical` |
| Cheap LLM judging via your Claude subscription | `--strategy llm --provider claude-cli` |
| Higher-throughput LLM judging, or CI with no interactive login | `--strategy llm --provider claude-api` |
| A stronger judge for conceptual queries | add `--model sonnet` |
| Authoritative human ground truth | `--strategy manual` (then hand-grade) |

For semantic/hybrid tracks, lexical is unsuitable as the primary judge — use an
LLM or human judge, and consider reporting inter-judge agreement (e.g. Cohen's
κ) against `lexical` to quantify the vocabulary-mismatch effect.
