//go:build referenceparity

package cosmosparity

import (
	"crypto/tls"
	"errors"
	"math"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/gabisonia/go-vectorstore/stores/cosmosnosql"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestSharedContainer(t *testing.T) {
	ctx := t.Context()
	cred, err := azcosmos.NewKeyCredential(os.Getenv("COSMOS_EMULATOR_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	opts := azcosmos.ClientOptions{}
	// Only the loopback gateway in this disposable emulator uses a self-signed certificate.
	opts.Transport = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	client, err := azcosmos.NewClientWithKey("https://localhost:8081", cred, &opts)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: "parity"}, nil); err != nil {
		t.Fatal(err)
	}
	db, err := client.NewDatabase("parity")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"csharp", "gocreated"} {
		t.Run(name, func(t *testing.T) {
			ref := func(phase string) {
				t.Helper()
				if out, err := exec.CommandContext(ctx, "dotnet", "Reference.dll", phase, name).CombinedOutput(); err != nil {
					t.Fatalf("C# %s: %v\n%s", phase, err, out)
				}
			}

			raw, err := db.NewContainer(name)
			if err != nil {
				t.Fatal(err)
			}
			properties := azcosmos.ContainerProperties{ID: name, PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{"/id"}},
				VectorEmbeddingPolicy: &azcosmos.VectorEmbeddingPolicy{VectorEmbeddings: []azcosmos.VectorEmbedding{{Path: "/direction", Dimensions: 2, DistanceFunction: "cosine", DataType: "float32"}}},
				IndexingPolicy:        &azcosmos.IndexingPolicy{Automatic: true, IndexingMode: "consistent", IncludedPaths: []azcosmos.IncludedPath{{Path: "/*"}}, VectorIndexes: []azcosmos.VectorIndex{{Path: "/direction", Type: "diskANN"}}}}
			if name == "csharp" {
				ref("create")
			} else {
				_, err := db.CreateContainer(ctx, properties, nil)
				if err != nil {
					t.Fatal(err)
				}
			}
			ref("seed")
			schema := cosmosnosql.NativeSchema{Text: "body", Vector: "direction", Dimensions: 2}
			wrong := schema
			wrong.Dimensions++
			if _, err := cosmosnosql.AttachProvisioned(ctx, raw, wrong, properties); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("dimension mismatch: %v", err)
			}
			c, err := cosmosnosql.AttachProvisioned(ctx, raw, schema, properties)
			if err != nil {
				t.Fatal(err)
			}
			doc, err := c.Get(ctx, "shared")
			if err != nil || doc.Text != "initial" || !slices.Equal(doc.Vector, []float32{-1, 0}) {
				t.Fatalf("seed: %+v %v", doc, err)
			}
			doc.Text = "keep"
			doc.Vector = []float32{4, 3}
			if err := c.Upsert(ctx, doc); err != nil {
				t.Fatal(err)
			}
			hits, err := c.Search(ctx, []float32{1, 0}, 3, &doc.Text)
			if err != nil || len(hits) != 2 || hits[0].ID != "shared" || hits[1].ID != "second" || math.Abs(hits[0].Score-0.8) > 1e-5 || math.Abs(hits[1].Score-(0)) > 1e-5 {
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
