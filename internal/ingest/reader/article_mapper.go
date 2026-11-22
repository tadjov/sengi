package reader

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/internal/types/language"
	"github.com/tadjov/sengi/pkg/apis/datamapping"
	"github.com/tadjov/sengi/pkg/utils"
)

type ArticleMapper struct {
	cfg      *datamapping.DataMapper
	language language.Code
}

func NewArticleMapper(cfg *datamapping.DataMapper) (*ArticleMapper, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	lang, err := cfg.Constants.ResolvedLanguage()
	if err != nil {
		return nil, err
	}
	return &ArticleMapper{cfg: cfg, language: lang}, nil
}

// Map returns the article without an id, or a *MappingDropError when a required value is missing or invalid.
func (m *ArticleMapper) Map(record map[string]string) (document.Article, error) {
	article := document.Article{Language: m.language}

	for _, fm := range m.cfg.FieldMappings {
		raw, present := record[fm.Source]
		if !present {
			if fm.Required {
				return document.Article{}, &MappingDropError{Reason: ReasonMissingColumn, Source: fm.Source, Target: fm.Target}
			}
			continue
		}

		if strings.TrimSpace(raw) == "" {
			if fm.Required {
				return document.Article{}, &MappingDropError{Reason: ReasonEmptyValue, Source: fm.Source, Target: fm.Target}
			}
			continue
		}

		converted, err := m.convert(raw, fm)
		if err == nil && isBlank(converted) {
			err = fmt.Errorf("value %q converted to an empty %s", raw, fm.SourceType)
		}
		if err == nil {
			err = assign(&article, fm.Target, converted)
		}
		if err != nil {
			if fm.Required {
				reason := ReasonConversion
				if errors.Is(err, errInvalidURL) {
					reason = ReasonInvalidURL
				}
				return document.Article{}, &MappingDropError{Reason: reason, Source: fm.Source, Target: fm.Target, Err: err}
			}
			slog.Warn("skipping optional field", "field", fm.Target, "error", err)
		}
	}

	article.PublisherDomain = PublisherDomain(article.URL)
	return article, nil
}

var errInvalidURL = errors.New("not an absolute http(s) url")

func (m *ArticleMapper) convert(raw string, fm datamapping.FieldMapping) (any, error) {
	sourceType, err := fm.ResolvedSourceType()
	if err != nil {
		return nil, err
	}
	if datamapping.IsListTarget(fm.Target) {
		return splitList(raw, fm.Separator), nil
	}
	return convertValue(raw, sourceType, m.cfg.DateFormat)
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

func assign(article *document.Article, target string, converted any) error {
	if key, ok := strings.CutPrefix(target, datamapping.ExtraTargetPrefix); ok {
		if article.Metadata.Extra == nil {
			article.Metadata.Extra = make(map[string]any)
		}
		article.Metadata.Extra[key] = converted
		return nil
	}

	var field any
	switch target {
	case datamapping.TargetURL:
		field = &article.URL
	case datamapping.TargetTitle:
		field = &article.Title
	case "Description":
		field = &article.Description
	case "Content":
		field = &article.Content
	case "PublishedAt":
		field = &article.PublishedAt
	case "Metadata.ExternalID":
		field = &article.Metadata.ExternalID
	case "Metadata.PublisherName":
		field = &article.Metadata.PublisherName
	case "Metadata.Authors":
		field = &article.Metadata.Authors
	case "Metadata.Section":
		field = &article.Metadata.Section
	case "Metadata.Keywords":
		field = &article.Metadata.Keywords
	case "Metadata.Image":
		field = &article.Metadata.Image
	case "Metadata.DateModified":
		field = &article.Metadata.DateModified
	default:
		return fmt.Errorf("unknown target %q", target)
	}

	switch field := field.(type) {
	case *string:
		if value, ok := converted.(string); ok {
			*field = value
			return nil
		}
	case *time.Time:
		if value, ok := converted.(time.Time); ok {
			*field = value
			return nil
		}
	case *[]string:
		if value, ok := converted.([]string); ok {
			*field = value
			return nil
		}
	}
	return fmt.Errorf("target %s cannot hold a %T", target, converted)
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

	publishedAt, err := utils.ParseTimeOptional(record["publishedAt"])
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
