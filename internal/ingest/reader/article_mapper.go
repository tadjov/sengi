package reader

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/internal/types/language"
	"github.com/tadjov/sengi/pkg/apis/mapping"
)

type ArticleMapper struct {
	spec     mapping.Spec
	language language.Code
}

func NewArticleMapper(spec mapping.Spec) (*ArticleMapper, error) {
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	lang, err := spec.Constants.ResolvedLanguage()
	if err != nil {
		return nil, err
	}
	return &ArticleMapper{spec: spec, language: lang}, nil
}

// Map returns the article without an id, or a *MappingDropError when a required value is missing or invalid.
func (m *ArticleMapper) Map(record map[string]string) (document.Article, error) {
	article := document.Article{Language: m.language}
	val := reflect.ValueOf(&article).Elem()

	for _, field := range m.spec.Fields {
		raw, present := record[field.Source]
		if !present || m.spec.IsMissing(raw) {
			if field.Required {
				return document.Article{}, &MappingDropError{Reason: ReasonMissing, Source: field.Source, Target: field.Target}
			}
			continue
		}

		converted, err := m.convert(raw, field)
		if err == nil && isBlank(converted) {
			err = fmt.Errorf("value %q converted to an empty %s", raw, field.ResolvedType())
		}
		if err == nil {
			err = m.assign(val, &article, field.Target, converted)
		}
		if err != nil {
			if field.Required {
				return document.Article{}, &MappingDropError{Reason: ReasonInvalid, Source: field.Source, Target: field.Target, Err: err}
			}
			slog.Warn("skipping optional field", "field", field.Target, "error", err)
		}
	}

	article.PublisherDomain = PublisherDomain(article.URL)
	return article, nil
}

var errInvalidURL = errors.New("not an absolute http(s) url")

func (m *ArticleMapper) convert(raw string, field mapping.Field) (any, error) {
	if mapping.IsListTarget(field.Target) {
		return splitList(raw, field.Separator), nil
	}
	return convertValue(raw, field.ResolvedType(), field.ResolvedFormat())
}

// splitList without a separator keeps raw as one element.
func splitList(raw, separator string) []string {
	parts := []string{raw}
	if separator != "" {
		parts = strings.Split(raw, separator)
	}
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			values = append(values, part)
		}
	}
	return values
}

// assign routes a converted value to its target. Metadata.Extra.<key> is a
// struct hop then a map key, which reflection cannot walk in one pass, so it is
// matched by prefix ahead of the ordinary struct paths.
func (m *ArticleMapper) assign(val reflect.Value, article *document.Article, target string, converted any) error {
	if key, ok := strings.CutPrefix(target, mapping.ExtraTargetPrefix); ok {
		if article.Metadata.Extra == nil {
			article.Metadata.Extra = make(map[string]any)
		}
		article.Metadata.Extra[key] = converted
		return nil
	}

	if path := strings.Split(target, "."); len(path) > 1 {
		return AssignNestedField(val, path, converted)
	}
	return AssignField(val, target, converted)
}

func isBlank(converted any) bool {
	switch v := converted.(type) {
	case string:
		return v == ""
	case time.Time:
		return v.IsZero()
	case []string:
		return len(v) == 0
	default:
		return false
	}
}

// ArticleDirectMapper reads records already in canonical form.
type ArticleDirectMapper struct{}

func NewArticleDirectMapper() *ArticleDirectMapper {
	return &ArticleDirectMapper{}
}

func (m *ArticleDirectMapper) Map(record map[string]string) (document.Article, error) {
	id, err := uuid.Parse(record["id"])
	if err != nil {
		return document.Article{}, fmt.Errorf("invalid id: %w", err)
	}

	lang, err := language.Parse(record["language"])
	if err != nil {
		return document.Article{}, fmt.Errorf("article %s: %w", id, err)
	}

	publishedAt, err := parseOptionalTime(record["publishedAt"])
	if err != nil {
		return document.Article{}, fmt.Errorf("article %s: invalid publishedAt: %w", id, err)
	}

	var metadata document.Metadata
	if raw := record["metadata"]; raw != "" {
		if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
			return document.Article{}, fmt.Errorf("article %s: invalid metadata: %w", id, err)
		}
	}

	return document.Article{
		ID:              id,
		URL:             record["url"],
		Title:           record["title"],
		Description:     record["description"],
		Content:         record["content"],
		PublishedAt:     publishedAt,
		Language:        lang,
		PublisherDomain: record["publisherDomain"],
		Metadata:        metadata,
	}, nil
}

func parseOptionalTime(v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339, v)
}
