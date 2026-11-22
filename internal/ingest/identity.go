package ingest

import (
	"crypto/sha256"
	"fmt"
	"hash"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/datapackage"
	"github.com/tadjov/sengi/internal/ingest/datamapping"
	"github.com/tadjov/sengi/internal/types/document"
)

// articleIdentity drops records that repeat a kept article and gives each kept one its id.
type articleIdentity struct {
	idKind datamapping.IDKind
	urls   map[uuid.UUID]struct{}
	rules  []uniqueRule
	// fields lists each field the rules compare once, so a field two rules share is hashed once per record.
	fields    []string
	digests   []fieldDigest
	collapsed []byte
	keyHash   hash.Hash
}

type uniqueRule struct {
	name   string
	fields []int
	kept   map[uniqueKey]struct{}
}

type uniqueKey [sha256.Size]byte

type fieldDigest struct {
	sum   [sha256.Size]byte
	empty bool
}

func newArticleIdentity(identity datamapping.Identity) (*articleIdentity, error) {
	kind, err := identity.ResolvedIDKind()
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	a := &articleIdentity{idKind: kind, urls: map[uuid.UUID]struct{}{}, keyHash: sha256.New()}
	position := map[string]int{}
	for _, rule := range identity.Unique {
		compared := uniqueRule{name: rule.Name, kept: map[uniqueKey]struct{}{}}
		for _, field := range rule.Fields {
			i, seen := position[field]
			if !seen {
				i = len(a.fields)
				position[field] = i
				a.fields = append(a.fields, field)
			}
			compared.fields = append(compared.fields, i)
		}
		a.rules = append(a.rules, compared)
	}
	a.digests = make([]fieldDigest, len(a.fields))
	return a, nil
}

// admit gives article its id, or names the first rule under which it repeats a kept article.
func (a *articleIdentity) admit(article *document.Article) (rule string, kept bool, err error) {
	urlID := document.DeriveArticleID(article.URL)
	if _, repeated := a.urls[urlID]; repeated {
		return datamapping.URLRule, false, nil
	}
	a.digestFields(*article)
	keys := make([]*uniqueKey, len(a.rules))
	for i, rule := range a.rules {
		key, ok := a.keyOf(rule)
		if !ok {
			continue
		}
		if _, repeated := rule.kept[key]; repeated {
			a.urls[urlID] = struct{}{}
			return rule.name, false, nil
		}
		keys[i] = &key
	}

	if article.ID, err = a.newID(urlID); err != nil {
		return "", false, err
	}
	a.urls[urlID] = struct{}{}
	for i, key := range keys {
		if key != nil {
			a.rules[i].kept[*key] = struct{}{}
		}
	}
	return "", true, nil
}

func (a *articleIdentity) newID(urlID uuid.UUID) (uuid.UUID, error) {
	if a.idKind != datamapping.IDKindRandom {
		return urlID, nil
	}
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("mint a random id: %w", err)
	}
	return id, nil
}

func (a *articleIdentity) digestFields(article document.Article) {
	for i, field := range a.fields {
		a.collapsed = appendCollapsed(a.collapsed[:0], datamapping.UniqueFieldValue(article, field))
		a.digests[i] = fieldDigest{sum: sha256.Sum256(a.collapsed), empty: len(a.collapsed) == 0}
	}
}

// keyOf has no key when a field is empty, as NULL never conflicts under UNIQUE.
func (a *articleIdentity) keyOf(rule uniqueRule) (uniqueKey, bool) {
	a.keyHash.Reset()
	for _, i := range rule.fields {
		if a.digests[i].empty {
			return uniqueKey{}, false
		}
		a.keyHash.Write(a.digests[i].sum[:])
	}
	var key uniqueKey
	a.keyHash.Sum(key[:0])
	return key, true
}

var asciiSpace = [utf8.RuneSelf]bool{'\t': true, '\n': true, '\v': true, '\f': true, '\r': true, ' ': true}

// appendCollapsed appends value trimmed, with each whitespace run inside it as one space,
// keeping every other byte as written, invalid UTF-8 included.
func appendCollapsed(dst []byte, value string) []byte {
	start, word := len(dst), -1
	for i := 0; i < len(value); {
		space, size := false, 1
		if c := value[i]; c < utf8.RuneSelf {
			space = asciiSpace[c]
		} else {
			var r rune
			r, size = utf8.DecodeRuneInString(value[i:])
			space = unicode.IsSpace(r)
		}
		switch {
		case space && word >= 0:
			dst = appendWord(dst, start, value[word:i])
			word = -1
		case !space && word < 0:
			word = i
		}
		i += size
	}
	if word >= 0 {
		dst = appendWord(dst, start, value[word:])
	}
	return dst
}

func appendWord(dst []byte, start int, word string) []byte {
	if len(dst) > start {
		dst = append(dst, ' ')
	}
	return append(dst, word...)
}

func describeIdentity(identity datamapping.Identity, kind datamapping.IDKind) *datapackage.Identity {
	described := &datapackage.Identity{Unique: identity.Unique, IDKind: kind, Algorithm: "uuidv7"}
	if kind == datamapping.IDKindURLHash {
		described.Algorithm = "uuidv5"
		described.Namespace = document.ArticleIDNamespace.String()
	}
	return described
}
