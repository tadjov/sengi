-- The fuzzy track expands each query word to the corpus words within its edit
-- budget; indexes.sql fills this after the load.
CREATE TABLE corpus_words
(
    word text    NOT NULL PRIMARY KEY,
    ndoc integer NOT NULL
);
