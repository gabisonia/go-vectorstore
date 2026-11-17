package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"testing"
)

func TestMeasuredProfilesVerifyPagesAndCleanFixtures(t *testing.T) {
	for _, distribution := range []string{"random", "ties"} {
		t.Run(distribution, func(t *testing.T) {
			parent := t.TempDir()
			c := config{Records: []int{200}, Dimensions: []int{8}, Tops: []int{3, 7}, Samples: 1, Batch: 64, Payload: 64, Distribution: distribution, Parent: parent, Quiet: true}
			var output bytes.Buffer
			if err := run(context.Background(), c, &output, io.Discard); err != nil {
				t.Fatal(err)
			}
			decoder := json.NewDecoder(&output)
			var env environment
			if err := decoder.Decode(&env); err != nil || env.Type != "environment" || env.Schema != 1 {
				t.Fatalf("environment: %+v %v", env, err)
			}
			var fixture fixtureReport
			if err := decoder.Decode(&fixture); err != nil {
				t.Fatal(err)
			}
			if fixture.Type != "fixture" || fixture.Records != 200 || len(fixture.Queries) != 36 {
				t.Fatalf("incomplete fixture: %+v", fixture)
			}
			for _, name := range []string{"canonical", "projected"} {
				if fixture.Load[name].Records != 200 || fixture.Load[name].Calls != 4 || fixture.Upsert[name].Records != 200 || fixture.StorageAfterLoad[name].Main <= 0 {
					t.Fatalf("missing operations/storage: %+v", fixture)
				}
			}
			// All three paths and both cache conditions must agree on each page,
			// including eligible sets smaller than top and the all-tied corpus.
			digests := map[[2]int]string{}
			for _, query := range fixture.Queries {
				key := [2]int{query.Top, query.EligiblePercent}
				if previous, ok := digests[key]; ok && previous != query.Digest {
					t.Fatalf("different measured pages: %+v", query)
				}
				digests[key] = query.Digest
				if query.Matches != min(query.Top, query.EligibleRecords) || query.Metric.Calls != 1 {
					t.Fatalf("incomplete query: %+v", query)
				}
			}
			var complete struct {
				Type     string
				Fixtures int
			}
			if err := decoder.Decode(&complete); err != nil || complete.Type != "complete" || complete.Fixtures != 1 {
				t.Fatalf("missing completion: %+v %v", complete, err)
			}
			var extra any
			if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
				t.Fatalf("unexpected trailing output: %v", err)
			}
			entries, err := os.ReadDir(parent)
			if err != nil || len(entries) != 0 {
				t.Fatalf("fixture leaked files: %v %v", entries, err)
			}
		})
	}
}

func TestCanceledWorkloadIsIncompleteAndCleansFixtures(t *testing.T) {
	parent := t.TempDir()
	c := config{Records: []int{200}, Dimensions: []int{8}, Tops: []int{3}, Samples: 1, Batch: 64, Payload: 64, Distribution: "random", Parent: parent, Quiet: true}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	if err := run(ctx, c, &output, io.Discard); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if bytes.Contains(output.Bytes(), []byte(`"type":"complete"`)) {
		t.Fatal("canceled run was reported complete")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("canceled fixture leaked files: %v %v", entries, err)
	}
}
