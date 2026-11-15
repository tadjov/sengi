CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
-- The timescaledb-ha image preloads pg_textsearch, so CREATE EXTENSION is enough.
CREATE EXTENSION IF NOT EXISTS pg_textsearch;
CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS fuzzystrmatch;
CREATE EXTENSION IF NOT EXISTS vector;

-- Keys and filter indexes are built after the load, in indexes.sql.
CREATE TABLE articles
(
    id               uuid        NOT NULL,
    url              text        NOT NULL,
    title            text        NOT NULL,
    description      text        NOT NULL DEFAULT '',
    content          text        NOT NULL,
    published_at     timestamptz NULL,
    language         text        NOT NULL,
    publisher_domain text        NOT NULL DEFAULT '',
    metadata         jsonb       NOT NULL DEFAULT '{}',
    -- pg_textsearch indexes one text column, so the searchable fields are concatenated.
    search_text      text GENERATED ALWAYS AS (
        title || ' ' || description || ' ' || content
    ) STORED
);

CREATE TABLE article_embeddings
(
    article_id uuid         NOT NULL,
    model_name text         NOT NULL,
    embedding  vector(1024) NOT NULL
);

-- The fuzzy track expands each query word to the corpus words within its edit
-- budget; indexes.sql fills this after the load.
CREATE TABLE corpus_words
(
    word text    NOT NULL PRIMARY KEY,
    ndoc integer NOT NULL
);

CREATE TABLE corpus_load
(
    finished_at timestamptz NOT NULL PRIMARY KEY,
    record      jsonb       NOT NULL
);
