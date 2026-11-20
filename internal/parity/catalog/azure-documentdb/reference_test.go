//go:build referenceparity

package documentdbparity

import (
	"errors"
	"math"
	"os"
	"os/exec"
	"slices"
	"testing"

	"github.com/gabisonia/go-vectorstore/stores/documentdb"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestSharedCollection(t *testing.T) {
	ctx := t.Context()
	uri := os.Getenv("DOCUMENTDB_TEST_URI")
	if uri == "" {
		t.Fatal("missing DocumentDB fixture URI")
	}
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	for _, name := range []string{"csharp", "gocreated"} {
		t.Run(name, func(t *testing.T) {
			ref := func(phase string) {
				t.Helper()
				if out, err := exec.CommandContext(ctx, "dotnet", "Reference.dll", phase, name).CombinedOutput(); err != nil {
					t.Fatalf("C# %s: %s %v", phase, out, err)
				}
			}

			db := client.Database("parity")
			raw := db.Collection(name)
			if name == "csharp" {
				ref("create")
			} else {
				if err := db.CreateCollection(ctx, name); err != nil {
					t.Fatal(err)
				}
				err := db.RunCommand(ctx, bson.D{{Key: "createIndexes", Value: name}, {Key: "indexes", Value: bson.A{
					bson.M{"name": "direction_", "key": bson.M{"direction": "cosmosSearch"}, "cosmosSearchOptions": bson.M{"kind": "vector-hnsw", "dimensions": 2, "similarity": "COS"}},
					bson.M{"name": "body_", "key": bson.M{"body": 1}}}}}).Err()
				if err != nil {
					t.Fatal(err)
				}
			}
			ref("seed")
			schema := documentdb.NativeSchema{Text: "body", Vector: "direction", Dimensions: 2, Index: "direction_"}
			wrong := schema
			wrong.Dimensions++
			if _, err := documentdb.AttachNative(ctx, raw, wrong); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("dimension mismatch: %v", err)
			}
			c, err := documentdb.AttachNative(ctx, raw, schema)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := c.Get(ctx, "shared")
			if err != nil || doc.Text != "reference" || !slices.Equal(doc.Vector, []float32{0, 1}) {
				t.Fatalf("seed: %+v %v", doc, err)
			}
			doc.Text = "from Go"
			doc.Vector = []float32{1, 0}
			if err := c.Upsert(ctx, doc); err != nil {
				t.Fatal(err)
			}
			hits, err := c.Search(ctx, []float32{1, 0}, 3, &doc.Text)
			if err != nil || len(hits) != 2 || hits[0].ID != "shared" || hits[1].ID != "second" || math.Abs(hits[0].Score-1) > 1e-5 || math.Abs(hits[1].Score-(-1)) > 1e-5 {
				t.Fatalf("filtered cosine hits: %+v %v", hits, err)
			}
			ref("verify")
			if _, err := c.Get(ctx, "shared"); !errors.Is(err, vectordata.ErrNotFound) {
				t.Fatalf("C# delete: %v", err)
			}
			if err := c.Delete(ctx, "second"); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Get(ctx, "second"); !errors.Is(err, vectordata.ErrNotFound) {
				t.Fatalf("Go delete: %v", err)
			}
		})
	}
}
