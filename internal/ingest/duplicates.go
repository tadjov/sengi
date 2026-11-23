package ingest

import (
	"crypto/sha256"
	"fmt"
	"hash"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/tadjov/sengi/internal/types/document"
	"github.com/tadjov/sengi/pkg/apis/mapping"
	"github.com/tadjov/sengi/pkg/apis/preprocess"
	"github.com/tadjov/sengi/pkg/datapackage"
)

type digest [sha256.Size]byte

// fieldDigests hashes each compared field of one article once, whitespace collapsed,
// for the duplicates keys, signals and rules that compare it.
type fieldDigests struct {
	fields    []string
	position  map[string]int
	sums      []digest
	filled    []bool
	collapsed []byte
}

func newFieldDigests() *fieldDigests {
	return &fieldDigests{position: map[string]int{}}
}

// index registers field and returns its position.
func (d *fieldDigests) index(field string) int {
	if i, ok := d.position[field]; ok {
		return i
	}
	i := len(d.fields)
	d.position[field] = i
	d.fields = append(d.fields, field)
	d.sums = append(d.sums, digest{})
	d.filled = append(d.filled, false)
	return i
}

func (d *fieldDigests) compute(article document.Article) {
	for i, field := range d.fields {
		d.collapsed = appendCollapsed(d.collapsed[:0], mapping.TextValue(article, field))
		d.sums[i] = sha256.Sum256(d.collapsed)
		d.filled[i] = len(d.collapsed) > 0
	}
}

// combined hashes the digests of fields in order; it has none when a field is empty,
// as NULL never conflicts under UNIQUE.
func (d *fieldDigests) combined(h hash.Hash, fields []int) (digest, bool) {
	if len(fields) == 1 {
		return d.sums[fields[0]], d.filled[fields[0]]
	}
	h.Reset()
	for _, i := range fields {
		if !d.filled[i] {
			return digest{}, false
		}
		h.Write(d.sums[i][:])
	}
	var sum digest
	h.Sum(sum[:0])
	return sum, true
}

// duplicateFinder keeps the first record of each article, in raw order, and gives it its id.
type duplicateFinder struct {
	idKind  preprocess.IDKind
	keys    []duplicateKey
	urlKey  int
	digests *fieldDigests
	keyHash hash.Hash
	values  []keyValue
	ids     map[uuid.UUID]struct{}
}

type duplicateKey struct {
	name   string
	fields []int
	kept   map[digest]uuid.UUID
}

type keyValue struct {
	sum digest
	ok  bool
}

// duplicate names the key a dropped record matched and the kept article it repeats.
type duplicate struct {
	key  string
	twin uuid.UUID
}

func newDuplicateFinder(spec preprocess.Spec, digests *fieldDigests) *duplicateFinder {
	f := &duplicateFinder{
		idKind:  spec.ID.Kind,
		urlKey:  -1,
		digests: digests,
		keyHash: sha256.New(),
		ids:     map[uuid.UUID]struct{}{},
	}
	for i, key := range spec.Unique.Keys {
		compared := duplicateKey{name: key.Name, kept: map[digest]uuid.UUID{}}
		for _, field := range key.Fields {
			compared.fields = append(compared.fields, digests.index(field))
		}
		if key.IsURL() {
			f.urlKey = i
		}
		f.keys = append(f.keys, compared)
	}
	f.values = make([]keyValue, len(f.keys))
	return f
}

// admit gives article its id, or names the first key a kept article already holds.
// The digests must have been computed for article.
func (f *duplicateFinder) admit(article *document.Article) (*duplicate, error) {
	for i, key := range f.keys {
		sum, ok := f.digests.combined(f.keyHash, key.fields)
		f.values[i] = keyValue{sum: sum, ok: ok}
		if !ok {
			continue
		}
		if twin, repeated := key.kept[sum]; repeated {
			f.claimURL(i, twin)
			return &duplicate{key: key.name, twin: twin}, nil
		}
	}

	id, err := f.newID(article.URL)
	if err != nil {
		return nil, err
	}
	if _, taken := f.ids[id]; taken {
		return nil, fmt.Errorf("two articles would share id %s; the url key must keep apart every record id kind %s derives one id from", id, f.idKind)
	}
	f.ids[id] = struct{}{}
	article.ID = id
	for i, value := range f.values {
		if value.ok {
			f.keys[i].kept[value.sum] = id
		}
	}
	return nil, nil
}

// claimURL leaves the url of a record that key matched with the article it repeats, so
// no later record takes it. A url already kept keeps its owner.
func (f *duplicateFinder) claimURL(matched int, twin uuid.UUID) {
	if f.urlKey < 0 || f.urlKey == matched {
		return
	}
	value := f.values[f.urlKey]
	if f.urlKey > matched {
		value.sum, value.ok = f.digests.combined(f.keyHash, f.keys[f.urlKey].fields)
	}
	if _, owned := f.keys[f.urlKey].kept[value.sum]; value.ok && !owned {
		f.keys[f.urlKey].kept[value.sum] = twin
	}
}

func (f *duplicateFinder) newID(url string) (uuid.UUID, error) {
	if f.idKind != preprocess.IDKindRandom {
		return document.DeriveArticleID(url), nil
	}
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.Nil, fmt.Errorf("mint a random id: %w", err)
	}
	return id, nil
}

func describeIDs(kind preprocess.IDKind) datapackage.ArticleIDs {
	if kind == preprocess.IDKindURLHash {
		return datapackage.ArticleIDs{Kind: kind, Algorithm: "uuidv5", Namespace: document.ArticleIDNamespace.String()}
	}
	return datapackage.ArticleIDs{Kind: kind, Algorithm: "uuidv7"}
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
