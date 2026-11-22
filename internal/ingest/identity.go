package ingest

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"

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
}

type uniqueRule struct {
	datamapping.UniqueRule
	kept map[uniqueKey]struct{}
}

type uniqueKey [sha256.Size]byte

func newArticleIdentity(identity datamapping.Identity) (*articleIdentity, error) {
	kind, err := identity.ResolvedIDKind()
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	rules := make([]uniqueRule, len(identity.Unique))
	for i, rule := range identity.Unique {
		rules[i] = uniqueRule{UniqueRule: rule, kept: map[uniqueKey]struct{}{}}
	}
	return &articleIdentity{idKind: kind, urls: map[uuid.UUID]struct{}{}, rules: rules}, nil
}

// admit gives article its id, or names the first rule under which it repeats a kept article.
func (a *articleIdentity) admit(article *document.Article) (rule string, kept bool, err error) {
	urlID := document.DeriveArticleID(article.URL)
	if _, repeated := a.urls[urlID]; repeated {
		return datamapping.URLRule, false, nil
	}
	keys := make([]*uniqueKey, len(a.rules))
	for i, rule := range a.rules {
		key, ok := keyOf(rule.Values(*article))
		if !ok {
			continue
		}
		if _, repeated := rule.kept[key]; repeated {
			a.urls[urlID] = struct{}{}
			return rule.Name, false, nil
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

// keyOf collapses whitespace runs; an empty value means no key, as NULL never conflicts under UNIQUE.
func keyOf(values []string) (uniqueKey, bool) {
	hash := sha256.New()
	for _, value := range values {
		value = strings.Join(strings.Fields(value), " ")
		if value == "" {
			return uniqueKey{}, false
		}
		hash.Write(binary.BigEndian.AppendUint64(nil, uint64(len(value))))
		hash.Write([]byte(value))
	}
	var key uniqueKey
	hash.Sum(key[:0])
	return key, true
}

func describeIdentity(identity datamapping.Identity, kind datamapping.IDKind) *datapackage.Identity {
	described := &datapackage.Identity{Unique: identity.Unique, IDKind: kind, Algorithm: "uuidv7"}
	if kind == datamapping.IDKindURLHash {
		described.Algorithm = "uuidv5"
		described.Namespace = document.ArticleIDNamespace.String()
	}
	return described
}
