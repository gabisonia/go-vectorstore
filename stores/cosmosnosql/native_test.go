package cosmosnosql

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestValidationBeforeIO(t *testing.T) {
	c := NativeCollection{schema: NativeSchema{Text: "body", Vector: "direction", Dimensions: 2}}
	if err := c.Upsert(t.Context(), NativeDocument{ID: "bad/id", Vector: []float32{1, 0}}); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatal(err)
	}
	if _, err := c.Search(t.Context(), []float32{1, 0}, 0, nil); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := c.Delete(ctx, "valid"); !errors.Is(err, context.Canceled) || errors.Is(err, ErrNativeOutcomeUnknown) {
		t.Fatal(err)
	}
}
func TestWriteOutcomes(t *testing.T) {
	err := writeError(context.DeadlineExceeded)
	if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrNativeOutcomeUnknown) {
		t.Fatal(err)
	}
	reject := &azcore.ResponseError{StatusCode: 400}
	if err := writeError(reject); err != reject {
		t.Fatal(err)
	}
}
func ExampleAttachNative() {
	search := func(ctx context.Context, container *azcosmos.ContainerClient) ([]NativeHit, error) {
		c, err := AttachNative(ctx, container, NativeSchema{Text: "body", Vector: "direction", Dimensions: 2})
		if err != nil {
			return nil, err
		}
		return c.Search(ctx, []float32{1, 0}, 5, nil)
	}
	_ = search
}

func TestProvisionedPolicies(t *testing.T) {
	s := NativeSchema{Text: "body", Vector: "direction", Dimensions: 2}
	creation := azcosmos.ContainerProperties{ID: "articles", PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{"/id"}},
		VectorEmbeddingPolicy: &azcosmos.VectorEmbeddingPolicy{VectorEmbeddings: []azcosmos.VectorEmbedding{{Path: "/direction", Dimensions: 2, DataType: "float32", DistanceFunction: "cosine"}}},
		IndexingPolicy:        &azcosmos.IndexingPolicy{VectorIndexes: []azcosmos.VectorIndex{{Path: "/direction", Type: "diskANN"}}}}
	observed := creation
	if err := s.checkPolicies(&observed, nil); err != nil {
		t.Fatal(err)
	}
	observed.VectorEmbeddingPolicy = nil
	if err := s.checkPolicies(&observed, nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("missing policy accepted: %v", err)
	}
	if err := s.checkPolicies(&observed, &creation); err != nil || observed.VectorEmbeddingPolicy != nil {
		t.Fatalf("creation policy fallback: %v", err)
	}
	observed.IndexingPolicy = &azcosmos.IndexingPolicy{VectorIndexes: []azcosmos.VectorIndex{{Path: "/direction", Type: "flat"}}}
	if err := s.checkPolicies(&observed, &creation); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("observable mismatch accepted: %v", err)
	}
}

type testHitPager struct {
	pages [][][]byte
	final func() error
}

func (p *testHitPager) More() bool { return len(p.pages) > 0 || p.final != nil }
func (p *testHitPager) NextPage(context.Context) (azcosmos.QueryItemsResponse, error) {
	if len(p.pages) == 0 {
		f := p.final
		p.final = nil
		return azcosmos.QueryItemsResponse{}, f()
	}
	items := p.pages[0]
	p.pages = p.pages[1:]
	return azcosmos.QueryItemsResponse{Items: items}, nil
}
func TestExactSearchAcrossPages(t *testing.T) {
	pages := [][][]byte{
		{[]byte(`{"id":"z","score":0}`), []byte(`{"id":"y","score":0}`)}, {},
		{[]byte(`{"id":"a","score":0}`), []byte(`{"id":"b","score":1}`), []byte(`{"id":"c","score":-1}`)},
	}
	for _, tc := range []struct {
		top  int
		want []NativeHit
	}{
		{1, []NativeHit{{"b", 1}}}, {2, []NativeHit{{"b", 1}, {"a", 0}}}, {100, []NativeHit{{"b", 1}, {"a", 0}, {"y", 0}, {"z", 0}, {"c", -1}}},
	} {
		got, err := collectHits(t.Context(), &testHitPager{pages: pages}, tc.top, true)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Fatalf("top %d: got %v, %v; want %v", tc.top, got, err, tc.want)
		}
	}
}
func TestExactSearchDiscardsIncompleteResults(t *testing.T) {
	valid := [][]byte{[]byte(`{"id":"best","score":1}`)}
	late := errors.New("late page failure")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	for _, tc := range []struct {
		name  string
		pager testHitPager
		err   error
	}{
		{"late error", testHitPager{[][][]byte{valid}, func() error { return late }}, late},
		{"missing score", testHitPager{pages: [][][]byte{valid, {[]byte(`{"id":"bad"}`)}}}, vectordata.ErrInvalidRecord},
		{"cancelled on final empty page", testHitPager{[][][]byte{valid}, func() error { cancel(); return nil }}, context.Canceled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits, err := collectHits(ctx, &tc.pager, 1, true)
			if hits != nil || !errors.Is(err, tc.err) {
				t.Fatalf("got %v, %v", hits, err)
			}
		})
	}
}
