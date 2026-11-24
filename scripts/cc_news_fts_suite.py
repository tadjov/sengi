import json
from pathlib import Path

OUT = Path(__file__).resolve().parent.parent / "tracks/cc-news/fts/suite.yaml"
FIELDS = ["title", "description", "content"]

# (id, category, description, terms or boolean spec)
KEYWORD = [
    ("kw-net-neutrality", "Articles about net neutrality", "net neutrality"),
    ("kw-bitcoin-price", "Articles about the price of bitcoin", "bitcoin price"),
    ("kw-climate-change", "Articles about climate change", "climate change"),
    ("kw-opioid-epidemic", "Articles about the opioid epidemic", "opioid epidemic"),
    ("kw-equifax-breach", "Articles about the Equifax data breach", "equifax breach"),
    ("kw-north-korea-missile", "Articles about North Korea's missile programme and tests", "north korea missile"),
    ("kw-cambridge-analytica", "Articles about Cambridge Analytica", "cambridge analytica"),
    ("kw-hurricane-harvey", "Articles about Hurricane Harvey", "hurricane harvey"),
    ("kw-grenfell-tower", "Articles about the Grenfell Tower fire", "grenfell tower"),
    ("kw-fidget-spinner", "Articles about fidget spinners", "fidget spinner"),
]
PHRASE = [
    ("ph-travel-ban", "Articles about the US travel ban", "travel ban"),
    ("ph-paris-climate-agreement", "Articles about the Paris climate agreement", "paris climate agreement"),
    ("ph-supreme-court-nominee", "Articles about a Supreme Court nominee", "supreme court nominee"),
    ("ph-las-vegas-shooting", "Articles about the Las Vegas shooting", "las vegas shooting"),
    ("ph-universal-basic-income", "Articles about universal basic income", "universal basic income"),
]
# must: groups of alternatives, all groups required; must_not: excluded terms.
BOOLEAN = [
    ("bool-tesla-musk-or-model", "Articles about Tesla that mention Musk or a Tesla model",
     {"must": [["tesla"], ["musk", "model"]], "must_not": []}),
    ("bool-facebook-privacy-not-instagram", "Articles about Facebook and privacy that do not mention Instagram",
     {"must": [["facebook"], ["privacy"]], "must_not": ["instagram"]}),
    ("bool-storm-puerto-rico", "Articles about a hurricane or storm in Puerto Rico",
     {"must": [["hurricane", "storm"], ["puerto"], ["rico"]], "must_not": []}),
    ("bool-uber-lyft-driver-not-taxi", "Articles about Uber or Lyft drivers that do not mention taxis",
     {"must": [["uber", "lyft"], ["driver"]], "must_not": ["taxi"]}),
    ("bool-flu-vaccine", "Articles about flu or influenza vaccines",
     {"must": [["flu", "influenza"], ["vaccine"]], "must_not": []}),
]
MULTI_FIELD = [
    ("mf-solar-power-cost", "Articles about the cost of solar power", "solar power cost"),
    ("mf-ai-jobs", "Articles about artificial intelligence and jobs", "artificial intelligence jobs"),
    ("mf-student-loan-debt", "Articles about student loan debt", "student loan debt"),
    ("mf-minimum-wage-increase", "Articles about minimum wage increases", "minimum wage increase"),
    ("mf-iran-nuclear-deal", "Articles about the Iran nuclear deal", "iran nuclear deal"),
    ("mf-housing-market-prices", "Articles about housing market prices", "housing market prices"),
]
TITLE = [
    ("title-self-driving", "Articles whose title is about self-driving vehicles", "self-driving"),
    ("title-premier-league", "Articles whose title is about the Premier League", "premier league"),
    ("title-super-bowl", "Articles whose title is about the Super Bowl", "super bowl"),
    ("title-wildfire", "Articles whose title is about a wildfire", "wildfire"),
]

TEMPLATES = """templates:
  # `, id` breaks ts_rank ties: exact-content copies score the same, and without it
  # the index and seq scans return tied rows in different orders.
  - id: pg_multi_field
    args: [terms, rank_norm, limit]
    query: |
      SELECT id FROM articles
      WHERE search_vector @@ plainto_tsquery('english', $1)
      ORDER BY ts_rank(search_vector, plainto_tsquery('english', $1), $2::int) DESC, id
      LIMIT $3::int

  - id: pg_phrase
    args: [terms, rank_norm, limit]
    query: |
      SELECT id FROM articles
      WHERE search_vector @@ phraseto_tsquery('english', $1)
      ORDER BY ts_rank(search_vector, phraseto_tsquery('english', $1), $2::int) DESC, id
      LIMIT $3::int

  - id: pg_boolean
    args: [tsquery_expr, rank_norm, limit]
    query: |
      SELECT id FROM articles
      WHERE search_vector @@ to_tsquery('english', $1)
      ORDER BY ts_rank(search_vector, to_tsquery('english', $1), $2::int) DESC, id
      LIMIT $3::int

  # Title lexemes carry weight A in search_vector; the outer match keeps the GIN index in play.
  - id: pg_title
    args: [terms, rank_norm, limit]
    query: |
      SELECT id FROM articles
      WHERE search_vector @@ plainto_tsquery('english', $1)
        AND ts_filter(search_vector, '{a}') @@ plainto_tsquery('english', $1)
      ORDER BY ts_rank(ts_filter(search_vector, '{a}'), plainto_tsquery('english', $1), $2::int) DESC, id
      LIMIT $3::int

  - id: pdb_multi_field_boosted
    args: [terms, limit]
    query: |
      SELECT id FROM articles
      WHERE id @@@ paradedb.boolean(
        should => ARRAY[
          paradedb.boost(3.0, paradedb.match('title', $1)),
          paradedb.boost(2.0, paradedb.match('description', $1)),
          paradedb.match('content', $1)
        ]
      )
      ORDER BY pdb.score(id) DESC, id
      LIMIT $2::int

  - id: pdb_phrase_boosted
    args: [terms, limit]
    query: |
      SELECT id FROM articles
      WHERE id @@@ paradedb.boolean(
        should => ARRAY[
          paradedb.boost(3.0, paradedb.parse('title:"' || $1 || '"')),
          paradedb.boost(2.0, paradedb.parse('description:"' || $1 || '"')),
          paradedb.parse('content:"' || $1 || '"')
        ]
      )
      ORDER BY pdb.score(id) DESC, id
      LIMIT $2::int

  - id: pdb_title
    args: [terms, limit]
    query: |
      SELECT id FROM articles
      WHERE id @@@ paradedb.match('title', $1)
      ORDER BY pdb.score(id) DESC, id
      LIMIT $2::int

  # pg_textsearch serves @@ from its bm25 index only when nothing orders by <@> in the
  # same scan, so the CTE filters through the index and BM25 then ranks the matches.
  # Tiger filters with the same tsquery as pg-gin, so the two differ only in the ranker.
  # A bound parameter needs the index named explicitly.
  - id: tiger_multi_field
    args: [terms, limit]
    query: |
      WITH matched AS MATERIALIZED (
        SELECT id, search_text FROM articles WHERE search_text @@ plainto_tsquery('english', $1)
      )
      SELECT id FROM matched
      ORDER BY search_text <@> to_bm25query($1, 'idx_articles_bm25'), id
      LIMIT $2::int

  - id: tiger_phrase
    args: [terms, limit]
    query: |
      WITH matched AS MATERIALIZED (
        SELECT id, search_text FROM articles WHERE search_text @@ phraseto_tsquery('english', $1)
      )
      SELECT id FROM matched
      ORDER BY search_text <@> to_bm25query($1, 'idx_articles_bm25'), id
      LIMIT $2::int

  # The BM25 query takes plain words, so it ranks by the expression's positive terms.
  - id: tiger_boolean
    args: [tsquery_expr, terms, limit]
    query: |
      WITH matched AS MATERIALIZED (
        SELECT id, search_text FROM articles WHERE search_text @@ to_tsquery('english', $1)
      )
      SELECT id FROM matched
      ORDER BY search_text <@> to_bm25query($2, 'idx_articles_bm25'), id
      LIMIT $3::int

  - id: tiger_title
    args: [terms, limit]
    query: |
      WITH matched AS MATERIALIZED (
        SELECT id, title FROM articles WHERE title @@ plainto_tsquery('english', $1)
      )
      SELECT id FROM matched
      ORDER BY title <@> to_bm25query($1, 'idx_articles_title_bm25'), id
      LIMIT $2::int

  - id: es_multi_match_boosted
    args: [terms]
    query: |
      {"query": {"multi_match": {"query": "{{terms}}", "fields": ["title^3", "description^2", "content"]}}, "size": 100, "sort": ["_score", {"id": "asc"}], "_source": false, "track_total_hits": false}

  - id: es_phrase_boosted
    args: [terms]
    query: |
      {"query": {"multi_match": {"query": "{{terms}}", "type": "phrase", "fields": ["title^3", "description^2", "content"]}}, "size": 100, "sort": ["_score", {"id": "asc"}], "_source": false, "track_total_hits": false}

  - id: es_title
    args: [terms]
    query: |
      {"query": {"match": {"title": "{{terms}}"}}, "size": 100, "sort": ["_score", {"id": "asc"}], "_source": false, "track_total_hits": false}
"""

HEADER = """schema_version: 1
id: cc_news_fts_v1
name: "cc-news FTS v1"
description: "Full-text search over cc-news: 30 queries across keyword, phrase, boolean, multi-field and single-field search"
version: "1.1.0"

corpus:
  name: cc-news-canonical@v1

"""


def block(qid, category, text, description, engines):
    lines = [f"  - id: {qid}", f"    text: {json.dumps(text)}", f"    description: {json.dumps(description)}",
             f"    category: {category}", "    engines:"]
    for name, body in engines:
        if isinstance(body, tuple):
            template, params = body
            rendered = ", ".join(f"{k}: {json.dumps(v)}" for k, v in params.items())
            lines += [f"      {name}:", f"        template: {template}", f"        params: {{ {rendered} }}"]
        else:
            lines.append(f"      {name}: |")
            lines += [f"        {line}" for line in body.splitlines()]
    return "\n".join(lines)


def standard(qid, category, description, terms, pg, pdb, tiger, es):
    return block(qid, category, terms, description, [
        ("pg-gin", (pg, {"terms": terms, "limit": 100})),
        ("paradedb", (pdb, {"terms": terms, "limit": 100})),
        ("tiger", (tiger, {"terms": terms, "limit": 100})),
        ("elasticsearch", (es, {"terms": terms})),
    ])


def boolean_text(spec):
    groups = [f"({' OR '.join(g)})" if len(g) > 1 else g[0] for g in spec["must"]]
    return " AND ".join(groups + [f"NOT {t}" for t in spec["must_not"]])


def tsquery(spec):
    groups = [f"({' | '.join(g)})" if len(g) > 1 else g[0] for g in spec["must"]]
    return " & ".join(groups + [f"!{t}" for t in spec["must_not"]])


def pdb_any_field(term):
    return "paradedb.boolean(should => ARRAY[" + ", ".join(f"paradedb.match('{f}', '{term}')" for f in FIELDS) + "])"


def pdb_boolean(spec):
    must = []
    for group in spec["must"]:
        if len(group) == 1:
            must.append(pdb_any_field(group[0]))
        else:
            must.append("paradedb.boolean(should => ARRAY[" + ", ".join(pdb_any_field(t) for t in group) + "])")
    parts = ["  must => ARRAY[\n    " + ",\n    ".join(must) + "\n  ]"]
    if spec["must_not"]:
        parts.append("  must_not => ARRAY[\n    " + ",\n    ".join(pdb_any_field(t) for t in spec["must_not"]) + "\n  ]")
    return ("SELECT id FROM articles\nWHERE id @@@ paradedb.boolean(\n" + ",\n".join(parts) + "\n)\n"
            "ORDER BY pdb.score(id) DESC, id\nLIMIT 100")


def es_boolean(spec):
    def mm(term):
        return {"multi_match": {"query": term, "fields": FIELDS}}
    must = [mm(g[0]) if len(g) == 1 else {"bool": {"should": [mm(t) for t in g], "minimum_should_match": 1}} for g in spec["must"]]
    query = {"bool": {"must": must}}
    if spec["must_not"]:
        query["bool"]["must_not"] = [mm(t) for t in spec["must_not"]]
    return json.dumps({"query": query, "size": 100, "sort": ["_score", {"id": "asc"}], "_source": False, "track_total_hits": False})


def boolean(qid, description, spec):
    positive = " ".join(t for g in spec["must"] for t in g)
    return block(qid, "boolean", boolean_text(spec), description, [
        ("pg-gin", ("pg_boolean", {"tsquery_expr": tsquery(spec), "limit": 100})),
        ("paradedb", pdb_boolean(spec)),
        ("tiger", ("tiger_boolean", {"tsquery_expr": tsquery(spec), "terms": positive, "limit": 100})),
        ("elasticsearch", es_boolean(spec)),
    ])


blocks = []
blocks += [standard(q, "keyword", d, t, "pg_multi_field", "pdb_multi_field_boosted", "tiger_multi_field", "es_multi_match_boosted") for q, d, t in KEYWORD]
blocks += [standard(q, "phrase", d, t, "pg_phrase", "pdb_phrase_boosted", "tiger_phrase", "es_phrase_boosted") for q, d, t in PHRASE]
blocks += [boolean(q, d, s) for q, d, s in BOOLEAN]
blocks += [standard(q, "multi-field", d, t, "pg_multi_field", "pdb_multi_field_boosted", "tiger_multi_field", "es_multi_match_boosted") for q, d, t in MULTI_FIELD]
blocks += [standard(q, "single-field", d, t, "pg_title", "pdb_title", "tiger_title", "es_title") for q, d, t in TITLE]

with open(OUT, "w") as out:
    out.write(HEADER + TEMPLATES + "\nqueries:\n" + "\n\n".join(blocks) + "\n")
print(len(blocks), "queries written to", OUT)
