package vectordata_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type benchmarkModel struct {
	ID      string         `vectordata:"key"`
	Title   string         `vectordata:"data"`
	Tags    []string       `vectordata:"data"`
	Details map[string]any `vectordata:"data"`
	Vector  []float32      `vectordata:"vector,dimensions=128"`
}

func BenchmarkStructCodec(b *testing.B) {
	for _, dimensions := range []int{128, 1536} {
		b.Run(fmt.Sprintf("dim%d", dimensions), func(b *testing.B) {
			d, err := vectordata.DefinitionFor[benchmarkModel]()
			if err != nil {
				b.Fatal(err)
			}
			d.Vectors[0].Dimensions = dimensions
			codec, err := vectordata.NewStructCodec[benchmarkModel](&d)
			if err != nil {
				b.Fatal(err)
			}
			model := benchmarkModel{ID: "record", Title: "quiet mountain hotel", Tags: []string{"news", "quiet"}, Details: map[string]any{"rank": 42}, Vector: make([]float32, dimensions)}
			model.Vector[0] = 1
			record, err := codec.Encode(model)
			if err != nil {
				b.Fatal(err)
			}
			b.Run("encode", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, err := codec.Encode(model); err != nil {
						b.Fatal(err)
					}
				}
			})
			b.Run("decode", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					if _, err := codec.Decode(record); err != nil {
						b.Fatal(err)
					}
				}
			})
		})
	}
}

func BenchmarkPrepareModelRecords(b *testing.B) {
	for _, dimensions := range []int{128, 1536} {
		b.Run(fmt.Sprintf("dim%d/rows100", dimensions), func(b *testing.B) {
			d, err := vectordata.DefinitionFor[benchmarkModel]()
			if err != nil {
				b.Fatal(err)
			}
			d.Vectors[0].Dimensions = dimensions
			d, err = d.Normalize()
			if err != nil {
				b.Fatal(err)
			}
			records := make([]vectordata.ModelRecord, 100)
			for i := range records {
				vector := make([]float32, dimensions)
				vector[0] = 1
				records[i] = vectordata.ModelRecord{Key: fmt.Sprint(i), Data: map[string]any{"Title": "quiet mountain hotel", "Tags": []string{"news", "quiet"}, "Details": map[string]any{"rank": 42}}, Vectors: map[string][]float32{"Vector": vector}}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := vectordata.PrepareModelRecords(context.Background(), d, vectordata.ModelCollectionOptions{}, records); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
