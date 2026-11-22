CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS pg_trgm;
-- levenshtein for the fuzzy track's edit-distance engine.
CREATE EXTENSION IF NOT EXISTS fuzzystrmatch;
CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE articles
(
    id               uuid        NOT NULL PRIMARY KEY,
    url              text        NOT NULL,
    title            text        NOT NULL,
    description      text        NOT NULL DEFAULT '',
    content          text        NOT NULL,
    published_at     timestamptz NULL,
    language         text        NOT NULL,
    publisher_domain text        NOT NULL DEFAULT '',
    metadata         jsonb       NOT NULL DEFAULT '{}',
    search_vector    tsvector GENERATED ALWAYS AS (
        setweight(to_tsvector('english', title), 'A') ||
        setweight(to_tsvector('english', description), 'B') ||
        setweight(to_tsvector('english', content), 'C')
    ) STORED
);
CREATE INDEX idx_articles_published_at ON articles (published_at DESC NULLS LAST);
CREATE INDEX idx_articles_publisher_domain ON articles (publisher_domain);

CREATE TABLE article_embeddings
(
    article_id uuid         NOT NULL REFERENCES articles (id) ON DELETE CASCADE,
    model_name text         NOT NULL,
    embedding  vector(1024) NOT NULL,
    PRIMARY KEY (article_id, model_name)
);

-- The fuzzy track expands each query word to the corpus words within its edit
-- budget; indexes.sql fills this after the load.
CREATE TABLE corpus_words
(
    word text    NOT NULL PRIMARY KEY,
    ndoc integer NOT NULL
);

-- Elasticsearch's fuzziness AUTO: no edit up to 2 letters, one up to 5, two above.
CREATE FUNCTION fuzzy_edit_budget(word text) RETURNS integer
    LANGUAGE sql IMMUTABLE PARALLEL SAFE
    RETURN CASE WHEN length(word) <= 2 THEN 0 WHEN length(word) <= 5 THEN 1 ELSE 2 END;

-- Each query word's corpus words within its edit budget, nearest first, then most
-- frequent, at most 50 a word as Elasticsearch's max_expansions.
CREATE FUNCTION fuzzy_expansions(query text) RETURNS TABLE (query_position bigint, word text)
    LANGUAGE sql STABLE PARALLEL SAFE
BEGIN ATOMIC
    SELECT typed.query_position, near.word
    FROM regexp_split_to_table(lower(query), '[^a-z]+') WITH ORDINALITY AS typed (word, query_position)
    CROSS JOIN LATERAL (
        SELECT w.word FROM corpus_words w
        WHERE w.word % typed.word
          AND levenshtein_less_equal(w.word, typed.word, fuzzy_edit_budget(typed.word)) <= fuzzy_edit_budget(typed.word)
        ORDER BY levenshtein(w.word, typed.word), w.ndoc DESC, w.word
        LIMIT 50
    ) near
    WHERE typed.word <> '';
END;

-- A word's expansions ORed, the words ANDed.
CREATE FUNCTION fuzzy_tsquery(query text) RETURNS tsquery
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN (
        SELECT to_tsquery('english', string_agg('(' || alternatives || ')', ' & ' ORDER BY query_position))
        FROM (
            SELECT query_position, string_agg(word, ' | ' ORDER BY word) AS alternatives
            FROM fuzzy_expansions(query)
            GROUP BY query_position
        ) per_word
    );

CREATE TABLE corpus_load
(
    finished_at timestamptz NOT NULL PRIMARY KEY,
    record      jsonb       NOT NULL
);
