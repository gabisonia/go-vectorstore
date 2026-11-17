//go:build qdrantprobe

package qdrantprobe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/internal/exactsearch"
	"github.com/gabisonia/go-vectorstore/internal/modelcodec"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestPinnedServer(t *testing.T) {
	var info struct{ Version, Commit string }
	status, err := request(t.Context(), client(), "GET", "/", nil, &info)
	if err != nil || status != 200 || info.Version != version {
		t.Fatalf("revisit probe/design for server version %q (want %q): HTTP %d: %v", info.Version, version, status, err)
	}
	t.Logf("Qdrant %s commit %s", info.Version, info.Commit)
}

func TestLifecycleRegistrationAndNativeIDs(t *testing.T) {
	definition, err := (vectordata.CollectionDefinition{
		Key:     vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyUint64},
		Vectors: []vectordata.VectorProperty{{Name: "Text", Dimensions: 2, Metric: vectordata.DistanceCosine}, {Name: "Image", Dimensions: 2, Optional: true, Metric: vectordata.DistanceL2}},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	registration, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	config := vectorConfig("Cosine")
	config["metadata"] = map[string]any{"probe_format": 1, "definition": string(registration)}
	config["vectors"].(map[string]any)["image"] = map[string]any{"size": 2, "distance": "Euclid"}
	path := collection(t, config)
	info := call[struct {
		Config struct {
			Metadata map[string]json.RawMessage `json:"metadata"`
			Params   struct {
				Vectors map[string]struct {
					Size     int
					Distance string
				}
			} `json:"params"`
		} `json:"config"`
	}](t, "GET", path, nil)
	var stored string
	if err := json.Unmarshal(info.Config.Metadata["definition"], &stored); err != nil || stored != string(registration) {
		t.Fatalf("registration: %q, %v", stored, err)
	}
	if info.Config.Params.Vectors["v"].Distance != "Cosine" || info.Config.Params.Vectors["image"].Size != 2 {
		t.Fatalf("named vector config: %+v", info.Config.Params.Vectors)
	}
	listed := call[struct {
		Collections []struct{ Name string } `json:"collections"`
	}](t, "GET", "/collections", nil)
	found := false
	for _, c := range listed.Collections {
		if "/collections/"+c.Name == path {
			found = true
		}
	}
	if !found {
		t.Fatal("owned collection missing from listing")
	}
	uuid := "8f516db6-38ca-4e8f-ad79-d71b015f5419"
	ids := []any{uint64(0), uint64(9007199254740993), uint64(math.MaxInt64) + 1, uint64(math.MaxUint64), uuid}
	points := make([]map[string]any, len(ids))
	for i, id := range ids {
		points[i] = map[string]any{"id": id, "vector": map[string]any{"v": []float32{1, 0}}}
	}
	put(t, path, points, nil)
	got := retrieve(t, path, ids...)
	if len(got) != len(ids) {
		t.Fatalf("native IDs: %d rows", len(got))
	}
	seen := map[string]bool{}
	for _, p := range got {
		seen[string(p.ID)] = true
	}
	for _, id := range ids {
		raw, _ := json.Marshal(id)
		if !seen[string(raw)] {
			t.Fatalf("lost ID precision: %s", raw)
		}
	}
	for _, id := range []any{"arbitrary string", int64(-1)} {
		status, err := request(t.Context(), client(), "PUT", path+"/points?wait=true&ordering=strong", map[string]any{"points": []any{map[string]any{"id": id, "vector": map[string]any{"v": []float32{1, 0}}}}}, nil)
		if err != nil || status != 400 {
			t.Fatalf("unsupported native ID %v: HTTP %d: %v", id, status, err)
		}
	}
	if !call[bool](t, "DELETE", path, nil) {
		t.Fatal("delete returned false")
	}
	status, err := request(t.Context(), client(), "GET", path, nil, nil)
	if err != nil || status != 404 {
		t.Fatalf("deleted collection: HTTP %d: %v", status, err)
	}
}

func TestCanonicalEnvelopeAndCosineNormalization(t *testing.T) {
	d, err := (vectordata.CollectionDefinition{
		Key:     vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyString},
		Data:    []vectordata.DataProperty{{Name: "NativeNull", Type: vectordata.TypeJSON, Optional: true}, {Name: "JSONNull", Type: vectordata.TypeJSON}, {Name: "Exact", Type: vectordata.TypeJSON}},
		Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 2, Metric: vectordata.DistanceCosine}},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	records, err := vectordata.PrepareModelRecords(t.Context(), d, vectordata.ModelCollectionOptions{}, []vectordata.ModelRecord{{Key: "arbitrary-key", Data: map[string]any{
		"NativeNull": nil, "JSONNull": vectordata.JSONNull{}, "Exact": map[string]any{"large": json.Number("9007199254740993"), "decimal": json.Number("0.1234567890123456789")},
	}, Vectors: map[string][]float32{"V": {3, 4}}}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := modelcodec.Encode(records[0])
	if err != nil {
		t.Fatal(err)
	}
	path := collection(t, vectorConfig("Cosine"))
	put(t, path, []map[string]any{{"id": 1, "vector": map[string]any{"v": []float32{3, 4}}, "payload": map[string]any{"canonical": string(raw)}}}, nil)
	got := retrieve(t, path, 1)
	if len(got) != 1 {
		t.Fatal("missing point")
	}
	stored := payloadString(t, got[0], "canonical")
	if stored != string(raw) {
		t.Fatal("canonical bytes changed")
	}
	decoded, err := modelcodec.Decode([]byte(stored), d)
	if err != nil || !reflect.DeepEqual(decoded, records[0]) {
		t.Fatalf("canonical round trip: %+v, %v", decoded, err)
	}
	if reflect.DeepEqual(got[0].Vector["v"], records[0].Vectors["V"]) {
		t.Fatal("revisit cosine storage gate: native vector was not normalized")
	}
	if math.Abs(float64(got[0].Vector["v"][0])-0.6) > 1e-6 || math.Abs(float64(got[0].Vector["v"][1])-0.8) > 1e-6 {
		t.Fatalf("unexpected native vector: %v", got[0].Vector)
	}
	t.Logf("canonical vector %v; native cosine vector %v; exact JSON/null distinctions preserved in string envelope", decoded.Vectors["V"], got[0].Vector["v"])
}

func TestInsertOnlyIsNotAtomicDuplicateRejection(t *testing.T) {
	path := collection(t, vectorConfig("Euclid"))
	point := func(id int, body string) map[string]any {
		return map[string]any{"id": id, "vector": map[string]any{"v": []float32{1, 0}}, "payload": map[string]any{"body": body}}
	}
	put(t, path, []map[string]any{point(1, "original")}, nil)
	// A successful insert_only batch silently skips the duplicate and inserts
	// the other row. Portable Insert must reject the entire batch instead.
	put(t, path, []map[string]any{point(1, "replacement"), point(2, "new")}, map[string]any{"update_mode": "insert_only"})
	got := retrieve(t, path, 1, 2)
	if len(got) != 2 {
		t.Fatalf("revisit insert-only gate: %d points", len(got))
	}
	for _, p := range got {
		want := "original"
		if numericID(t, p) == 2 {
			want = "new"
		}
		if payloadString(t, p, "body") != want {
			t.Fatalf("insert-only payload: %+v", p)
		}
	}
	put(t, path, []map[string]any{point(1, "replaced")}, nil)
	if got := retrieve(t, path, 1); len(got) != 1 || payloadString(t, got[0], "body") != "replaced" {
		t.Fatal("default upsert did not replace existing ID")
	}
	t.Log("insert_only returned completed after inserting new ID and silently skipping duplicate; default upsert overwrites")
}

func TestConditionalUpdatesAndManifestCandidate(t *testing.T) {
	path := collection(t, map[string]any{"vectors": map[string]any{}, "shard_number": 1, "replication_factor": 1})
	row := func(generation string) map[string]any {
		return map[string]any{"id": 0, "vector": map[string]any{}, "payload": map[string]any{"generation": generation}}
	}
	filter := func(generation string) map[string]any {
		return map[string]any{"must": []any{map[string]any{"key": "generation", "match": map[string]any{"value": generation}}}}
	}
	extra := map[string]any{"update_filter": filter("absent")}
	// update_filter applies only to existing rows. It is not create-if-absent
	// protection: a missing manifest gets inserted despite a false filter.
	put(t, path, []map[string]any{row("base")}, extra)
	put(t, path, []map[string]any{row("wrong")}, extra)
	if got := retrieve(t, path, 0); len(got) != 1 || payloadString(t, got[0], "generation") != "base" {
		t.Fatal("conditional existing update did not skip")
	}
	missing := row("must-not-create")
	missing["id"] = 1
	put(t, path, []map[string]any{missing}, map[string]any{"update_mode": "update_only", "update_filter": filter("absent")})
	if got := retrieve(t, path, 1); len(got) != 0 {
		t.Fatal("update_only created a point")
	}
	// Two independent clients compete from the same known revision. This
	// exercises one local shard; it is not a distributed linearizability proof.
	start := make(chan struct{})
	errors := make(chan error, 2)
	var wg sync.WaitGroup
	for _, generation := range []string{"left", "right"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			var out response[struct {
				Status string `json:"status"`
			}]
			status, err := request(t.Context(), client(), "PUT", path+"/points?wait=true&ordering=strong", map[string]any{"points": []any{row(generation)}, "update_mode": "update_only", "update_filter": filter("base")}, &out)
			if err != nil || status != 200 || out.Result.Status != "completed" {
				errors <- fmt.Errorf("conditional write: HTTP %d: %v", status, err)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	got := retrieve(t, path, 0)
	if len(got) != 1 {
		t.Fatal("missing candidate manifest")
	}
	winner := payloadString(t, got[0], "generation")
	if winner != "left" && winner != "right" {
		t.Fatalf("unexpected generation %q", winner)
	}
	// Both replies say completed even though only one revision is observable.
	// A subsequent read and unique token are necessary to determine success.
	put(t, path, []map[string]any{row("stale")}, map[string]any{"update_mode": "update_only", "update_filter": filter("base")})
	if got := retrieve(t, path, 0); len(got) != 1 || payloadString(t, got[0], "generation") != winner {
		t.Fatal("stale conditional publication overwrote current generation")
	}
	t.Logf("one-shard conditional manifest candidate: %s observed; both requests returned completed, stale update skipped", winner)
}

func TestNativeL2ScoreCollapsesDistinctCanonicalDistances(t *testing.T) {
	path := collection(t, vectorConfig("Euclid"))
	put(t, path, []map[string]any{
		{"id": 2, "vector": map[string]any{"v": []float32{1, 0}}},
		{"id": 1, "vector": map[string]any{"v": []float32{1, 0.0001}}},
	}, nil)
	points := query(t, path, map[string]any{"query": []float32{0, 0}, "using": "v", "limit": 2, "params": map[string]any{"exact": true}, "with_vector": true})
	if len(points) != 2 || points[0].Score != points[1].Score {
		t.Fatalf("revisit distance gate: %+v", points)
	}
	near := exactsearch.DenseDistance([]float32{0, 0}, []float32{1, 0}, vectordata.DistanceL2)
	far := exactsearch.DenseDistance([]float32{0, 0}, []float32{1, 0.0001}, vectordata.DistanceL2)
	if near >= far {
		t.Fatalf("invalid canonical fixture: %g / %g", near, far)
	}
	t.Logf("native exact Euclid scores %.17g / %.17g; canonical distances %.17g / %.17g; bounded native candidates need a separate recall proof", points[0].Score, points[1].Score, near, far)
}

func TestFilterBeforeLimitAndMissingNamedVector(t *testing.T) {
	path := collection(t, vectorConfig("Euclid"))
	put(t, path, []map[string]any{
		{"id": 1, "vector": map[string]any{"v": []float32{0, 0}}, "payload": map[string]any{"eligible": false}},
		{"id": 2, "vector": map[string]any{"v": []float32{1, 0}}, "payload": map[string]any{"eligible": true}},
		{"id": 3, "vector": map[string]any{}, "payload": map[string]any{"eligible": true}},
	}, nil)
	filter := map[string]any{"must": []any{map[string]any{"key": "eligible", "match": map[string]any{"value": true}}}}
	points := query(t, path, map[string]any{"query": []float32{0, 0}, "using": "v", "filter": filter, "limit": 1, "params": map[string]any{"exact": true}})
	if len(points) != 1 || numericID(t, points[0]) != 2 {
		t.Fatalf("filter/optional vector: %+v", points)
	}
	if got := retrieve(t, path, 3); len(got) != 1 || len(got[0].Vector) != 0 {
		t.Fatal("missing optional vector was synthesized")
	}
}

func TestNativeNegationDiffersFromPortableMissingTruth(t *testing.T) {
	d, err := (vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyUint64}, Data: []vectordata.DataProperty{{Name: "Details", Type: vectordata.TypeJSON}}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	path := collection(t, map[string]any{"vectors": map[string]any{}})
	put(t, path, []map[string]any{
		{"id": 1, "vector": map[string]any{}, "payload": map[string]any{"Details": map[string]any{"x": 1}}},
		{"id": 2, "vector": map[string]any{}, "payload": map[string]any{"Details": map[string]any{"x": 2}}},
		{"id": 3, "vector": map[string]any{}, "payload": map[string]any{"Details": map[string]any{}}},
	}, nil)
	filter := map[string]any{"must_not": []any{map[string]any{"key": "Details.x", "match": map[string]any{"value": 1}}}}
	points := query(t, path, map[string]any{"filter": filter, "limit": 10})
	var ids []uint64
	for _, p := range points {
		ids = append(ids, numericID(t, p))
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []uint64{2, 3}) {
		t.Fatalf("revisit missing truth gate: %v", ids)
	}
	portable := vectordata.Not(vectordata.Eq(vectordata.DataPath("Details", "x"), 1))
	match, err := vectordata.MatchModelFilter(portable, vectordata.ModelRecord{Key: uint64(3), Data: map[string]any{"Details": map[string]any{}}}, d)
	if err != nil || match {
		t.Fatalf("portable missing comparison: %v, %v", match, err)
	}
	t.Log("native must_not returns the missing path; portable NOT preserves unknown and excludes it")
}

func TestScrollPagesDoNotPinACollectionSnapshot(t *testing.T) {
	path := collection(t, map[string]any{"vectors": map[string]any{}})
	row := func(id int, generation string) map[string]any {
		return map[string]any{"id": id, "vector": map[string]any{}, "payload": map[string]any{"generation": generation}}
	}
	put(t, path, []map[string]any{row(1, "old"), row(2, "old")}, nil)
	type page struct {
		Points []point         `json:"points"`
		Next   json.RawMessage `json:"next_page_offset"`
	}
	first := call[page](t, "POST", path+"/points/scroll", map[string]any{"limit": 1, "with_payload": true})
	if len(first.Points) != 1 || numericID(t, first.Points[0]) != 1 || payloadString(t, first.Points[0], "generation") != "old" || len(first.Next) == 0 || string(first.Next) == "null" {
		t.Fatal("unexpected first scroll page")
	}
	put(t, path, []map[string]any{row(1, "new"), row(2, "new")}, nil)
	second := call[page](t, "POST", path+"/points/scroll", map[string]any{"limit": 1, "with_payload": true, "offset": first.Next})
	if len(second.Points) != 1 || numericID(t, second.Points[0]) != 2 || payloadString(t, second.Points[0], "generation") != "new" {
		t.Fatal("revisit scroll snapshot gate")
	}
	t.Log("one enumeration saw old ID1 then new ID2 across an acknowledged write; a continuation offset is not a snapshot")
}

func TestAliasActionsDoNotCompareThePreviousTarget(t *testing.T) {
	a := collection(t, map[string]any{"vectors": map[string]any{}})
	b := collection(t, map[string]any{"vectors": map[string]any{}})
	c := collection(t, map[string]any{"vectors": map[string]any{}})
	name := strings.TrimPrefix(a, "/collections/") + "_alias"
	change := func(target string) {
		t.Helper()
		if !call[bool](t, "POST", "/collections/aliases", map[string]any{"actions": []any{
			map[string]any{"delete_alias": map[string]any{"alias_name": name}},
			map[string]any{"create_alias": map[string]any{"alias_name": name, "collection_name": strings.TrimPrefix(target, "/collections/")}},
		}}) {
			t.Fatal("alias change returned false")
		}
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		status, err := request(ctx, client(), "POST", "/collections/aliases", map[string]any{"actions": []any{map[string]any{"delete_alias": map[string]any{"alias_name": name}}}}, nil)
		if err != nil || status != 200 {
			t.Errorf("cleanup owned alias: HTTP %d: %v", status, err)
		}
	})
	change(a)
	// Two writers derived their replacement from A. B publishes first, then
	// C's stale publication succeeds without checking that A is still current.
	change(b)
	change(c)
	aliases := call[struct {
		Aliases []struct {
			Alias      string `json:"alias_name"`
			Collection string `json:"collection_name"`
		} `json:"aliases"`
	}](t, "GET", "/aliases", nil)
	for _, alias := range aliases.Aliases {
		if alias.Alias == name {
			if "/collections/"+alias.Collection != c {
				t.Fatal("unexpected alias target")
			}
			t.Log("atomic alias action list permits stale replacement; it is not compare-and-swap")
			return
		}
	}
	t.Fatal("owned alias missing")
}

func TestCanceledRequestPreservesContextError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := request(ctx, client(), "GET", "/collections", nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
