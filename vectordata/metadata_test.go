package vectordata_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/stores/postgres"
	"github.com/gabisonia/go-vectorstore/stores/sqlite"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5/pgxpool"
)

type metadataDocument struct {
	ID string `vectordata:"key"`
}

func TestMetadataDiscoveryAfterClose(t *testing.T) {
	ctx := context.Background()
	// Pool creation is lazy. Closing it before discovery proves no database
	// round trip is needed, including when constructing collection handles.
	pool, err := pgxpool.New(ctx, "postgres://private_user:private_password@127.0.0.1:1/research?application_name=private_option")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	pg, err := postgres.NewVectorStore(pool, postgres.StoreOptions{Schema: `tenant"one`})
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	path := filepath.Join(t.TempDir(), "literal?mode=memory#x.db")
	sq, err := sqlite.Open(ctx, path, sqlite.StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := sq.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		store vectordata.ModelStore
		want  vectordata.StoreMetadata
	}{
		{memory.New(), vectordata.StoreMetadata{SystemName: "memory"}},
		{sq, vectordata.StoreMetadata{SystemName: "sqlite", DatabaseName: path}},
		{pg, vectordata.StoreMetadata{SystemName: "postgresql", DatabaseName: "research", Namespace: `tenant"one`}},
	} {
		t.Run(tt.want.SystemName, func(t *testing.T) {
			if got := vectordata.GetStoreMetadata(tt.store); got != tt.want {
				t.Fatalf("metadata: %+v", got)
			}
			c, err := vectordata.GetTypedModelCollection[string, metadataDocument](tt.store, `docs"one`, nil, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			want := vectordata.CollectionMetadata{StoreMetadata: tt.want, CollectionName: `docs"one`}
			got := c.CollectionMetadata()
			if got != want {
				t.Fatalf("typed collection metadata: %+v", got)
			}
			got.DatabaseName = "changed"
			if c.CollectionMetadata() != want {
				t.Fatal("caller mutation changed provider identity")
			}
		})
	}
	// The original Record API and its typed wrapper expose the same identity.
	base := pg.Collection("records", 2, vectordata.DistanceCosine)
	want := vectordata.CollectionMetadata{StoreMetadata: pg.StoreMetadata(), CollectionName: "records"}
	typed := vectordata.NewTypedCollection[struct{}](base, nil)
	if vectordata.GetCollectionMetadata(base) != want || typed.CollectionMetadata() != want {
		t.Fatal("Record metadata lost")
	}
}

func TestMetadataFallbacks(t *testing.T) {
	store := memory.New()
	// These wrappers deliberately expose only the old interfaces.
	legacyStore := struct{ vectordata.ModelStore }{store}
	if _, ok := any(legacyStore).(vectordata.StoreMetadataProvider); ok {
		t.Fatal("metadata became mandatory")
	}
	if got := vectordata.GetStoreMetadata(legacyStore); got != (vectordata.StoreMetadata{}) {
		t.Fatalf("invented identity: %+v", got)
	}
	base, err := store.GetModelCollection("legacy", vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	legacyCollection := struct{ vectordata.ModelCollection }{base}
	if _, ok := any(legacyCollection).(vectordata.CollectionMetadataProvider); ok {
		t.Fatal("metadata became mandatory")
	}
	want := vectordata.CollectionMetadata{CollectionName: "legacy"}
	if got := vectordata.GetCollectionMetadata(legacyCollection); got != want {
		t.Fatalf("fallback: %+v", got)
	}
	// A codec with nil embedded methods panics if discovery invokes it.
	codec := struct {
		vectordata.ModelCodec[metadataDocument]
	}{}
	typed, err := vectordata.NewTypedModelCollection[string, metadataDocument](legacyCollection, codec)
	if err != nil {
		t.Fatal(err)
	}
	if got := typed.CollectionMetadata(); got != want {
		t.Fatalf("typed fallback: %+v", got)
	}
	var nilStore *memory.Store
	var nilCollection *vectordata.TypedModelCollection[string, metadataDocument]
	for _, s := range []any{nil, nilStore, 42} {
		if vectordata.GetStoreMetadata(s) != (vectordata.StoreMetadata{}) {
			t.Fatal("nil/unsupported store metadata")
		}
	}
	if vectordata.GetCollectionMetadata(nil) != (vectordata.CollectionMetadata{}) || vectordata.GetCollectionMetadata(nilCollection) != (vectordata.CollectionMetadata{}) {
		t.Fatal("nil collection metadata")
	}
}

func TestSQLiteMetadataFilename(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cwd, filepath.Join(t.TempDir(), "relative.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{":memory:", relative} {
		t.Run(path, func(t *testing.T) {
			s, err := sqlite.Open(context.Background(), path, sqlite.StoreOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			want := path
			if path != ":memory:" {
				want, err = filepath.Abs(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			if got := s.StoreMetadata(); got.SystemName != "sqlite" || got.DatabaseName != want || got.Namespace != "" {
				t.Fatalf("filename metadata: %+v", got)
			}
		})
	}
}
