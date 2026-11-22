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
