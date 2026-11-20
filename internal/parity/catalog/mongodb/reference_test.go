//go:build referenceparity

package mongodbparity

import (
	"errors"
	"math"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/stores/mongodb"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func TestSharedCollection(t *testing.T) {
	client, err := mongo.Connect(options.Client().ApplyURI("mongodb://localhost:27017/?directConnection=true"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(t.Context())
	db := client.Database("parity")
	for _, name := range []string{"parity_go", "parity_csharp"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			ref := func(phase string) {
				t.Helper()
				if out, err := exec.CommandContext(ctx, "dotnet", "Reference.dll", phase, name).CombinedOutput(); err != nil {
					t.Fatalf("C# %s: %s %v", phase, out, err)
				}
			}
			coll := db.Collection(name)
			if name == "parity_go" {
				if err := db.CreateCollection(ctx, name); err != nil {
					t.Fatal(err)
				}
				fields := bson.A{bson.M{"type": "vector", "path": "direction", "numDimensions": 2, "similarity": "cosine"}, bson.M{"type": "filter", "path": "body"}}
				_, err := coll.SearchIndexes().CreateOne(ctx, mongo.SearchIndexModel{Definition: bson.M{"fields": fields}, Options: options.SearchIndexes().SetName("vector_index").SetType("vectorSearch")})
				if err != nil {
					t.Fatal(err)
				}
			} else {
				ref("create")
			}
			ref("seed")
			s := mongodb.NativeSchema{Index: "vector_index", Text: "body", Vector: "direction", Dimensions: 2}
			var c *mongodb.NativeCollection
			for deadline := time.Now().Add(90 * time.Second); ; {
				c, err = mongodb.AttachNative(ctx, coll, s)
				if err == nil {
					break
				}
				if !errors.Is(err, vectordata.ErrSchemaMismatch) || time.Now().After(deadline) {
					t.Fatal(err)
				}
				time.Sleep(time.Second)
			}
			wrong := s
			wrong.Dimensions++
			if _, err := mongodb.AttachNative(ctx, coll, wrong); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("dimension mismatch: %v", err)
			}
			seed, err := c.Get(ctx, "seed")
			if err != nil || seed.Text != "keep" || !slices.Equal(seed.Vector, []float32{0, 1}) {
				t.Fatalf("C# seed: %+v %v", seed, err)
			}
			point := mongodb.NativeDocument{ID: "go", Text: "initial", Vector: []float32{0, 1}}
			if err := c.Upsert(ctx, point); err != nil {
				t.Fatal(err)
			}
			point.Text, point.Vector = "keep", []float32{1, 0}
			if err := c.Upsert(ctx, point); err != nil {
				t.Fatal(err)
			}
			for deadline := time.Now().Add(90 * time.Second); ; {
				hits, err := c.Search(ctx, point.Vector, 3, &point.Text)
				if err != nil {
					t.Fatal(err)
				}
				if len(hits) == 2 && hits[0].ID == "go" && hits[1].ID == "seed" && math.Abs(hits[0].Score-1) < 1e-5 && math.Abs(hits[1].Score-0.5) < 1e-5 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("filtered ranking: %+v", hits)
				}
				time.Sleep(time.Second)
			}
			ref("verify")
			doc, err := c.Get(ctx, "go")
			if err != nil || doc.Text != "edited" {
				t.Fatalf("C# update: %+v %v", doc, err)
			}
			if _, err = c.Get(ctx, "seed"); !errors.Is(err, vectordata.ErrNotFound) {
				t.Fatalf("C# deletion: %v", err)
			}
			if err := c.Delete(ctx, "go"); err != nil {
				t.Fatal(err)
			}
			ref("verify-delete")
		})
	}
}
