package vectordata_test

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type contextCodec struct {
	encode func(vectordata.ModelRecord) (vectordata.ModelRecord, error)
	decode func(vectordata.ModelRecord) (vectordata.ModelRecord, error)
}

func (c contextCodec) Encode(r vectordata.ModelRecord) (vectordata.ModelRecord, error) {
	if c.encode != nil {
		return c.encode(r)
	}
	return r, nil
}
func (c contextCodec) Decode(r vectordata.ModelRecord) (vectordata.ModelRecord, error) {
	if c.decode != nil {
		return c.decode(r)
	}
	return r, nil
}

// The read fixture deliberately ignores context, as an older provider can do.
// The typed adapter must guard its own callbacks and close source iterators.
type contextReadProvider struct {
	vectordata.ModelCollection
	loads, closed int
	onLoad        func()
}

func (p *contextReadProvider) records() []vectordata.ModelRecord {
	p.loads++
	if p.onLoad != nil {
		p.onLoad()
	}
	return []vectordata.ModelRecord{{Key: "a"}, {Key: "b"}}
}
func (p *contextReadProvider) Get(context.Context, any, vectordata.RetrievalOptions) (vectordata.ModelRecord, error) {
	return p.records()[0], nil
}
func (p *contextReadProvider) GetMany(context.Context, []any, vectordata.RetrievalOptions) ([]vectordata.ModelRecord, error) {
	return p.records(), nil
}
func (p *contextReadProvider) Query(context.Context, vectordata.Filter, int, vectordata.QueryOptions) ([]vectordata.ModelRecord, error) {
	return p.records(), nil
}
func (p *contextReadProvider) QueryIter(context.Context, vectordata.Filter, int, vectordata.QueryOptions) iter.Seq2[vectordata.ModelRecord, error] {
	return func(yield func(vectordata.ModelRecord, error) bool) {
		defer func() { p.closed++ }()
		for _, r := range p.records() {
			if !yield(r, nil) {
				return
			}
		}
	}
}
func (p *contextReadProvider) results() ([]vectordata.ModelSearchResult, error) {
	rows := p.records()
	return []vectordata.ModelSearchResult{{Record: rows[0], Distance: 2, Score: 3, ScoreKind: vectordata.ScoreRRF}, {Record: rows[1], Distance: 4, Score: 5, ScoreKind: vectordata.ScoreRRF}}, nil
}
func (p *contextReadProvider) sequence() iter.Seq2[vectordata.ModelSearchResult, error] {
	return func(yield func(vectordata.ModelSearchResult, error) bool) {
		defer func() { p.closed++ }()
		rows, _ := p.results()
		for _, r := range rows {
			if !yield(r, nil) {
				return
			}
		}
	}
}
func (p *contextReadProvider) Search(context.Context, []float32, int, vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return p.results()
}
func (p *contextReadProvider) SearchText(context.Context, string, int, vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return p.results()
}
func (p *contextReadProvider) SearchInput(context.Context, any, int, vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return p.results()
}
func (p *contextReadProvider) SearchBinary(context.Context, vectordata.BinaryVector, int, vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return p.results()
}
func (p *contextReadProvider) SearchSparse(context.Context, vectordata.SparseVector, int, vectordata.ModelSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return p.results()
}
func (p *contextReadProvider) SearchIter(context.Context, []float32, int, vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return p.sequence()
}
func (p *contextReadProvider) SearchBinaryIter(context.Context, vectordata.BinaryVector, int, vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return p.sequence()
}
func (p *contextReadProvider) SearchSparseIter(context.Context, vectordata.SparseVector, int, vectordata.ModelSearchOptions) iter.Seq2[vectordata.ModelSearchResult, error] {
	return p.sequence()
}
func (p *contextReadProvider) HybridSearch(context.Context, []float32, string, int, vectordata.HybridSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return p.results()
}
func (p *contextReadProvider) HybridSearchText(context.Context, string, string, int, vectordata.HybridSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return p.results()
}
func (p *contextReadProvider) HybridSearchInput(context.Context, any, string, int, vectordata.HybridSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return p.results()
}
func (p *contextReadProvider) HybridSearchBinary(context.Context, vectordata.BinaryVector, string, int, vectordata.HybridSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return p.results()
}
func (p *contextReadProvider) HybridSearchSparse(context.Context, vectordata.SparseVector, string, int, vectordata.HybridSearchOptions) ([]vectordata.ModelSearchResult, error) {
	return p.results()
}

type contextTyped = vectordata.TypedModelCollection[string, vectordata.ModelRecord]
type contextHit = vectordata.TypedModelSearchResult[vectordata.ModelRecord]

func TestTypedReadCallbackCancellation(t *testing.T) {
	operations := []struct {
		name string
		read func(context.Context, *contextTyped) (int, error)
	}{
		{"get", func(ctx context.Context, c *contextTyped) (int, error) {
			r, e := c.Get(ctx, "a", vectordata.RetrievalOptions{})
			if r.Key != nil {
				return 1, e
			}
			return 0, e
		}},
		{"try_get", func(ctx context.Context, c *contextTyped) (int, error) {
			r, found, e := c.TryGet(ctx, "a", vectordata.RetrievalOptions{})
			if found || r.Key != nil {
				return 1, e
			}
			return 0, e
		}},
		{"batch", func(ctx context.Context, c *contextTyped) (int, error) {
			r, e := c.GetMany(ctx, []string{"a", "b"}, vectordata.RetrievalOptions{})
			return len(r), e
		}},
		{"query", func(ctx context.Context, c *contextTyped) (int, error) {
			r, e := c.Query(ctx, nil, 2, vectordata.QueryOptions{})
			return len(r), e
		}},
		{"dense", func(ctx context.Context, c *contextTyped) (int, error) {
			r, e := c.Search(ctx, nil, 2, vectordata.ModelSearchOptions{})
			return len(r), e
		}},
		{"text", func(ctx context.Context, c *contextTyped) (int, error) {
			r, e := c.SearchText(ctx, "a", 2, vectordata.ModelSearchOptions{})
			return len(r), e
		}},
		{"input", func(ctx context.Context, c *contextTyped) (int, error) {
			r, e := c.SearchInput(ctx, "a", 2, vectordata.ModelSearchOptions{})
			return len(r), e
		}},
		{"binary", func(ctx context.Context, c *contextTyped) (int, error) {
			r, e := c.SearchBinary(ctx, vectordata.BinaryVector{}, 2, vectordata.ModelSearchOptions{})
			return len(r), e
		}},
		{"sparse", func(ctx context.Context, c *contextTyped) (int, error) {
			r, e := c.SearchSparse(ctx, vectordata.SparseVector{}, 2, vectordata.ModelSearchOptions{})
			return len(r), e
		}},
		{"hybrid", func(ctx context.Context, c *contextTyped) (int, error) {
			r, e := c.HybridSearch(ctx, nil, "a", 2, vectordata.HybridSearchOptions{})
			return len(r), e
		}},
		{"hybrid_text", func(ctx context.Context, c *contextTyped) (int, error) {
			r, e := c.HybridSearchText(ctx, "a", "a", 2, vectordata.HybridSearchOptions{})
			return len(r), e
		}},
		{"hybrid_input", func(ctx context.Context, c *contextTyped) (int, error) {
			r, e := c.HybridSearchInput(ctx, "a", "a", 2, vectordata.HybridSearchOptions{})
			return len(r), e
		}},
		{"hybrid_binary", func(ctx context.Context, c *contextTyped) (int, error) {
			r, e := c.HybridSearchBinary(ctx, vectordata.BinaryVector{}, "a", 2, vectordata.HybridSearchOptions{})
			return len(r), e
		}},
		{"hybrid_sparse", func(ctx context.Context, c *contextTyped) (int, error) {
			r, e := c.HybridSearchSparse(ctx, vectordata.SparseVector{}, "a", 2, vectordata.HybridSearchOptions{})
			return len(r), e
		}},
	}
	for _, op := range operations {
		for _, point := range []string{"before", "provider", "codec", "codec_error"} {
			t.Run(op.name+"/"+point, func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				p := &contextReadProvider{ModelCollection: contextCollection(t)}
				calls := 0
				cause := errors.New("codec failed")
				codec := contextCodec{decode: func(r vectordata.ModelRecord) (vectordata.ModelRecord, error) {
					calls++
					cancel()
					if point == "codec_error" {
						return r, cause
					}
					return r, nil
				}}
				c, e := vectordata.NewTypedModelCollection[string, vectordata.ModelRecord](p, codec)
				if e != nil {
					t.Fatal(e)
				}
				if point == "before" {
					cancel()
				}
				if point == "provider" {
					p.onLoad = cancel
				}
				count, err := op.read(ctx, c)
				wantCalls, wantLoads := 1, 1
				if point == "before" {
					wantCalls, wantLoads = 0, 0
				}
				if point == "provider" {
					wantCalls = 0
				}
				if count != 0 || !errors.Is(err, context.Canceled) || calls != wantCalls || p.loads != wantLoads {
					t.Fatalf("values=%d err=%v decodes=%d loads=%d", count, err, calls, p.loads)
				}
				if point == "codec_error" && !errors.Is(err, cause) {
					t.Fatalf("lost codec cause: %v", err)
				}
			})
		}
	}
}

func TestTypedIteratorCallbackCancellation(t *testing.T) {
	operations := []struct {
		name     string
		sequence func(context.Context, *contextTyped) iter.Seq2[contextHit, error]
		native   bool
	}{
		{"dense", func(ctx context.Context, c *contextTyped) iter.Seq2[contextHit, error] {
			return c.SearchIter(ctx, nil, 2, vectordata.ModelSearchOptions{})
		}, true},
		{"binary", func(ctx context.Context, c *contextTyped) iter.Seq2[contextHit, error] {
			return c.SearchBinaryIter(ctx, vectordata.BinaryVector{}, 2, vectordata.ModelSearchOptions{})
		}, true},
		{"sparse", func(ctx context.Context, c *contextTyped) iter.Seq2[contextHit, error] {
			return c.SearchSparseIter(ctx, vectordata.SparseVector{}, 2, vectordata.ModelSearchOptions{})
		}, true},
		{"text", func(ctx context.Context, c *contextTyped) iter.Seq2[contextHit, error] {
			return c.SearchTextIter(ctx, "a", 2, vectordata.ModelSearchOptions{})
		}, false},
		{"input", func(ctx context.Context, c *contextTyped) iter.Seq2[contextHit, error] {
			return c.SearchInputIter(ctx, "a", 2, vectordata.ModelSearchOptions{})
		}, false},
		{"hybrid", func(ctx context.Context, c *contextTyped) iter.Seq2[contextHit, error] {
			return c.HybridSearchIter(ctx, nil, "a", 2, vectordata.HybridSearchOptions{})
		}, false},
		{"hybrid_text", func(ctx context.Context, c *contextTyped) iter.Seq2[contextHit, error] {
			return c.HybridSearchTextIter(ctx, "a", "a", 2, vectordata.HybridSearchOptions{})
		}, false},
		{"hybrid_input", func(ctx context.Context, c *contextTyped) iter.Seq2[contextHit, error] {
			return c.HybridSearchInputIter(ctx, "a", "a", 2, vectordata.HybridSearchOptions{})
		}, false},
		{"hybrid_binary", func(ctx context.Context, c *contextTyped) iter.Seq2[contextHit, error] {
			return c.HybridSearchBinaryIter(ctx, vectordata.BinaryVector{}, "a", 2, vectordata.HybridSearchOptions{})
		}, false},
		{"hybrid_sparse", func(ctx context.Context, c *contextTyped) iter.Seq2[contextHit, error] {
			return c.HybridSearchSparseIter(ctx, vectordata.SparseVector{}, "a", 2, vectordata.HybridSearchOptions{})
		}, false},
	}
	for _, op := range operations {
		t.Run(op.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			p := &contextReadProvider{ModelCollection: contextCollection(t)}
			calls := 0
			cause := errors.New("decoder failed")
			c, e := vectordata.NewTypedModelCollection[string, vectordata.ModelRecord](p, contextCodec{decode: func(r vectordata.ModelRecord) (vectordata.ModelRecord, error) { calls++; cancel(); return r, cause }})
			if e != nil {
				t.Fatal(e)
			}
			seq := op.sequence(ctx, c)
			if p.loads != 0 {
				t.Fatal("iterator loaded eagerly")
			}
			failures := 0
			for hit, err := range seq {
				if err == nil || !errors.Is(err, context.Canceled) || !errors.Is(err, cause) || hit.Item.Key != nil {
					t.Fatalf("partial result/lost error: %+v %v", hit, err)
				}
				failures++
			}
			if calls != 1 || failures != 1 || op.native && p.closed != 1 {
				t.Fatalf("decodes=%d failures=%d closed=%d", calls, failures, p.closed)
			}
		})
	}
	for _, batch := range []bool{false, true} {
		t.Run(map[bool]string{false: "query", true: "batch"}[batch], func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			p := &contextReadProvider{ModelCollection: contextCollection(t)}
			calls := 0
			c, e := vectordata.NewTypedModelCollection[string, vectordata.ModelRecord](p, contextCodec{decode: func(r vectordata.ModelRecord) (vectordata.ModelRecord, error) { calls++; cancel(); return r, nil }})
			if e != nil {
				t.Fatal(e)
			}
			seq := c.QueryIter(ctx, nil, 2, vectordata.QueryOptions{})
			if batch {
				seq = c.GetManyIter(ctx, []string{"a", "b"}, vectordata.RetrievalOptions{})
			}
			failures := 0
			for r, err := range seq {
				if !errors.Is(err, context.Canceled) || r.Key != nil {
					t.Fatalf("partial result: %+v %v", r, err)
				}
				failures++
			}
			if calls != 1 || failures != 1 || !batch && p.closed != 1 {
				t.Fatalf("decodes=%d failures=%d closed=%d", calls, failures, p.closed)
			}
		})
	}
}

func TestTypedWritePreservesCodecFailureAndConfirmedSuccess(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	cause := errors.New("encoder failed")
	p := &integerWriteSpy{ModelCollection: contextCollection(t)}
	c, err := vectordata.NewTypedModelCollection[string, vectordata.ModelRecord](p, contextCodec{encode: func(r vectordata.ModelRecord) (vectordata.ModelRecord, error) { cancel(); return r, cause }})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Insert(ctx, []vectordata.ModelRecord{{Key: "a"}})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, cause) || p.calls != 0 {
		t.Fatalf("lost failure/wrote after cancellation: %v calls=%d", err, p.calls)
	}
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	confirmed := &confirmedContextWrite{ModelCollection: contextCollection(t), cancel: cancel}
	c, err = vectordata.NewTypedModelCollection[string, vectordata.ModelRecord](confirmed, contextCodec{})
	if err != nil {
		t.Fatal(err)
	}
	keys, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "a"}})
	if err != nil || len(keys) != 1 || keys[0] != "a" || ctx.Err() == nil {
		t.Fatalf("confirmed write was reclassified: %v, %v", keys, err)
	}
}

type confirmedContextWrite struct {
	vectordata.ModelCollection
	cancel context.CancelFunc
}

func (p *confirmedContextWrite) Insert(ctx context.Context, rows []vectordata.ModelRecord) ([]any, error) {
	keys, err := p.ModelCollection.Insert(ctx, rows)
	p.cancel()
	return keys, err
}

func contextCollection(t *testing.T) vectordata.ModelCollection {
	t.Helper()
	c, err := memory.New().EnsureModelCollection(t.Context(), "codec_context", vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTypedWriteStopsCanceledCodecBatch(t *testing.T) {
	for _, operation := range []string{"insert", "upsert"} {
		for _, before := range []bool{false, true} {
			t.Run(operation+map[bool]string{false: "/during", true: "/before"}[before], func(t *testing.T) {
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				calls := 0
				p := &integerWriteSpy{ModelCollection: contextCollection(t)}
				c, err := vectordata.NewTypedModelCollection[string, vectordata.ModelRecord](p, contextCodec{encode: func(r vectordata.ModelRecord) (vectordata.ModelRecord, error) { calls++; cancel(); return r, nil }})
				if err != nil {
					t.Fatal(err)
				}
				if before {
					cancel()
				}
				write := c.Insert
				if operation == "upsert" {
					write = c.Upsert
				}
				keys, err := write(ctx, []vectordata.ModelRecord{{Key: "a"}, {Key: "b"}})
				wantCalls := 1
				if before {
					wantCalls = 0
				}
				if !errors.Is(err, context.Canceled) || keys != nil || calls != wantCalls || p.calls != 0 {
					t.Fatalf("keys=%v err=%v encodes=%d writes=%d", keys, err, calls, p.calls)
				}
			})
		}
	}
}

func TestTypedReadStopsCanceledCodecBatch(t *testing.T) {
	base := contextCollection(t)
	if _, err := base.Insert(t.Context(), []vectordata.ModelRecord{{Key: "a"}, {Key: "b"}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	c, err := vectordata.NewTypedModelCollection[string, vectordata.ModelRecord](base, contextCodec{decode: func(r vectordata.ModelRecord) (vectordata.ModelRecord, error) { calls++; cancel(); return r, nil }})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := c.Query(ctx, nil, 2, vectordata.QueryOptions{})
	if !errors.Is(err, context.Canceled) || rows != nil || calls != 1 {
		t.Fatalf("rows=%v err=%v decodes=%d", rows, err, calls)
	}
}

func TestTypedCodecExpiredDeadline(t *testing.T) {
	ctx, cancel := context.WithDeadline(t.Context(), time.Time{})
	defer cancel()
	p := &contextReadProvider{ModelCollection: contextCollection(t)}
	calls := 0
	codec := contextCodec{encode: func(r vectordata.ModelRecord) (vectordata.ModelRecord, error) { calls++; return r, nil }, decode: func(r vectordata.ModelRecord) (vectordata.ModelRecord, error) { calls++; return r, nil }}
	c, err := vectordata.NewTypedModelCollection[string, vectordata.ModelRecord](p, codec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "a"}}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := c.Get(ctx, "a", vectordata.RetrievalOptions{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if calls != 0 || p.loads != 0 {
		t.Fatalf("expired request invoked callbacks or reads: %d, %d", calls, p.loads)
	}
}
