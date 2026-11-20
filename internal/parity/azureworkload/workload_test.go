//go:build azureworkload

// Package azureworkload measures filtered native search on disposable Azure services.
package azureworkload

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math"
	mathrand "math/rand"
	"net/http"
	"os"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/data/azcosmos"
	"github.com/gabisonia/go-vectorstore/stores/cosmosnosql"
	"github.com/gabisonia/go-vectorstore/stores/documentdb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const dimensions = 128

type hit = cosmosnosql.NativeHit
type row = cosmosnosql.NativeDocument

// Cancel a marked search only after the gateway has supplied a continuation.
type cancelContinuation struct {
	cancel context.CancelFunc
	calls  atomic.Int32
}
type cancelProbeKey struct{}
type continuationPolicy struct{}

func (continuationPolicy) Do(req *policy.Request) (*http.Response, error) {
	probe, _ := req.Raw().Context().Value(cancelProbeKey{}).(*cancelContinuation)
	if probe != nil && req.Raw().Header.Get("x-ms-continuation") != "" {
		probe.calls.Add(1)
		probe.cancel()
		return nil, context.Canceled
	}
	return req.Next()
}
func TestCosmosExact(t *testing.T) {
	endpoint := os.Getenv("COSMOS_WORKLOAD_ENDPOINT")
	if endpoint == "" {
		t.Skip("set COSMOS_WORKLOAD_ENDPOINT and COSMOS_WORKLOAD_KEY for a disposable two-partition account")
	}
	cred, err := azcosmos.NewKeyCredential(os.Getenv("COSMOS_WORKLOAD_KEY"))
	if err != nil {
		t.Fatal(err)
	}
	opts := azcosmos.ClientOptions{}
	opts.PerCallPolicies = []policy.Policy{continuationPolicy{}}
	client, err := azcosmos.NewClientWithKey(endpoint, cred, &opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	name := "workload" + rand.Text()
	db, err := client.NewDatabase(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = client.CreateDatabase(ctx, azcosmos.DatabaseProperties{ID: name}, nil); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := db.Delete(cleanup, nil); err != nil {
			t.Errorf("cleanup %s: %v", name, err)
		}
	}()
	ru := azcosmos.NewManualThroughputProperties(11000)
	props := azcosmos.ContainerProperties{ID: "items", PartitionKeyDefinition: azcosmos.PartitionKeyDefinition{Paths: []string{"/id"}},
		VectorEmbeddingPolicy: &azcosmos.VectorEmbeddingPolicy{VectorEmbeddings: []azcosmos.VectorEmbedding{{Path: "/direction", Dimensions: dimensions, DistanceFunction: "cosine", DataType: "float32"}}},
		IndexingPolicy:        &azcosmos.IndexingPolicy{Automatic: true, IndexingMode: "consistent", IncludedPaths: []azcosmos.IncludedPath{{Path: "/*"}}, ExcludedPaths: []azcosmos.ExcludedPath{{Path: "/direction/*"}}, VectorIndexes: []azcosmos.VectorIndex{{Path: "/direction", Type: "diskANN"}}}}
	if _, err = db.CreateContainer(ctx, props, &azcosmos.CreateContainerOptions{ThroughputProperties: &ru}); err != nil {
		t.Fatal(err)
	}
	raw, err := db.NewContainer("items")
	if err != nil {
		t.Fatal(err)
	}
	ranges, err := raw.ReadFeedRanges(ctx, nil)
	if err != nil || len(ranges) < 2 {
		t.Fatalf("physical ranges %d: %v", len(ranges), err)
	}
	c, err := cosmosnosql.AttachNative(ctx, raw, cosmosnosql.NativeSchema{Text: "body", Vector: "direction", Dimensions: dimensions})
	if err != nil {
		t.Fatal(err)
	}
	measure(t, c.Upsert, c.SearchExact, true)
	query := make([]float32, dimensions)
	query[0] = 1
	pageCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	probe := &cancelContinuation{cancel: cancel}
	hits, err := c.SearchExact(context.WithValue(pageCtx, cancelProbeKey{}, probe), query, 10, nil)
	if !errors.Is(err, context.Canceled) || hits != nil || probe.calls.Load() != 1 {
		t.Fatalf("cancel on continuation: %v, %v, calls=%d", hits, err, probe.calls.Load())
	}
}

func TestDocumentDBHNSW(t *testing.T) {
	uri := os.Getenv("DOCUMENTDB_WORKLOAD_URI")
	if uri == "" {
		t.Skip("set DOCUMENTDB_WORKLOAD_URI for a disposable cluster")
	}
	ctx := t.Context()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	db := client.Database("workload" + rand.Text())
	if err := db.CreateCollection(ctx, "items"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := db.Drop(cleanup); err != nil {
			t.Errorf("cleanup %s: %v", db.Name(), err)
		}
	}()
	indexes := bson.A{bson.M{"name": "direction_", "key": bson.M{"direction": "cosmosSearch"}, "cosmosSearchOptions": bson.M{"kind": "vector-hnsw", "dimensions": dimensions, "similarity": "COS"}}, bson.M{"name": "body_", "key": bson.M{"body": 1}}}
	if err := db.RunCommand(ctx, bson.D{{Key: "createIndexes", Value: "items"}, {Key: "indexes", Value: indexes}}).Err(); err != nil {
		t.Fatal(err)
	}
	c, err := documentdb.AttachNative(ctx, db.Collection("items"), documentdb.NativeSchema{Text: "body", Vector: "direction", Index: "direction_", Dimensions: dimensions})
	if err != nil {
		t.Fatal(err)
	}
	measure(t, func(ctx context.Context, d row) error { return c.Upsert(ctx, documentdb.NativeDocument(d)) }, func(ctx context.Context, v []float32, k int, s *string) ([]hit, error) {
		hs, err := c.Search(ctx, v, k, s)
		out := make([]hit, len(hs))
		for i, h := range hs {
			out[i] = hit(h)
		}
		return out, err
	}, false)
}

func cosine(x, y []float32) float64 {
	var dot, a, b float64
	for i, val := range x {
		u, v := float64(val), float64(y[i])
		dot += u * v
		a += u * u
		b += v * v
	}
	return dot / math.Sqrt(a*b)
}

func measure(t *testing.T, upsert func(context.Context, row) error, search func(context.Context, []float32, int, *string) ([]hit, error), exact bool) {
	t.Helper()
	ctx := t.Context()
	rng := mathrand.New(mathrand.NewSource(4192))
	nextVector := func() []float32 {
		v := make([]float32, dimensions)
		for i := range v {
			v[i] = float32(rng.NormFloat64())
		}
		return v
	}
	const total = 1024
	const requests = 60
	const k = 10
	rows := make([]row, total)
	for i := range rows {
		text := "keep"
		if i%2 == 0 {
			text = "drop"
		}
		rows[i] = row{ID: fmt.Sprintf("r%04d", i), Text: text, Vector: nextVector()}
	}
	var workers sync.WaitGroup
	for worker := range 8 {
		workers.Go(func() {
			for i := worker; i < total; i += 8 {
				if err := upsert(ctx, rows[i]); err != nil {
					t.Errorf("seed %s: %v", rows[i].ID, err)
					return
				}
			}
		})
	}
	workers.Wait()
	if t.Failed() {
		return
	}
	var latencies [requests]time.Duration
	var matches [requests]int
	start := time.Now()
	for i := range requests {
		query := nextVector()
		var reference []hit
		for _, r := range rows {
			if r.Text == "keep" {
				reference = append(reference, hit{ID: r.ID, Score: cosine(query, r.Vector)})
			}
		}
		slices.SortFunc(reference, func(a, b hit) int {
			if order := cmp.Compare(b.Score, a.Score); order != 0 {
				return order
			}
			return cmp.Compare(a.ID, b.ID)
		})
		due := start.Add(time.Duration(i) * 500 * time.Millisecond)
		time.Sleep(time.Until(due))
		workers.Go(func() {
			filter := "keep"
			got, err := search(ctx, query, k, &filter)
			latencies[i] = time.Since(due)
			if err != nil || len(got) != k {
				t.Errorf("query %d: %d hits, %v", i, len(got), err)
				return
			}
			seen := map[string]bool{}
			for j, h := range got {
				if seen[h.ID] {
					t.Errorf("query %d: duplicate %s", i, h.ID)
					return
				}
				seen[h.ID] = true
				if exact && (h.ID != reference[j].ID || math.Abs(h.Score-reference[j].Score) > 1e-5) {
					t.Errorf("query %d rank %d: got %v, want %v", i, j, h, reference[j])
					return
				}
				for _, want := range reference[:k] {
					if want.ID == h.ID {
						matches[i]++
					}
				}
			}
		})
	}
	workers.Wait()
	slices.Sort(latencies[:])
	p95 := latencies[(95*requests+99)/100-1]
	sum := 0
	for _, n := range matches {
		sum += n
	}
	recall := float64(sum) / (requests * k)
	t.Logf("records=%d dimensions=%d queries=%d offered_qps=2 p95=%v recall@%d=%.4f", total, dimensions, requests, p95, k, recall)
	if p95 > time.Second || recall < 0.95 || (exact && recall != 1) {
		t.Error("workload missed acceptance criteria")
	}
}
