package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func write(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) (config, *atomic.Int64) {
	t.Helper()
	var embedded atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			json.NewEncoder(w).Encode(map[string]any{"models": []map[string]string{{"name": "test:fixed", "digest": "fixture-v1"}}})
		case "/api/version":
			json.NewEncoder(w).Encode(map[string]string{"version": "test"})
		case "/api/embed":
			var request struct {
				Model    string
				Input    []string
				Truncate bool
				Options  map[string]int
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				http.Error(w, "bad JSON", 400)
				return
			}
			if request.Truncate || request.Model != "test:fixed" || request.Options["num_ctx"] != 512 || request.Options["num_thread"] != 2 || len(request.Input) > 16 {
				t.Error("wrong embedding request configuration")
			}
			embedded.Add(int64(len(request.Input)))
			vectors := make([][]float32, len(request.Input))
			for i, text := range request.Input {
				vectors[i] = []float32{1 + float32(strings.Count(text, "backup")), 1 + float32(strings.Count(text, "filter")), .5}
			}
			json.NewEncoder(w).Encode(map[string]any{"embeddings": vectors})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	root := t.TempDir()
	corpus := filepath.Join(root, "docs")
	if err := os.Mkdir(corpus, 0700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(corpus, "backup.md"), "A backup keeps the database safe. Close the store before copying it.")
	write(t, filepath.Join(corpus, "filter.md"), "A filter limits matches to records with the right category.")
	queries := filepath.Join(root, "queries.json")
	write(t, queries, `[{"id":"backup","text":"backup database","relevant":{"backup.md":2}}]`)
	return config{Corpus: corpus, Queries: queries, Output: filepath.Join(root, "output"), URL: server.URL, Model: "test:fixed", Dimensions: 3, ContextTokens: 512, Threads: 2, ChunkRunes: 80, OverlapRunes: 16, K: 1, Candidates: 10, Workers: 3, PerWorker: 4, Timeout: time.Minute, OperationTimeout: time.Second}, &embedded
}

func TestPilotIngestReuseUpdateRestore(t *testing.T) {
	cfg, embedded := fixture(t)
	first, err := runPilot(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Completed || first.Ingestion.Upserted != 2 || first.Load.Requests != 12 || first.Load.Failures != 0 || first.Restore.Records != 2 || !first.Restore.IdenticalRankings || first.Quality.Mean.Recall != 1 {
		t.Fatalf("unexpected first run: %+v", first)
	}
	if embedded.Load() != 3 {
		t.Fatalf("embedding inputs %d, expected 2 chunks + 1 query", embedded.Load())
	}
	second, err := runPilot(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if second.Ingestion.Reused != 2 || second.Ingestion.Upserted != 0 || embedded.Load() != 4 {
		t.Fatalf("unchanged chunks were re-embedded: %+v, %d", second.Ingestion, embedded.Load())
	}
	write(t, filepath.Join(cfg.Corpus, "backup.md"), "Changed backup advice.")
	if err := os.Remove(filepath.Join(cfg.Corpus, "filter.md")); err != nil {
		t.Fatal(err)
	}
	third, err := runPilot(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if third.Ingestion.Upserted != 1 || third.Ingestion.Deleted != 1 || third.Restore.Records != 1 {
		t.Fatalf("stale records: %+v", third)
	}
	data, err := os.ReadFile(filepath.Join(cfg.Output, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved report
	if err := json.Unmarshal(data, &saved); err != nil || !saved.Completed {
		t.Fatalf("invalid completion report: %v", err)
	}
}

func TestIdentityMismatchDoesNotEmbedOrComplete(t *testing.T) {
	cfg, embedded := fixture(t)
	first, err := runPilot(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	before := embedded.Load()
	cfg.OverlapRunes++
	r, err := runPilot(t.Context(), cfg)
	if err == nil || r.Completed || embedded.Load() != before {
		t.Fatalf("configuration change accepted: %v %+v", err, r)
	}
	data, err := os.ReadFile(filepath.Join(cfg.Output, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved report
	if err := json.Unmarshal(data, &saved); err != nil || saved.Completed || saved.Error == "" {
		t.Fatalf("stale completion report: %s, %v", data, err)
	}
	// A subsequent compatible run still recovers the original stored vectors.
	cfg.OverlapRunes--
	again, err := runPilot(t.Context(), cfg)
	if err != nil || again.Restore.RecordSHA256 != first.Restore.RecordSHA256 {
		t.Fatalf("mismatch changed stored corpus: %v", err)
	}
}

func TestPilotBoundsEmbeddingBatches(t *testing.T) {
	cfg, embedded := fixture(t)
	write(t, filepath.Join(cfg.Corpus, "backup.md"), strings.Repeat("backup ", 300))
	r, err := runPilot(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture rejects any embedding call with more than 16 inputs.
	if r.Ingestion.Upserted <= 32 || embedded.Load() != int64(r.Ingestion.Upserted+1) {
		t.Fatalf("expected multiple bounded write batches: %+v", r.Ingestion)
	}
}

func TestLoadFailuresPreventCompletion(t *testing.T) {
	cfg, _ := fixture(t)
	cfg.OperationTimeout = time.Nanosecond
	r, err := runPilot(t.Context(), cfg)
	if err == nil || r.Completed || r.Stage != "load" || r.Load.Failures == 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("load failure hidden: %+v, %v", r, err)
	}
}

func TestOllamaDisappearingModelDoesNotReuseDigest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"models":[]}`)) }))
	defer server.Close()
	o := &ollama{URL: server.URL, Model: modelIdentity{Name: "removed:tag", Digest: "previous"}, Client: server.Client()}
	if _, err := o.identify(t.Context()); err == nil {
		t.Fatal("removed tag retained previous digest")
	}
}

func TestRestoredModelMetadataRequired(t *testing.T) {
	cfg, _ := fixture(t)
	r, err := runPilot(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	o := &ollama{Model: r.Identity.Model}
	id := r.Identity
	id.Model.Digest = "different-weights"
	idx, err := openIndex(t.Context(), filepath.Join(cfg.Output, "restored.sqlite"), o, id, false)
	if idx != nil {
		idx.store.Close()
	}
	if err == nil {
		t.Fatal("restored identity mismatch accepted")
	}
}

func TestCorpusRuneWindowsAndValidation(t *testing.T) {
	root := t.TempDir()
	text := strings.Repeat("á界🙂text ", 20)
	write(t, filepath.Join(root, "unicode.md"), text)
	chunks, sources, hash, err := readCorpus(root, 32, 8)
	if err != nil {
		t.Fatal(err)
	}
	if !sources["unicode.md"] || hash == "" || len(chunks) < 2 || chunks[1].Offset != 24 || chunks[1].Text != string([]rune(text)[24:56]) {
		t.Fatalf("bad rune windows: %+v", chunks)
	}
	_, _, _, err = readCorpus(root, 32, 32)
	if err == nil {
		t.Fatal("invalid overlap accepted")
	}
	write(t, filepath.Join(root, "invalid.md"), string([]byte{0xff}))
	if _, _, _, err := readCorpus(root, 32, 8); err == nil {
		t.Fatal("invalid UTF-8 accepted")
	}
	if err := os.Remove(filepath.Join(root, "invalid.md")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "unicode.md"), filepath.Join(root, "link.md")); err == nil {
		if _, _, _, err := readCorpus(root, 32, 8); err == nil {
			t.Fatal("symlink accepted")
		}
	}
}

func TestInvalidJudgmentsBeforeIndexCreation(t *testing.T) {
	cfg, embedded := fixture(t)
	write(t, cfg.Queries, `[{"id":"missing","text":"backup","relevant":{"missing.md":2}}]`)
	if _, err := runPilot(t.Context(), cfg); err == nil {
		t.Fatal("missing source accepted")
	}
	if _, err := os.Stat(cfg.Output); !errors.Is(err, os.ErrNotExist) || embedded.Load() != 0 {
		t.Fatal("invalid input created index or embedded")
	}
}

func TestGradedMetrics(t *testing.T) {
	r := score([]string{"irrelevant", "good", "best"}, map[string]int{"best": 2, "good": 1, "missing": 1}, 3)
	wantDCG := 1/math.Log2(3) + 3/math.Log2(4)
	wantIDCG := 3 + 1/math.Log2(3) + 1/math.Log2(4)
	if math.Abs(r.Recall-2.0/3) > 1e-12 || r.MRR != .5 || math.Abs(r.NDCG-wantDCG/wantIDCG) > 1e-12 {
		t.Fatalf("wrong metrics %+v", r)
	}
	if got := score([]string{"best", "best"}, map[string]int{"best": 2}, 5); got.Recall != 1 || got.NDCG != 1 {
		t.Fatalf("duplicates counted: %+v", got)
	}
	if percentile([]float64{1, 2, 3, 4}, .95) != 4 {
		t.Fatal("wrong percentile")
	}
}

func TestOutputOwnershipAndWALGuards(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "important.txt"), "keep")
	if _, err := ownDirectory(dir); err == nil {
		t.Fatal("unowned directory accepted")
	}
	dir = t.TempDir()
	unlock, err := ownDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if _, err := ownDirectory(dir); err == nil {
		t.Fatal("concurrent owner accepted")
	}
	source, dest := filepath.Join(dir, "source"), filepath.Join(dir, "copy")
	write(t, source, "database")
	write(t, source+"-wal", "uncheckpointed")
	if _, _, err := offlineCopy(source, dest); err == nil {
		t.Fatal("nonempty WAL copied")
	}
	if err := os.Remove(source + "-wal"); err != nil {
		t.Fatal(err)
	}
	write(t, dest+"-wal", "stale")
	if _, _, err := offlineCopy(source, dest); err == nil {
		t.Fatal("nonempty destination WAL overwritten")
	}
}

func TestOllamaRejectsBadVectorsAndCancellation(t *testing.T) {
	for name, body := range map[string]string{
		"count": `{"embeddings":[]}`, "dimensions": `{"embeddings":[[1,2]]}`,
		"zero": `{"embeddings":[[0,0,0]]}`, "infinite": `{"embeddings":[[1e100,1,1]]}`,
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer server.Close()
			o := &ollama{URL: server.URL, Model: modelIdentity{Name: "fixture", Dimensions: 3, ContextTokens: 512}, Client: server.Client()}
			if _, err := o.Embed(t.Context(), []string{"test"}); err == nil {
				t.Fatal("bad vector accepted")
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := o.Embed(ctx, []string{"test"}); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation lost: %v", err)
			}
		})
	}
}
