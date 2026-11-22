package ingest

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/datapackage"
	"github.com/tadjov/sengi/internal/ingest/reader"
	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/pkg/apis/datamapping"
)

// articleIdentity drops records that repeat a kept article and gives each kept one its id.
type articleIdentity struct {
	urlIdentity *datamapping.URLIdentity
	idKind      datamapping.IDKind
	pages       map[uuid.UUID]struct{}
	rules       []uniqueRule
}

type uniqueRule struct {
	name   string
	fields []string
	kept   map[uniqueKey]struct{}
}

type uniqueKey [sha256.Size]byte

func newArticleIdentity(identity datamapping.Identity) (*articleIdentity, error) {
	kind, err := identity.ResolvedIDKind()
	if err != nil {
		return nil, fmt.Errorf("identity: %w", err)
	}
	rules := make([]uniqueRule, len(identity.Unique))
	for i, rule := range identity.Unique {
		for _, field := range rule.Fields {
			if _, ok := uniqueFieldValue(document.Article{}, field); !ok {
				return nil, fmt.Errorf("identity: unique rule %q compares field %q, which preprocess cannot read", rule.Name, field)
			}
		}
		rules[i] = uniqueRule{name: rule.Name, fields: rule.Fields, kept: map[uniqueKey]struct{}{}}
	}
	return &articleIdentity{
		urlIdentity: identity.URL,
		idKind:      kind,
		pages:       map[uuid.UUID]struct{}{},
		rules:       rules,
	}, nil
}

// admit gives article its id when no kept article shares its page url or a unique
// rule's key; otherwise it names the first rule that matched.
func (a *articleIdentity) admit(article *document.Article) (rule string, kept bool, err error) {
	pageID := document.DeriveArticleID(reader.PageURL(article.URL, a.urlIdentity))
	if _, repeated := a.pages[pageID]; repeated {
		return datamapping.URLRule, false, nil
	}
	keys := make([]*uniqueKey, len(a.rules))
	for i, rule := range a.rules {
		key, ok := rule.keyOf(*article)
		if !ok {
			continue
		}
		if _, repeated := rule.kept[key]; repeated {
			return rule.name, false, nil
		}
		keys[i] = &key
	}

	if article.ID, err = a.newID(pageID); err != nil {
		return "", false, err
	}
	a.pages[pageID] = struct{}{}
	for i, key := range keys {
		if key != nil {
			a.rules[i].kept[*key] = struct{}{}
		}
	}
	return "", true, nil
}

func (a *articleIdentity) newID(pageID uuid.UUID) (uuid.UUID, error) {
	if a.idKind != datamapping.IDKindRandom {
		return pageID, nil
	}
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("mint a random id: %w", err)
	}
	return id, nil
}

// keyOf collapses whitespace runs; an empty field means no key, as NULL never conflicts under UNIQUE.
func (r uniqueRule) keyOf(article document.Article) (uniqueKey, bool) {
	hash := sha256.New()
	for _, field := range r.fields {
		value, _ := uniqueFieldValue(article, field)
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

func uniqueFieldValue(article document.Article, field string) (string, bool) {
	switch field {
	case datamapping.TargetTitle:
		return article.Title, true
	case "Description":
		return article.Description, true
	case "Content":
		return article.Content, true
	case datamapping.TargetURL:
		return article.URL, true
	case datamapping.TargetPublisherDomain:
		return article.PublisherDomain, true
	case "Metadata.ExternalID":
		return article.Metadata.ExternalID, true
	case "Metadata.PublisherName":
		return article.Metadata.PublisherName, true
	case "Metadata.Section":
		return article.Metadata.Section, true
	default:
		return "", false
	}
}

func describeIdentity(identity datamapping.Identity, kind datamapping.IDKind) *datapackage.Identity {
	described := &datapackage.Identity{URL: identity.URL, Unique: identity.Unique, IDKind: kind, Algorithm: "uuidv7"}
	if kind == datamapping.IDKindURLHash {
		described.Algorithm = "uuidv5"
		described.Namespace = document.ArticleIDNamespace.String()
	}
	return described
}
