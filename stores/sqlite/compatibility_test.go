package sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestReleasedFormat1Compatibility(t *testing.T) {
	read := func(name string) []byte {
		t.Helper()
		data, err := os.ReadFile(filepath.Join("testdata", "v0.4.0", name))
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var manifest struct {
		Release string
		Format  int
		SHA256  string
	}
	if err := json.Unmarshal(read("manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	database := read("format1.db")
	if manifest.Release != "v0.4.0" || manifest.Format != 1 || manifest.SHA256 != fmt.Sprintf("%x", sha256.Sum256(database)) {
		t.Fatal("released fixture provenance/checksum changed")
	}
	var fixture struct {
		Definitions map[string]vectordata.CollectionDefinition
		Records     map[string][]json.RawMessage
	}
	if err := json.Unmarshal(read("records.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "released.db")
	if err := os.WriteFile(path, database, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store, err := Open(ctx, path, StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	for name, d := range fixture.Definitions {
		t.Run(name, func(t *testing.T) {
			c, err := store.EnsureModelCollection(ctx, name, d, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			rows, err := c.Query(ctx, nil, 10, vectordata.QueryOptions{IncludeVectors: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != len(fixture.Records[name]) {
				t.Fatalf("record count: %d", len(rows))
			}
			for i, row := range rows {
				got, err := json.Marshal(row)
				if err != nil {
					t.Fatal(err)
				}
				var want bytes.Buffer
				if err := json.Compact(&want, fixture.Records[name][i]); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(got, want.Bytes()) {
					t.Fatalf("released record changed:\ngot %s\nwant %s", got, &want)
				}
			}
			if name == "native" {
				if _, ok := rows[1].Data["JSON"].(vectordata.JSONNull); !ok || rows[1].Data["SQLJSON"] != nil {
					t.Fatal("JSON null/SQL NULL distinction lost")
				}
				if len(rows[1].Vectors) != 0 || len(rows[1].BinaryVectors) != 0 || len(rows[1].SparseVectors) != 0 {
					t.Fatal("absent optional vectors became present")
				}
				keys, err := c.Insert(ctx, []vectordata.ModelRecord{{}})
				if err != nil || len(keys) != 1 || keys[0] != int64(3) {
					t.Fatalf("released generated sequence: %v %v", keys, err)
				}
			}
		})
	}
}
