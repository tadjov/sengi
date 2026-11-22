package native

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/tadjov/sengi/internal/token"
)

// booleanTsQuery renders a boolean expression as to_tsquery text, its tokens space separated.
func booleanTsQuery(expression string) (string, error) {
	tokens := token.Tokenize(expression)
	if err := token.Validate(tokens); err != nil {
		return "", err
	}

	var parts []string
	prevType := token.EOF

	for _, tok := range tokens {
		if tok.Type == token.EOF {
			break
		}

		if needsImplicitAnd(prevType, tok.Type) {
			parts = append(parts, "&")
		}

		switch tok.Type {
		case token.WORD:
			words := strings.Fields(sanitizeTerm(tok.Value))
			if len(words) > 1 {
				parts = append(parts, strings.Join(words, " <-> "))
			} else if len(words) == 1 {
				parts = append(parts, words[0])
			}
		case token.AND:
			parts = append(parts, "&")
		case token.OR:
			parts = append(parts, "|")
		case token.NOT:
			parts = append(parts, "!")
		case token.LPAREN:
			parts = append(parts, "(")
		case token.RPAREN:
			parts = append(parts, ")")
		}

		prevType = tok.Type
	}

	result := strings.Join(parts, " ")
	if result == "" {
		return "", fmt.Errorf("empty boolean expression")
	}
	return result, nil
}

func needsImplicitAnd(prev, curr token.Type) bool {
	prevIsValue := prev == token.WORD || prev == token.RPAREN
	currIsValue := curr == token.WORD || curr == token.LPAREN || curr == token.NOT
	return prevIsValue && currIsValue
}

func sanitizeTerm(word string) string {
	var b strings.Builder
	for _, r := range word {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == ' ' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
