package conformance

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type codecContextModel struct {
	ID     int64     `vectordata:"key,generated"`
	Body   string    `vectordata:"data"`
	Vector []float32 `vectordata:"vector,dimensions=2,metric=l2,source=Body"`
}

type cancelingModelCodec struct {
	*vectordata.StructCodec[codecContextModel]
	onEncode, onDecode func()
	encodes, decodes   int
}

func (c *cancelingModelCodec) Encode(v codecContextModel) (vectordata.ModelRecord, error) {
	c.encodes++
	if c.onEncode != nil {
		c.onEncode()
	}
	return c.StructCodec.Encode(v)
}
func (c *cancelingModelCodec) Decode(r vectordata.ModelRecord) (codecContextModel, error) {
	c.decodes++
	if c.onDecode != nil {
		c.onDecode()
	}
	return c.StructCodec.Decode(r)
}

func runTypedCodecContext(t *testing.T, factory Factory) {
	ctx := t.Context()
	plain, err := vectordata.NewStructCodec[codecContextModel](nil)
	if err != nil {
		t.Fatal(err)
	}
	embeds := 0
	embedder := vectordata.EmbeddingFunc(func(ctx context.Context, texts []string) ([][]float32, error) {
		embeds++
		vectors := make([][]float32, len(texts))
		for i := range vectors {
			vectors[i] = []float32{1, 0}
		}
		return vectors, ctx.Err()
	})
	base, err := factory(t).EnsureModelCollection(ctx, "codec_context", plain.Definition(), vectordata.ModelCollectionOptions{Embedder: embedder})
	if err != nil {
		t.Fatal(err)
	}
	codec := &cancelingModelCodec{StructCodec: plain}
	c, err := vectordata.NewTypedModelCollection[int64, codecContextModel](base, codec)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Insert(ctx, []codecContextModel{{Body: "seed"}}); err != nil {
		t.Fatal(err)
	}
	writeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	codec.onEncode = cancel
	codec.encodes = 0
	embeds = 0
	keys, err := c.Insert(writeCtx, []codecContextModel{{Body: "first"}, {Body: "second"}})
	if keys != nil || !errors.Is(err, context.Canceled) || codec.encodes != 1 || embeds != 0 {
		t.Fatalf("canceled batch: keys=%v error=%v encodes=%d embeddings=%d", keys, err, codec.encodes, embeds)
	}
	codec.onEncode = nil
	if count, err := c.Count(ctx, nil); err != nil || count != 1 {
		t.Fatalf("canceled batch changed storage: %d, %v", count, err)
	}
	keys, err = c.Insert(ctx, []codecContextModel{{Body: "after"}})
	if err != nil || len(keys) != 1 || keys[0] != 2 {
		t.Fatalf("canceled batch consumed generated keys: %v, %v", keys, err)
	}
	for _, iterator := range []bool{false, true} {
		readCtx, cancel := context.WithCancel(ctx)
		codec.onDecode = cancel
		codec.decodes = 0
		if iterator {
			failures := 0
			for row, err := range c.QueryIter(readCtx, nil, 2, vectordata.QueryOptions{}) {
				if !errors.Is(err, context.Canceled) || row.ID != 0 {
					t.Fatalf("canceled iterator result: %+v, %v", row, err)
				}
				failures++
			}
			if failures != 1 {
				t.Fatalf("canceled iterator errors: %d", failures)
			}
		} else {
			rows, err := c.Query(readCtx, nil, 2, vectordata.QueryOptions{})
			if rows != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled read returned partial data: %+v, %v", rows, err)
			}
		}
		cancel()
		codec.onDecode = nil
		if codec.decodes != 1 {
			t.Fatalf("decoded after cancellation: %d", codec.decodes)
		}
		check, cancelCheck := context.WithTimeout(ctx, 2*time.Second)
		count, err := c.Count(check, nil)
		cancelCheck()
		if err != nil || count != 2 {
			t.Fatalf("operation after canceled decode: %d, %v", count, err)
		}
	}
}
