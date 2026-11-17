package vectordata_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type singleRecordModel struct {
	ID string `vectordata:"key"`
}

// An older third-party provider only needs the original ModelCollection methods.
// These overrides let us check malformed/uncertain responses without replaying.
type singleRecordProvider struct {
	vectordata.ModelCollection
	keys                   []any
	count                  int64
	record                 vectordata.ModelRecord
	err                    error
	writes, reads, deletes int
}

func (p *singleRecordProvider) Insert(_ context.Context, rows []vectordata.ModelRecord) ([]any, error) {
	p.writes++
	if len(rows) != 1 {
		panic("expected one record")
	}
	return p.keys, p.err
}
func (p *singleRecordProvider) Upsert(ctx context.Context, rows []vectordata.ModelRecord) ([]any, error) {
	return p.Insert(ctx, rows)
}
func (p *singleRecordProvider) Get(context.Context, any, vectordata.RetrievalOptions) (vectordata.ModelRecord, error) {
	p.reads++
	return p.record, p.err
}
func (p *singleRecordProvider) Delete(_ context.Context, keys []any) (int64, error) {
	p.deletes++
	if len(keys) != 1 {
		panic("expected one key")
	}
	return p.count, p.err
}

func singleProvider(t *testing.T) *singleRecordProvider {
	t.Helper()
	base, err := memory.New().EnsureModelCollection(context.Background(), "records", vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyString}}, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return &singleRecordProvider{ModelCollection: base, keys: []any{"key"}, record: vectordata.ModelRecord{Key: "key"}}
}

type singleRecordCodec struct {
	*vectordata.StructCodec[singleRecordModel]
	encodes, decodes     int
	encodeErr, decodeErr error
}

func (c *singleRecordCodec) Encode(value singleRecordModel) (vectordata.ModelRecord, error) {
	c.encodes++
	if c.encodeErr != nil {
		return vectordata.ModelRecord{}, c.encodeErr
	}
	return c.StructCodec.Encode(value)
}
func (c *singleRecordCodec) Decode(r vectordata.ModelRecord) (singleRecordModel, error) {
	c.decodes++
	if c.decodeErr != nil {
		return singleRecordModel{ID: "partial"}, c.decodeErr
	}
	return c.StructCodec.Decode(r)
}

func TestSingleRecordProviderErrorsAndReplyValidation(t *testing.T) {
	ctx := context.Background()
	for _, typed := range []bool{false, true} {
		p := singleProvider(t)
		c, err := vectordata.NewTypedModelCollection[string, singleRecordModel](p, mustSingleCodec(t))
		if err != nil {
			t.Fatal(err)
		}
		for _, keys := range [][]any{nil, {}, {"one", "two"}, {nil}} {
			p.keys = keys
			before := p.writes
			var err error
			if typed {
				_, err = c.UpsertOne(ctx, singleRecordModel{ID: "key"})
			} else {
				_, err = vectordata.UpsertOne(ctx, p, vectordata.ModelRecord{Key: "key"})
			}
			if !errors.Is(err, vectordata.ErrInvalidRecord) || p.writes != before+1 {
				t.Fatalf("typed=%v reply=%v: %v calls=%d", typed, keys, err, p.writes-before)
			}
		}
		cause := fmt.Errorf("uncertain write: %w", context.DeadlineExceeded)
		p.keys, p.err = []any{"key"}, cause
		before := p.writes
		if typed {
			_, err = c.InsertOne(ctx, singleRecordModel{ID: "key"})
		} else {
			_, err = vectordata.InsertOne(ctx, p, vectordata.ModelRecord{Key: "key"})
		}
		if !errors.Is(err, cause) || p.writes != before+1 {
			t.Fatalf("write replayed/lost cause: %v calls=%d", err, p.writes-before)
		}
		p.err = nil
		for _, count := range []int64{-1, 2} {
			p.count = count
			if typed {
				_, err = c.DeleteOne(ctx, "key")
			} else {
				_, err = vectordata.DeleteOne(ctx, p, "key")
			}
			if !errors.Is(err, vectordata.ErrInvalidRecord) {
				t.Fatalf("count %d: %v", count, err)
			}
		}
		p.err = cause
		if typed {
			_, err = c.DeleteOne(ctx, "key")
		} else {
			_, err = vectordata.DeleteOne(ctx, p, "key")
		}
		if !errors.Is(err, cause) {
			t.Fatalf("delete error: %v", err)
		}
	}
}

func TestTypedBatchWritesRejectMalformedKeys(t *testing.T) {
	p := singleProvider(t)
	c, err := vectordata.NewTypedModelCollection[string, singleRecordModel](p, mustSingleCodec(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, write := range []func(context.Context, []singleRecordModel) ([]string, error){c.Insert, c.Upsert} {
		for _, keys := range [][]any{nil, {nil}, {(*string)(nil)}, {"one", "two"}} {
			p.keys = keys
			before := p.writes
			got, err := write(context.Background(), []singleRecordModel{{ID: "key"}})
			if !errors.Is(err, vectordata.ErrInvalidRecord) || got != nil || p.writes != before+1 {
				t.Fatalf("malformed keys %v: got %v err=%v calls=%d", keys, got, err, p.writes-before)
			}
		}
	}
}

func mustSingleCodec(t *testing.T) *vectordata.StructCodec[singleRecordModel] {
	t.Helper()
	codec, err := vectordata.NewStructCodec[singleRecordModel](nil)
	if err != nil {
		t.Fatal(err)
	}
	return codec
}

func TestTryGetSeparatesAbsenceFromCodecAndProviderFailures(t *testing.T) {
	ctx := context.Background()
	p := singleProvider(t)
	codec := &singleRecordCodec{StructCodec: mustSingleCodec(t)}
	c, err := vectordata.NewTypedModelCollection[string, singleRecordModel](p, codec)
	if err != nil {
		t.Fatal(err)
	}
	for _, cause := range []error{vectordata.ErrNotFound, vectordata.ErrCollectionNotFound, context.Canceled, errors.New("connection failed")} {
		wrapped := &vectordata.OperationError{Backend: "third-party", Collection: "records", Operation: "get", Err: fmt.Errorf("wrapped: %w", cause)}
		p.err = wrapped
		r, found, err := vectordata.TryGet(ctx, p, "key", vectordata.RetrievalOptions{})
		if found || !reflect.DeepEqual(r, vectordata.ModelRecord{}) {
			t.Fatal("partial provider value escaped")
		}
		if cause == vectordata.ErrNotFound {
			if err != nil {
				t.Fatal(err)
			}
		} else if !errors.Is(err, cause) {
			t.Fatalf("lost error: %v", err)
		}
		_, found, err = c.TryGet(ctx, "key", vectordata.RetrievalOptions{})
		if found || codec.decodes != 0 {
			t.Fatal("decoded absent/failed provider record")
		}
		if cause == vectordata.ErrNotFound {
			if err != nil {
				t.Fatal(err)
			}
		} else {
			var operation *vectordata.OperationError
			if !errors.As(err, &operation) || operation != wrapped {
				t.Fatalf("lost operation metadata: %v", err)
			}
		}
	}
	p.err = nil
	codec.decodeErr = fmt.Errorf("codec lookup failed: %w", vectordata.ErrNotFound)
	r, found, err := c.TryGet(ctx, "key", vectordata.RetrievalOptions{})
	if !errors.Is(err, codec.decodeErr) || found || r.ID != "" || codec.decodes != 1 {
		t.Fatalf("codec absence suppressed/partial value escaped: %v %v %v", r, found, err)
	}
}

func TestSingleRecordCancellationAndNilHandles(t *testing.T) {
	p := singleProvider(t)
	codec := &singleRecordCodec{StructCodec: mustSingleCodec(t)}
	c, err := vectordata.NewTypedModelCollection[string, singleRecordModel](p, codec)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checks := []func() error{
		func() error { _, e := vectordata.InsertOne(ctx, p, vectordata.ModelRecord{}); return e },
		func() error { _, e := vectordata.UpsertOne(ctx, p, vectordata.ModelRecord{}); return e },
		func() error { _, _, e := vectordata.TryGet(ctx, p, "key", vectordata.RetrievalOptions{}); return e },
		func() error { _, e := vectordata.DeleteOne(ctx, p, "key"); return e },
		func() error { _, e := c.InsertOne(ctx, singleRecordModel{}); return e },
		func() error { _, e := c.UpsertOne(ctx, singleRecordModel{}); return e },
		func() error { _, _, e := c.TryGet(ctx, "key", vectordata.RetrievalOptions{}); return e },
		func() error { _, e := c.DeleteOne(ctx, "key"); return e },
	}
	for _, check := range checks {
		if err := check(); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if p.writes+p.reads+p.deletes+codec.encodes+codec.decodes != 0 {
		t.Fatal("canceled operation called provider or codec")
	}
	var nilProvider *singleRecordProvider
	for _, p := range []vectordata.ModelCollection{nil, nilProvider} {
		if _, err := vectordata.InsertOne(context.Background(), p, vectordata.ModelRecord{}); !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatal(err)
		}
		if _, err := vectordata.UpsertOne(context.Background(), p, vectordata.ModelRecord{}); !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatal(err)
		}
		if _, _, err := vectordata.TryGet(context.Background(), p, "key", vectordata.RetrievalOptions{}); !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatal(err)
		}
		if _, err := vectordata.DeleteOne(context.Background(), p, "key"); !errors.Is(err, vectordata.ErrSchemaMismatch) {
			t.Fatal(err)
		}
	}
	var nilTyped *vectordata.TypedModelCollection[string, singleRecordModel]
	if _, _, err := nilTyped.TryGet(context.Background(), "key", vectordata.RetrievalOptions{}); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatal(err)
	}
	codec.encodeErr = errors.New("encoding failed")
	if _, err := c.UpsertOne(context.Background(), singleRecordModel{ID: "key"}); !errors.Is(err, codec.encodeErr) || p.writes != 0 {
		t.Fatalf("encoding failure: %v", err)
	}
}
