import json
from pathlib import Path

OUT = Path(__file__).resolve().parent.parent / "tracks/cc-news/fuzzy/suite.yaml"
FIELDS = [("title", 3.0), ("description", 2.0), ("content", None)]

# (id, description, as typed, spelled correctly)
TYPO = [
    ("typo-hurricane-harvey", "Articles about Hurricane Harvey", "hurricane harvy", "hurricane harvey"),
    ("typo-brexit-negotiations", "Articles about the Brexit negotiations", "brexit negociations", "brexit negotiations"),
    ("typo-facebook-privacy", "Articles about Facebook and privacy", "facebok privacy", "facebook privacy"),
    ("typo-flu-vaccine", "Articles about the flu vaccine", "flu vacine", "flu vaccine"),
    ("typo-immigration-policy", "Articles about immigration policy", "imigration policy", "immigration policy"),
    ("typo-iran-nuclear-deal", "Articles about the Iran nuclear deal", "iran nuclar deal", "iran nuclear deal"),
    ("typo-opioid-crisis", "Articles about the opioid crisis", "opiod crisis", "opioid crisis"),
    ("typo-tesla-autopilot", "Articles about Tesla's Autopilot", "tesla autopilt", "tesla autopilot"),
    ("typo-global-warming", "Articles about global warming", "global warmng", "global warming"),
    ("typo-syrian-refugees", "Articles about Syrian refugees", "syrian refugess", "syrian refugees"),
]
HARD_TYPO = [
    ("hard-government-shutdown", "Articles about a US government shutdown", "goverment shutdwon", "government shutdown"),
    ("hard-bitcoin-price", "Articles about the price of bitcoin", "bitcion pirce", "bitcoin price"),
    ("hard-presidential-election", "Articles about a presidential election", "presidental eletcion", "presidential election"),
    ("hard-harassment-allegations", "Articles about sexual harassment allegations", "sexual harasment alegations", "sexual harassment allegations"),
    ("hard-artificial-intelligence", "Articles about artificial intelligence", "artifical inteligence", "artificial intelligence"),
    ("hard-california-evacuation", "Articles about wildfire evacuations in California", "wildfire evacuaton califronia", "wildfire evacuation california"),
    ("hard-cryptocurrency-exchange", "Articles about cryptocurrency exchanges", "cryptocurency exchnage", "cryptocurrency exchange"),
    ("hard-chemical-weapons", "Articles about chemical weapons in Syria", "chemcial wepons syria", "chemical weapons syria"),
    ("hard-renewable-energy", "Articles about renewable energy", "renewble enrgy", "renewable energy"),
    ("hard-supreme-court-nominee", "Articles about a Supreme Court nominee", "suprme cuort nominee", "supreme court nominee"),
]
KEYWORD = [
    ("kw-solar-eclipse", "Articles about the solar eclipse", "solar eclipse"),
    ("kw-pyeongchang-olympics", "Articles about the Pyeongchang Olympics", "pyeongchang olympics"),
    ("kw-stephen-hawking", "Articles about Stephen Hawking", "stephen hawking"),
    ("kw-puerto-rico-power", "Articles about Puerto Rico's power outage", "puerto rico power outage"),
    ("kw-anthem-protests", "Articles about NFL anthem protests", "nfl anthem protests"),
    ("kw-amazon-whole-foods", "Articles about Amazon and Whole Foods", "amazon whole foods"),
    ("kw-paradise-papers", "Articles about the Paradise Papers", "paradise papers"),
    ("kw-disney-fox", "Articles about the Disney and Fox deal", "disney fox deal"),
    ("kw-starbucks-arrests", "Articles about the arrests at a Starbucks", "starbucks arrests"),
    ("kw-uber-self-driving", "Articles about the Uber self-driving crash", "uber self driving crash"),
]

# The edit budget is Elasticsearch's fuzziness AUTO for every engine: no edit for a
# word of 1 to 2 letters, one for 3 to 5, two above. pg-fuzzy and tiger-fuzzy expand
# each query word to at most 50 corpus words within its budget, as Elasticsearch's
# max_expansions does, and OR them; the words are then ANDed, Postgres's default.
TEMPLATES = """templates:
  - id: pg_multi_field
    args: [terms, rank_norm, limit]
    query: |
      SELECT id FROM articles
      WHERE search_vector @@ plainto_tsquery('english', $1)
      ORDER BY ts_rank(search_vector, plainto_tsquery('english', $1), $2::int) DESC, id
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
      ORDER BY pdb.score(id) DESC
      LIMIT $2::int

  - id: tiger_multi_field
    args: [terms, limit]
    query: |
      WITH matched AS MATERIALIZED (
        SELECT id, search_text FROM articles WHERE search_text @@ plainto_tsquery('english', $1)
      )
      SELECT id FROM matched
      ORDER BY search_text <@> to_bm25query($1, 'idx_articles_bm25'), id
      LIMIT $2::int

  - id: es_multi_match_boosted
    args: [terms]
    query: |
      {"query": {"multi_match": {"query": "{{terms}}", "fields": ["title^3", "description^2", "content"]}}, "size": 100, "_source": false, "track_total_hits": false}

  - id: es_fuzzy
    args: [terms]
    query: |
      {"query": {"multi_match": {"query": "{{terms}}", "fields": ["title^3", "description^2", "content"], "fuzziness": "AUTO"}}, "size": 100, "_source": false, "track_total_hits": false}

  # Trigram similarity of the query to the title, at pg_trgm's default threshold:
  # the approach most people reach for first.
  - id: pg_trgm_title
    args: [terms, limit]
    query: |
      SELECT id FROM articles
      WHERE $1 <% title
      ORDER BY word_similarity($1, title) DESC, id
      LIMIT $2::int

  - id: pg_fuzzy
    args: [terms, rank_norm, limit]
    query: |
      WITH query_words AS (
        SELECT typed, ord,
               CASE WHEN length(typed) <= 2 THEN 0 WHEN length(typed) <= 5 THEN 1 ELSE 2 END AS edits
        FROM regexp_split_to_table(lower($1), '[^a-z]+') WITH ORDINALITY AS t(typed, ord)
        WHERE typed <> ''
      ),
      expansions AS (
        SELECT q.ord, string_agg(near.word, ' | ') AS alternatives
        FROM query_words q
        CROSS JOIN LATERAL (
          SELECT w.word FROM corpus_words w
          WHERE w.word % q.typed AND levenshtein_less_equal(w.word, q.typed, q.edits) <= q.edits
          ORDER BY levenshtein(w.word, q.typed), w.ndoc DESC, w.word
          LIMIT 50
        ) near
        GROUP BY q.ord
      ),
      fuzzy AS (
        SELECT to_tsquery('english', string_agg('(' || alternatives || ')', ' & ' ORDER BY ord)) AS q
        FROM expansions
      )
      SELECT a.id FROM articles a, fuzzy
      WHERE a.search_vector @@ fuzzy.q
      ORDER BY ts_rank(a.search_vector, fuzzy.q, $2::int) DESC, a.id
      LIMIT $3::int

  - id: tiger_fuzzy
    args: [terms, limit]
    query: |
      WITH query_words AS (
        SELECT typed, ord,
               CASE WHEN length(typed) <= 2 THEN 0 WHEN length(typed) <= 5 THEN 1 ELSE 2 END AS edits
        FROM regexp_split_to_table(lower($1), '[^a-z]+') WITH ORDINALITY AS t(typed, ord)
        WHERE typed <> ''
      ),
      expansions AS (
        SELECT q.ord, string_agg(near.word, ' | ') AS alternatives
        FROM query_words q
        CROSS JOIN LATERAL (
          SELECT w.word FROM corpus_words w
          WHERE w.word % q.typed AND levenshtein_less_equal(w.word, q.typed, q.edits) <= q.edits
          ORDER BY levenshtein(w.word, q.typed), w.ndoc DESC, w.word
          LIMIT 50
        ) near
        GROUP BY q.ord
      ),
      fuzzy AS (
        SELECT to_tsquery('english', string_agg('(' || alternatives || ')', ' & ' ORDER BY ord)) AS q,
               string_agg(replace(alternatives, ' | ', ' '), ' ' ORDER BY ord) AS words
        FROM expansions
      ),
      matched AS MATERIALIZED (
        SELECT a.id, a.search_text FROM articles a, fuzzy WHERE a.search_text @@ fuzzy.q
      )
      SELECT m.id FROM matched m, fuzzy
      ORDER BY m.search_text <@> to_bm25query(fuzzy.words, 'idx_articles_bm25'), m.id
      LIMIT $2::int
"""

HEADER = """schema_version: 1
id: cc_news_fuzzy_v1
name: "cc-news fuzzy v1"
description: "Typo-tolerant search over cc-news: 30 queries, 10 with one misspelt word, 10 harder, 10 spelled correctly"
version: "1.0.0"

corpus:
  name: cc-news-canonical@v1
  source: "sha256:7cdf4c719dc70cd013a1eb76fa33688564dc99af03ddf8cc57d20952cd4c89a3 (datasets/cc-news/canonical/v1/datapackage.json)"

"""


def edits(word):
    return 0 if len(word) <= 2 else 1 if len(word) <= 5 else 2


def paradedb_fuzzy(typed):
    clauses = []
    for field, boost in FIELDS:
        for word in typed.split():
            match = f"paradedb.match('{field}', '{word}', distance => {edits(word)})"
            clauses.append(f"paradedb.boost({boost}, {match})" if boost else match)
    body = ",\n    ".join(clauses)
    return ("SELECT id FROM articles\nWHERE id @@@ paradedb.boolean(should => ARRAY[\n    " + body + "\n])\n"
            "ORDER BY pdb.score(id) DESC\nLIMIT 100")


def templated(name, template, params):
    rendered = ", ".join(f"{key}: {json.dumps(value)}" for key, value in params.items())
    return [f"      {name}:", f"        template: {template}", f"        params: {{ {rendered} }}"]


def block(qid, category, description, typed, spelled):
    lines = [f"  - id: {qid}", f"    text: {json.dumps(typed)}", f"    description: {json.dumps(description)}",
             f"    category: {category}", "    engines:"]
    lines += templated("pg-fuzzy", "pg_fuzzy", {"terms": typed, "limit": 100})
    lines += templated("tiger-fuzzy", "tiger_fuzzy", {"terms": typed, "limit": 100})
    lines += [f"      paradedb-fuzzy: |"] + [f"        {line}" for line in paradedb_fuzzy(typed).splitlines()]
    lines += templated("es-fuzzy", "es_fuzzy", {"terms": typed})
    lines += templated("pg-trgm", "pg_trgm_title", {"terms": typed, "limit": 100})
    lines += templated("pg-gin", "pg_multi_field", {"terms": typed, "limit": 100})
    lines += templated("paradedb", "pdb_multi_field_boosted", {"terms": typed, "limit": 100})
    lines += templated("tiger", "tiger_multi_field", {"terms": typed, "limit": 100})
    lines += templated("elasticsearch", "es_multi_match_boosted", {"terms": typed})
    lines += templated("pg-gin-spelled", "pg_multi_field", {"terms": spelled, "limit": 100})
    return "\n".join(lines)


blocks = [block(q, "typo", d, t, s) for q, d, t, s in TYPO]
blocks += [block(q, "hard-typo", d, t, s) for q, d, t, s in HARD_TYPO]
blocks += [block(q, "keyword", d, t, t) for q, d, t in KEYWORD]

OUT.parent.mkdir(parents=True, exist_ok=True)
with open(OUT, "w") as out:
    out.write(HEADER + TEMPLATES + "\nqueries:\n" + "\n\n".join(blocks) + "\n")
print(len(blocks), "queries written to", OUT)
