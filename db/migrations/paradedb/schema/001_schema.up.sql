CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS pg_search;
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
    metadata         jsonb       NOT NULL DEFAULT '{}'
);

CREATE TABLE article_embeddings
(
    article_id uuid         NOT NULL,
    model_name text         NOT NULL,
    embedding  vector(1024) NOT NULL
);

CREATE TABLE corpus_load
(
    finished_at timestamptz NOT NULL PRIMARY KEY,
    record      jsonb       NOT NULL
);
