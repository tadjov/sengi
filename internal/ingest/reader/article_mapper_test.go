package reader_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tadjov/sengi/internal/ingest/reader"
	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/internal/types/language"
)

const everyTargetMapping = `kind: DataMapper
version: v1
metadata:
  name: Test
dataset: test
dateFormat: "02/01/2006 15:04"
constants:
  Language: eng
output:
  format: parquet
identity:
  idKind: urlHash
fieldMappings:
  - {source: title, target: Title, required: true}
  - {source: link, sourceType: url, target: URL, required: true}
  - {source: summary, target: Description}
  - {source: body, target: Content}
  - {source: date, sourceType: datetime, target: PublishedAt}
  - {source: uid, target: Metadata.ExternalID}
  - {source: outlet, target: Metadata.PublisherName}
  - {source: byline, target: Metadata.Authors, separator: ';'}
  - {source: section, target: Metadata.Section}
  - {source: tags, target: Metadata.Keywords}
  - {source: image_url, sourceType: url, target: Metadata.Image}
  - {source: modified, sourceType: date, target: Metadata.DateModified}
  - {source: words, sourceType: int, target: Metadata.Extra.wordCount}
  - {source: score, sourceType: float, target: Metadata.Extra.score}
  - {source: paywalled, sourceType: bool, target: Metadata.Extra.paywalled}
  - {source: note, target: Metadata.Extra.note}
  - {source: first_seen, sourceType: date, target: Metadata.Extra.firstSeen}
  - {source: crawled, sourceType: datetime, target: Metadata.Extra.crawled}
  - {source: amp, sourceType: url, target: Metadata.Extra.amp}
`

func everyTargetRecord() map[string]string {
	return map[string]string{
		"title":      "Storm hits coast",
		"link":       "  https://News.Example.co.uk/a/1 ",
		"summary":    "Lead one",
		"body":       "Body one",
		"date":       "04/03/2019 05:06",
		"uid":        "89541",
		"outlet":     "Example News",
		"byline":     "Jane Doe; John Roe ;",
		"section":    "World",
		"tags":       "weather, coast",
		"image_url":  "https://img.example.com/1.jpg",
		"modified":   "2019-03-05",
		"words":      "412",
		"score":      "4.5",
		"paywalled":  "true",
		"note":       "syndicated",
		"first_seen": "2019-03-01",
		"crawled":    "2019-03-06T07:08:09Z",
		"amp":        "https://AMP.example.com/a/1",
		"unmapped":   "ignored",
	}
}

func newMapper(t *testing.T, doc string) *reader.ArticleMapper {
	t.Helper()
	mapping, err := loadMapping(doc)
	require.NoError(t, err)
	lang, err := mapping.Constants.ResolvedLanguage()
	require.NoError(t, err)
	return reader.NewArticleMapper(mapping, lang)
}

func TestArticleMapper_MapsEveryTargetAndSourceType(t *testing.T) {
	article, err := newMapper(t, everyTargetMapping).Map(everyTargetRecord())
	require.NoError(t, err)

	assert.Equal(t, document.Article{
		URL:             "https://news.example.co.uk/a/1",
		Title:           "Storm hits coast",
		Description:     "Lead one",
		Content:         "Body one",
		PublishedAt:     time.Date(2019, 3, 4, 5, 6, 0, 0, time.UTC),
		Language:        language.English,
		PublisherDomain: "example.co.uk",
		Metadata: document.Metadata{
			ExternalID:    "89541",
			PublisherName: "Example News",
			Authors:       []string{"Jane Doe", "John Roe"},
			Section:       "World",
			Keywords:      []string{"weather, coast"},
			Image:         "https://img.example.com/1.jpg",
			DateModified:  time.Date(2019, 3, 5, 0, 0, 0, 0, time.UTC),
			Extra: map[string]any{
				"wordCount": int64(412),
				"score":     4.5,
				"paywalled": true,
				"note":      "syndicated",
				"firstSeen": time.Date(2019, 3, 1, 0, 0, 0, 0, time.UTC),
				"crawled":   time.Date(2019, 3, 6, 7, 8, 9, 0, time.UTC),
				"amp":       "https://amp.example.com/a/1",
			},
		},
	}, article)
}

func TestArticleMapper_LeavesOutAnOptionalValueItCannotUse(t *testing.T) {
	tests := []struct {
		name   string
		source string
		value  string
	}{
		{name: "a blank value", source: "summary", value: "   "},
		{name: "an unparseable datetime", source: "date", value: "yesterday"},
		{name: "an unparseable date", source: "modified", value: "05/03/2019"},
		{name: "a url without a host", source: "image_url", value: "1.jpg"},
		{name: "a list of only separators", source: "byline", value: " ; ;"},
		{name: "an unparseable int", source: "words", value: "many"},
		{name: "an unparseable float", source: "score", value: "high"},
		{name: "an unparseable bool", source: "paywalled", value: "maybe"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mapper := newMapper(t, everyTargetMapping)
			complete, err := mapper.Map(everyTargetRecord())
			require.NoError(t, err)
			withoutColumn := everyTargetRecord()
			delete(withoutColumn, tt.source)
			want, err := mapper.Map(withoutColumn)
			require.NoError(t, err)
			require.NotEqual(t, complete, want)

			record := everyTargetRecord()
			record[tt.source] = tt.value
			article, err := mapper.Map(record)

			require.NoError(t, err)
			assert.Equal(t, want, article)
		})
	}
}

func TestArticleMapper_DropsARecordWhoseRequiredValueItCannotUse(t *testing.T) {
	tests := []struct {
		name       string
		edit       func(record map[string]string)
		wantReason reader.DropReason
		wantTarget string
	}{
		{name: "an absent column", edit: func(r map[string]string) { delete(r, "title") }, wantReason: reader.ReasonMissingColumn, wantTarget: "Title"},
		{name: "a blank value", edit: func(r map[string]string) { r["title"] = " \t" }, wantReason: reader.ReasonEmptyValue, wantTarget: "Title"},
		{name: "a url that is not http", edit: func(r map[string]string) { r["link"] = "ftp://example.com/a" }, wantReason: reader.ReasonInvalidURL, wantTarget: "URL"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := everyTargetRecord()
			tt.edit(record)

			_, err := newMapper(t, everyTargetMapping).Map(record)

			var drop *reader.MappingDropError
			require.ErrorAs(t, err, &drop)
			assert.Equal(t, tt.wantReason, drop.Reason)
			assert.Equal(t, tt.wantTarget, drop.Target)
		})
	}
}

func TestArticleMapper_ReadsADatetimeInACommonFormatWhenTheMappingsFormatFails(t *testing.T) {
	tests := []struct {
		value string
		want  time.Time
	}{
		{value: "2023-10-30 10:12:35.000000", want: time.Date(2023, 10, 30, 10, 12, 35, 0, time.UTC)},
		{value: "2023-11-27 16:52:44", want: time.Date(2023, 11, 27, 16, 52, 44, 0, time.UTC)},
		{value: "2023-11-27T16:52:44.250000Z", want: time.Date(2023, 11, 27, 16, 52, 44, 250_000_000, time.UTC)},
		{value: "2023-11-27T16:52:44", want: time.Date(2023, 11, 27, 16, 52, 44, 0, time.UTC)},
		{value: "2023-11-27T16:52:44+02:00", want: time.Date(2023, 11, 27, 14, 52, 44, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			record := everyTargetRecord()
			record["date"] = tt.value

			article, err := newMapper(t, everyTargetMapping).Map(record)

			require.NoError(t, err)
			assert.True(t, tt.want.Equal(article.PublishedAt), "PublishedAt = %s, want %s", article.PublishedAt, tt.want)
		})
	}
}
