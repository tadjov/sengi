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

-- Every expansion as one any-word query for to_bm25query.
CREATE FUNCTION fuzzy_words(query text) RETURNS text
    LANGUAGE sql STABLE PARALLEL SAFE
    RETURN (SELECT string_agg(word, ' ' ORDER BY query_position, word) FROM fuzzy_expansions(query));
