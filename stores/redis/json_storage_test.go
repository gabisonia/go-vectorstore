package redis

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/gabisonia/go-vectorstore/internal/modelcodec"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestJSONConfigurationAndCanonicalProjection(t *testing.T) {
	if s, err := New(StoreOptions{StorageMode: StorageJSON, NativeIndexes: true}); !errors.Is(err, vectordata.ErrUnsupported) {
		if s != nil {
			s.Close()
		}
		t.Fatalf("native JSON indexes accepted: %v", err)
	}
	s, err := New(StoreOptions{StorageMode: StorageJSON, DB: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := fixtureCollection(t, s)
	var state snapshot
	if err := json.Unmarshal(fixtureBytes(t), &state); err != nil {
		t.Fatal(err)
	}
	m, raw, rows, err := c.projectJSON(context.Background(), state)
	if err != nil || len(rows) != 1 || m.Format != 3 || m.Next != math.MaxInt64 {
		t.Fatalf("JSON projection: %+v %v", m, err)
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(rows[0].document), &fields); err != nil {
		t.Fatal(err)
	}
	for name, value := range fields {
		if _, ok := value.(string); !ok {
			t.Fatalf("%s entered RedisJSON numeric parsing: %T", name, value)
		}
	}
	r, err := modelcodec.Decode([]byte(fields["__vs_payload"].(string)), c.definition)
	if err != nil || r.Key != int64(math.MinInt64) || r.Data["JSON"].(map[string]any)["number"] != json.Number("18446744073709551615") {
		t.Fatalf("canonical precision: %+v %v", r, err)
	}
	caps := s.Capabilities()
	if len(caps.VectorIndexMethods) != 0 || len(caps.IndexedDataTypes) != 0 || caps.FullTextIndexing || caps.HybridSearch || caps.MigrationPlanning {
		t.Fatalf("JSON foundation advertised indexes/migrations: %+v", caps)
	}
	s.maxBytes = len(raw)
	if _, _, _, err := c.projectJSON(context.Background(), state); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatalf("JSON byte budget ignored: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Count(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("JSON cancellation lost: %v", err)
	}
}
