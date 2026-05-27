package vectordata

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type typedTestItem struct {
	ID      string
	Content string
}

type typedTestCodec struct {
	encodeErr error
	decodeErr error
}

func (c typedTestCodec) Encode(value typedTestItem) (Record, error) {
	if c.encodeErr != nil {
		return Record{}, c.encodeErr
	}
	return Record{
		ID:      value.ID,
		Vector:  []float32{1, 2},
		Content: &value.Content,
	}, nil
}

func (c typedTestCodec) Decode(record Record) (typedTestItem, error) {
	if c.decodeErr != nil {
		return typedTestItem{}, c.decodeErr
	}
	item := typedTestItem{ID: record.ID}
	if record.Content != nil {
		item.Content = *record.Content
	}
	return item, nil
}

type typedTestCollection struct {
	inserted      []Record
	upserted      []Record
	getRecord     Record
	getID         string
	getErr        error
	searchResults []SearchResult
	searchVector  []float32
	searchTopK    int
	searchOptions SearchOptions
	searchErr     error
}

func (c *typedTestCollection) Name() string                                      { return "typed" }
func (c *typedTestCollection) Dimension() int                                    { return 2 }
func (c *typedTestCollection) Metric() DistanceMetric                            { return DistanceCosine }
func (c *typedTestCollection) Delete(context.Context, []string) (int64, error)   { return 0, nil }
func (c *typedTestCollection) Count(context.Context, Filter) (int64, error)      { return 0, nil }
func (c *typedTestCollection) EnsureIndexes(context.Context, IndexOptions) error { return nil }

func (c *typedTestCollection) Insert(_ context.Context, records []Record) error {
	c.inserted = append([]Record(nil), records...)
	return nil
}

func (c *typedTestCollection) Upsert(_ context.Context, records []Record) error {
	c.upserted = append([]Record(nil), records...)
	return nil
}

func (c *typedTestCollection) Get(_ context.Context, id string) (Record, error) {
	c.getID = id
	if c.getErr != nil {
		return Record{}, c.getErr
	}
	return c.getRecord, nil
}

func (c *typedTestCollection) SearchByVector(_ context.Context, vector []float32, topK int, opts SearchOptions) ([]SearchResult, error) {
	c.searchVector = append([]float32(nil), vector...)
	c.searchTopK = topK
	c.searchOptions = opts
	if c.searchErr != nil {
		return nil, c.searchErr
	}
	return c.searchResults, nil
}

func TestTypedCollectionInsertAndUpsertEncodeValues(t *testing.T) {
	// Arrange
	base := &typedTestCollection{}
	collection := NewTypedCollection[typedTestItem](base, typedTestCodec{})
	values := []typedTestItem{
		{ID: "a", Content: "alpha"},
		{ID: "b", Content: "bravo"},
	}

	// Act
	insertErr := collection.Insert(context.Background(), values)
	upsertErr := collection.Upsert(context.Background(), values)

	// Assert
	if insertErr != nil {
		t.Fatalf("Insert: %v", insertErr)
	}
	if upsertErr != nil {
		t.Fatalf("Upsert: %v", upsertErr)
	}
	for name, records := range map[string][]Record{"insert": base.inserted, "upsert": base.upserted} {
		if len(records) != 2 {
			t.Fatalf("%s records length: %d", name, len(records))
		}
		if records[0].ID != "a" || records[0].Content == nil || *records[0].Content != "alpha" {
			t.Fatalf("%s first record not encoded: %#v", name, records[0])
		}
		if records[1].ID != "b" || records[1].Content == nil || *records[1].Content != "bravo" {
			t.Fatalf("%s second record not encoded: %#v", name, records[1])
		}
	}
}

func TestTypedCollectionStopsOnEncodeError(t *testing.T) {
	// Arrange
	encodeErr := errors.New("encode failed")
	base := &typedTestCollection{}
	collection := NewTypedCollection[typedTestItem](base, typedTestCodec{encodeErr: encodeErr})

	// Act
	err := collection.Insert(context.Background(), []typedTestItem{{ID: "a"}})

	// Assert
	if !errors.Is(err, encodeErr) {
		t.Fatalf("expected encode error, got %v", err)
	}
	if len(base.inserted) != 0 {
		t.Fatalf("base Insert should not be called after encode error: %#v", base.inserted)
	}
}

func TestTypedCollectionGetAndSearchDecodeResults(t *testing.T) {
	// Arrange
	content := "alpha"
	base := &typedTestCollection{
		getRecord: Record{ID: "a", Content: &content},
		searchResults: []SearchResult{{
			Record:   Record{ID: "a", Content: &content},
			Distance: 0.25,
			Score:    0.75,
		}},
	}
	collection := NewTypedCollection[typedTestItem](base, typedTestCodec{})
	opts := SearchOptions{Filter: Eq(Column("id"), "a")}

	// Act
	got, getErr := collection.Get(context.Background(), "a")
	results, searchErr := collection.SearchByVector(context.Background(), []float32{1, 0}, 3, opts)

	// Assert
	if getErr != nil {
		t.Fatalf("Get: %v", getErr)
	}
	if got != (typedTestItem{ID: "a", Content: "alpha"}) {
		t.Fatalf("unexpected Get item: %#v", got)
	}
	if base.getID != "a" {
		t.Fatalf("get ID not forwarded: %q", base.getID)
	}
	if searchErr != nil {
		t.Fatalf("SearchByVector: %v", searchErr)
	}
	if !reflect.DeepEqual(base.searchVector, []float32{1, 0}) {
		t.Fatalf("search vector not forwarded: %#v", base.searchVector)
	}
	if base.searchTopK != 3 {
		t.Fatalf("search topK not forwarded: %d", base.searchTopK)
	}
	if base.searchOptions.Filter == nil {
		t.Fatal("search options not forwarded")
	}
	if len(results) != 1 || results[0].Item != (typedTestItem{ID: "a", Content: "alpha"}) {
		t.Fatalf("unexpected search results: %#v", results)
	}
	if results[0].Distance != 0.25 || results[0].Score != 0.75 {
		t.Fatalf("ranking metrics not preserved: %#v", results[0])
	}
}

func TestTypedCollectionPropagatesBaseAndDecodeErrors(t *testing.T) {
	// Arrange
	baseErr := errors.New("base failed")
	decodeErr := errors.New("decode failed")

	// Act
	_, getBaseErr := NewTypedCollection[typedTestItem](&typedTestCollection{getErr: baseErr}, typedTestCodec{}).Get(context.Background(), "missing")
	_, getDecodeErr := NewTypedCollection[typedTestItem](&typedTestCollection{getRecord: Record{ID: "a"}}, typedTestCodec{decodeErr: decodeErr}).Get(context.Background(), "a")
	_, searchBaseErr := NewTypedCollection[typedTestItem](&typedTestCollection{searchErr: baseErr}, typedTestCodec{}).SearchByVector(context.Background(), []float32{1, 0}, 1, SearchOptions{})
	_, searchDecodeErr := NewTypedCollection[typedTestItem](&typedTestCollection{searchResults: []SearchResult{{Record: Record{ID: "a"}}}}, typedTestCodec{decodeErr: decodeErr}).SearchByVector(context.Background(), []float32{1, 0}, 1, SearchOptions{})

	// Assert
	if !errors.Is(getBaseErr, baseErr) {
		t.Fatalf("expected get base error, got %v", getBaseErr)
	}
	if !errors.Is(getDecodeErr, decodeErr) {
		t.Fatalf("expected get decode error, got %v", getDecodeErr)
	}
	if !errors.Is(searchBaseErr, baseErr) {
		t.Fatalf("expected search base error, got %v", searchBaseErr)
	}
	if !errors.Is(searchDecodeErr, decodeErr) {
		t.Fatalf("expected search decode error, got %v", searchDecodeErr)
	}
}
