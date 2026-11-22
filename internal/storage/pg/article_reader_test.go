package pg_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/storage/pg"
	"github.com/tadjov/sengi/internal/storage/pg/pgtest"
	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/internal/types/language"
)

func TestArticleReader_GetByIDs_ReturnsEverySavedField(t *testing.T) {
	full := document.Article{
		ID:              uuid.MustParse("6f1c0d53-1b0e-5a51-9a2b-7d0f6c3e8a01"),
		URL:             "https://uk.reuters.com/world/storm",
		Title:           "Storm hits the coast",
		Description:     "Thousands without power",
		Content:         "The storm made landfall overnight.",
		PublishedAt:     time.Date(2019, 3, 4, 5, 6, 7, 0, time.UTC),
		Language:        language.English,
		PublisherDomain: "reuters.com",
		Metadata: document.Metadata{
			ExternalID:    "reuters-0412",
			PublisherName: "Reuters",
			Authors:       []string{"Jane Doe", "John Roe"},
			Section:       "World",
			Keywords:      []string{"storm", "coast"},
			Image:         "https://uk.reuters.com/storm.jpg",
			DateModified:  time.Date(2019, 3, 5, 0, 0, 0, 0, time.UTC),
			Extra:         map[string]any{"wordCount": "412"},
		},
	}
	bare := document.Article{
		ID:       uuid.MustParse("6f1c0d53-1b0e-5a51-9a2b-7d0f6c3e8a02"),
		URL:      "https://example.com/bare",
		Title:    "Bare",
		Content:  "No date, no description, no metadata.",
		Language: language.English,
	}
	pool := loadedPool(t, pgtest.Corpus{Articles: []document.Article{full, bare}})

	got, err := pg.NewArticleReader(pool).GetByIDs(context.Background(), []uuid.UUID{full.ID, bare.ID})
	require.NoError(t, err)

	byID := make(map[uuid.UUID]document.Article, len(got))
	for _, a := range got {
		a.PublishedAt = a.PublishedAt.UTC()
		a.Metadata.DateModified = a.Metadata.DateModified.UTC()
		byID[a.ID] = a
	}
	assert.Equal(t, full, byID[full.ID])
	assert.Equal(t, bare, byID[bare.ID])
}
