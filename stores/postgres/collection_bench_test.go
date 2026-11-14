package postgres

import (
	"fmt"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func benchmarkRecords(rows, dimension int) []vectordata.Record {
	records := make([]vectordata.Record, rows)
	for i := range records {
		vector := make([]float32, dimension)
		for j := range vector {
			vector[j] = float32((i+j)%997+1) / 997
		}
		records[i] = vectordata.Record{ID: fmt.Sprint(i), Vector: vector, Metadata: map[string]any{"category": "benchmark", "rank": i}}
	}
	return records
}

func BenchmarkVectorLiteral(b *testing.B) {
	for _, dimension := range []int{128, 768, 1536} {
		b.Run(fmt.Sprint(dimension), func(b *testing.B) {
			vector := benchmarkRecords(1, dimension)[0].Vector
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				_ = vectorLiteral(vector)
			}
		})
	}
}

func BenchmarkBuildWriteBatch(b *testing.B) {
	for _, dimension := range []int{128, 1536} {
		for _, rows := range []int{1, 500} {
			b.Run(fmt.Sprintf("dim%d/rows%d", dimension, rows), func(b *testing.B) {
				records := benchmarkRecords(rows, dimension)
				collection := testCollection()
				collection.dimension = dimension
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					if _, _, err := collection.buildWriteBatch(records, writeModeUpsert); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
