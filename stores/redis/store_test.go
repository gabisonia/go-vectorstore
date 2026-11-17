package redis

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/internal/modelcodec"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestLocalConfigurationAndCapabilities(t *testing.T) {
	for _, opts := range []StoreOptions{{DB: -1}, {Namespace: "bad\x00name"}, {MaxCollectionBytes: -1}, {MaxCollectionBytes: 257 << 20}, {EmbeddingBatchSize: -1}} {
		if s, err := New(opts); err == nil {
			s.Close()
			t.Fatalf("accepted invalid options: %+v", opts)
		}
	}
	tlsConfig := &tls.Config{ServerName: "example.test", MinVersion: tls.VersionTLS12}
	s, err := New(StoreOptions{Addr: "127.0.0.1:1", Username: "secret-user", Password: "secret", Namespace: "test[*]:雪", DB: 2, TLSConfig: tlsConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.client.Options().MaxRetries != 0 || !s.client.Options().ContextTimeoutEnabled {
		t.Fatal("unsafe client retry/context configuration")
	}
	tlsConfig.ServerName = "changed"
	if s.client.Options().TLSConfig.ServerName != "example.test" {
		t.Fatal("TLS config alias")
	}
	caps := s.Capabilities()
	if caps.Backend != "redis" || caps.HybridSearch || caps.FullTextIndexing || len(caps.VectorIndexMethods) > 0 || len(caps.IndexedDataTypes) > 0 || len(caps.VectorKinds) != 1 || caps.VectorKinds[0] != vectordata.VectorDense {
		t.Fatalf("unsupported capabilities: %+v", caps)
	}
	caps.DataTypes[0] = "changed"
	if s.Capabilities().DataTypes[0] == "changed" {
		t.Fatal("capability alias")
	}
	definition := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2}}}
	c, err := s.GetModelCollection("quoted' 雪", definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got := vectordata.GetCollectionMetadata(c); got.SystemName != "redis" || got.DatabaseName != "2" || got.Namespace != "test[*]:雪" {
		t.Fatalf("metadata: %+v", got)
	}
	definition.Vectors[0].Kind = vectordata.VectorBinary
	definition.Vectors[0].Metric = vectordata.DistanceHamming
	if _, err := s.GetModelCollection("bits", definition, vectordata.ModelCollectionOptions{}); !errors.Is(err, vectordata.ErrUnsupported) {
		t.Fatalf("binary definition: %v", err)
	}
	if s.key("a:b") == s.key("a") || s.prefix == "test[*]:雪" {
		t.Fatal("unescaped key namespace")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Count(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled count: %v", err)
	}
}

func TestSnapshotRoundTripPreservesPrecision(t *testing.T) {
	s, err := New(StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyUint64}, Data: []vectordata.DataProperty{{Name: "JSON", Type: vectordata.TypeJSON}, {Name: "Int", Type: vectordata.TypeInt64}, {Name: "Null", Type: vectordata.TypeJSON, Optional: true}}}
	base, err := s.GetModelCollection("test", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := base.(*Collection)
	rows, err := vectordata.PrepareModelRecords(context.Background(), c.definition, vectordata.ModelCollectionOptions{}, []vectordata.ModelRecord{{Key: uint64(math.MaxUint64), Data: map[string]any{"JSON": vectordata.JSONNull{}, "Null": nil, "Int": int64(math.MinInt64)}}})
	if err != nil {
		t.Fatal(err)
	}
	payload, err := modelcodec.Encode(rows[0])
	if err != nil {
		t.Fatal(err)
	}
	raw, err := c.encode(snapshot{Format: 1, Definition: c.registration, Next: math.MaxInt64, Records: map[string]json.RawMessage{token(rows[0].Key): payload}})
	if err != nil {
		t.Fatal(err)
	}
	var state snapshot
	if err = json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	got, err := c.decode(context.Background(), state, nil)
	if err != nil || !reflect.DeepEqual(got, rows) || state.Next != math.MaxInt64 {
		t.Fatalf("roundtrip: %+v %v", got, err)
	}
}
