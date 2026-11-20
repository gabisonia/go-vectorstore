//go:build referenceparity

package cosmosparity

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
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
	opts.Transport = faultTransport{&http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}}
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
			t.Run("write_outcomes", func(t *testing.T) { checkWriteOutcomes(t, c) })
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
			exact, err := c.SearchExact(ctx, []float32{1, 0}, 2, &doc.Text)
			if err != nil || !slices.Equal(exact, hits) {
				t.Fatalf("filtered exact search: %+v, %v; ranked: %+v", exact, err, hits)
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

// The fault applies only to the marked write. Point reads and SDK metadata
// requests use the real emulator connection.
type faultKey struct{}
type writeFault struct {
	method     string
	status     int
	attempts   atomic.Int32
	afterApply func() error
}
type faultTransport struct{ client *http.Client }

var errLostReply = errors.New("fixture discarded successful write reply")

func (f faultTransport) Do(req *http.Request) (*http.Response, error) {
	fault, _ := req.Context().Value(faultKey{}).(*writeFault)
	if fault == nil || req.Method != fault.method {
		return f.client.Do(req)
	}
	if fault.attempts.Add(1) > 1 {
		return nil, errors.New("fixture observed a replay")
	}
	if fault.status != 0 {
		return &http.Response{Request: req, StatusCode: fault.status, Status: http.StatusText(fault.status), Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"message":"injected gateway response"}`))}, nil
	}
	resp, err := f.client.Do(req)
	if err != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return resp, err
	}
	resp.Body.Close()
	if fault.afterApply != nil {
		if err := fault.afterApply(); err != nil {
			return nil, err
		}
	}
	return nil, errLostReply
}

func checkWriteOutcomes(t *testing.T, c *cosmosnosql.NativeCollection) {
	ctx := t.Context()
	doc := cosmosnosql.NativeDocument{ID: "outcome", Text: "fault fixture", Vector: []float32{0, 1}}
	throttle := &writeFault{method: http.MethodPost, status: http.StatusTooManyRequests}
	err := c.Upsert(context.WithValue(ctx, faultKey{}, throttle), doc)
	var rejection *azcore.ResponseError
	if !errors.As(err, &rejection) || rejection.StatusCode != 429 || errors.Is(err, cosmosnosql.ErrNativeOutcomeUnknown) || throttle.attempts.Load() != 1 {
		t.Fatalf("throttled upsert: %v (%d attempts)", err, throttle.attempts.Load())
	}
	if _, err := c.Get(ctx, doc.ID); !errors.Is(err, vectordata.ErrNotFound) {
		t.Fatalf("after throttle: %v", err)
	}
	timeout := &writeFault{method: http.MethodPost, status: http.StatusRequestTimeout}
	err = c.Upsert(context.WithValue(ctx, faultKey{}, timeout), doc)
	if !errors.As(err, &rejection) || rejection.StatusCode != 408 || !errors.Is(err, cosmosnosql.ErrNativeOutcomeUnknown) || timeout.attempts.Load() != 1 {
		t.Fatalf("gateway timeout: %v (%d attempts)", err, timeout.attempts.Load())
	}
	lost := &writeFault{method: http.MethodPost}
	err = c.Upsert(context.WithValue(ctx, faultKey{}, lost), doc)
	if !errors.Is(err, cosmosnosql.ErrNativeOutcomeUnknown) || !errors.Is(err, errLostReply) || lost.attempts.Load() != 1 {
		t.Fatalf("lost upsert reply: %v (%d attempts)", err, lost.attempts.Load())
	}
	got, err := c.Get(ctx, doc.ID)
	if err != nil || got.ID != doc.ID || got.Text != doc.Text || !slices.Equal(got.Vector, doc.Vector) {
		t.Fatalf("after lost upsert reply: %+v %v", got, err)
	}
	lost = &writeFault{method: http.MethodDelete}
	err = c.Delete(context.WithValue(ctx, faultKey{}, lost), doc.ID)
	if !errors.Is(err, cosmosnosql.ErrNativeOutcomeUnknown) || !errors.Is(err, errLostReply) || lost.attempts.Load() != 1 {
		t.Fatalf("lost delete reply: %v (%d attempts)", err, lost.attempts.Load())
	}
	if _, err := c.Get(ctx, doc.ID); !errors.Is(err, vectordata.ErrNotFound) {
		t.Fatalf("after lost delete reply: %v", err)
	}
	// The first writer still awaits its response while a second writer updates
	// the same ID. Replaying the first request would erase the acknowledged update.
	newer := doc
	newer.Text = "superseding writer"
	overlap := &writeFault{method: http.MethodPost, afterApply: func() error { return c.Upsert(ctx, newer) }}
	err = c.Upsert(context.WithValue(ctx, faultKey{}, overlap), doc)
	if !errors.Is(err, cosmosnosql.ErrNativeOutcomeUnknown) || !errors.Is(err, errLostReply) || overlap.attempts.Load() != 1 {
		t.Fatalf("superseded unknown write: %v (%d attempts)", err, overlap.attempts.Load())
	}
	got, err = c.Get(ctx, doc.ID)
	if err != nil || got.Text != newer.Text {
		t.Fatalf("superseding write: %+v %v", got, err)
	}
	if err := c.Delete(ctx, doc.ID); err != nil {
		t.Fatal(err)
	}

}
