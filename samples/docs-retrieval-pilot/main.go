// The pilot is a consumer of the published module, not a root-module benchmark.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"syscall"
	"time"
)

type config struct {
	Corpus, Queries, Output, URL, Model, Revision       string
	Dimensions, ContextTokens, ChunkRunes, OverlapRunes int
	Threads                                             int
	K, Candidates, Workers, PerWorker                   int
	Timeout, OperationTimeout                           time.Duration
}

type environment struct {
	Library, Go, OS, Arch, Revision, OllamaVersion string
	CPUs                                           int
}
type heap struct{ AllocBytes, TotalAllocatedBytes, NumGC uint64 }

func readHeap() heap {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return heap{m.HeapAlloc, m.TotalAlloc, uint64(m.NumGC)}
}

type restoreResult struct {
	FileSHA256, RecordSHA256 string
	Bytes                    int64
	Records                  int
	IdenticalRankings        bool
	Seconds                  float64
}
type report struct {
	Completed                          bool
	Stage, Error                       string
	StartedUTC                         string
	Environment                        environment
	Config                             config
	Identity                           identity
	CorpusSHA256, QueriesSHA256        string
	Ingestion                          ingestion
	QueryEmbeddingSeconds              float64
	Quality                            quality
	Load                               loadResult
	HeapBeforeIngestion, HeapAfterLoad heap
	Restore                            restoreResult
}

func libraryVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/gabisonia/go-vectorstore" {
				if dep.Replace != nil {
					return dep.Version + " (replaced)"
				}
				return dep.Version
			}
		}
	}
	return "unknown (test binary)"
}

func saveReport(dir string, result report) error {
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".report-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, writeErr := f.Write(append(b, '\n'))
	syncErr := f.Sync()
	closeErr := f.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(dir, "report.json"))
}

func runPilot(ctx context.Context, cfg config) (result report, err error) {
	result = report{StartedUTC: time.Now().UTC().Format(time.RFC3339), Config: cfg, Stage: "validate"}
	result.Environment = environment{Library: libraryVersion(), Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, Revision: cfg.Revision, CPUs: runtime.NumCPU()}
	if cfg.Workers < 1 || cfg.Workers > 32 || cfg.PerWorker < 1 || cfg.PerWorker > 1000 || cfg.Workers*cfg.PerWorker > 10000 || cfg.K < 1 || cfg.K > 20 || cfg.Candidates < cfg.K || cfg.Candidates > 1000 || cfg.Dimensions < 1 || cfg.Dimensions > 4096 || cfg.ContextTokens < 32 || cfg.ContextTokens > 32768 || cfg.Threads < 1 || cfg.Threads > 16 || cfg.Timeout <= 0 || cfg.OperationTimeout <= 0 {
		return result, fmt.Errorf("invalid workload/model bounds; see -help")
	}
	desired, sources, corpusHash, err := readCorpus(cfg.Corpus, cfg.ChunkRunes, cfg.OverlapRunes)
	if err != nil {
		return result, err
	}
	queries, queryHash, err := readQueries(cfg.Queries, sources)
	if err != nil {
		return result, err
	}
	result.CorpusSHA256, result.QueriesSHA256 = corpusHash, queryHash
	embedder := &ollama{URL: cfg.URL, Model: modelIdentity{Name: cfg.Model, Dimensions: cfg.Dimensions, ContextTokens: cfg.ContextTokens, Threads: cfg.Threads}, Client: &http.Client{Timeout: 2 * time.Minute}}
	defer embedder.Client.CloseIdleConnections()
	version, err := embedder.identify(ctx)
	if err != nil {
		return result, err
	}
	result.Environment.OllamaVersion = version
	result.Identity = identity{Format: 1, Model: embedder.Model, ChunkRunes: cfg.ChunkRunes, OverlapRunes: cfg.OverlapRunes, Metric: "cosine"}
	unlock, err := ownDirectory(cfg.Output)
	if err != nil {
		return result, err
	}
	defer unlock()
	if err := saveReport(cfg.Output, result); err != nil {
		return result, err
	}
	// Replace a previous completion report with this attempt, even on failure.
	defer func() {
		if err != nil {
			result.Error = err.Error()
			result.Completed = false
		}
		err = errors.Join(err, saveReport(cfg.Output, result))
	}()
	path := filepath.Join(cfg.Output, "index.sqlite")
	result.Stage = "ingest"
	idx, err := openIndex(ctx, path, embedder, result.Identity, true)
	if err != nil {
		return result, err
	}
	defer func() {
		if idx != nil {
			err = errors.Join(err, idx.store.Close())
		}
	}()
	result.HeapBeforeIngestion = readHeap()
	start := time.Now()
	result.Ingestion, err = idx.sync(ctx, desired)
	result.Ingestion.Files = len(sources)
	result.Ingestion.Seconds = time.Since(start).Seconds()
	if err != nil {
		return result, err
	}
	result.Stage = "quality"
	texts := make([]string, len(queries))
	for i, q := range queries {
		texts[i] = q.Text
	}
	start = time.Now()
	// Bound request size for custom query sets too.
	var vectors [][]float32
	for offset := 0; offset < len(texts); offset += 16 {
		batch, err := embedder.Embed(ctx, texts[offset:min(offset+16, len(texts))])
		if err != nil {
			return result, err
		}
		vectors = append(vectors, batch...)
	}
	result.QueryEmbeddingSeconds = time.Since(start).Seconds()
	result.Quality, err = idx.evaluate(ctx, queries, vectors, cfg.K, cfg.Candidates)
	if err != nil {
		return result, err
	}
	result.Stage = "load"
	result.Load, err = idx.load(ctx, vectors, cfg.Candidates, cfg.Workers, cfg.PerWorker, cfg.OperationTimeout)
	result.HeapAfterLoad = readHeap()
	if err != nil {
		return result, err
	}
	result.Stage = "offline-restore"
	start = time.Now()
	digest, count, err := idx.digest(ctx)
	if err != nil {
		return result, err
	}
	if count != len(desired) {
		return result, fmt.Errorf("stored count %d differs from corpus %d", count, len(desired))
	}
	if err := idx.store.Close(); err != nil {
		return result, err
	}
	idx = nil
	backup := filepath.Join(cfg.Output, "backup.sqlite")
	backupHash, bytes, err := offlineCopy(path, backup)
	if err != nil {
		return result, err
	}
	restoredPath := filepath.Join(cfg.Output, "restored.sqlite")
	restoredHash, restoredBytes, err := offlineCopy(backup, restoredPath)
	if err != nil {
		return result, err
	}
	if restoredHash != backupHash || restoredBytes != bytes {
		return result, fmt.Errorf("restore file mismatch")
	}
	restored, err := openIndex(ctx, restoredPath, embedder, result.Identity, false)
	if err != nil {
		return result, err
	}
	defer func() { err = errors.Join(err, restored.store.Close()) }()
	restoredDigest, restoredCount, err := restored.digest(ctx)
	if err != nil {
		return result, err
	}
	if restoredDigest != digest || restoredCount != count {
		return result, fmt.Errorf("restored records/vectors differ")
	}
	restoredQuality, err := restored.evaluate(ctx, queries, vectors, cfg.K, cfg.Candidates)
	if err != nil {
		return result, err
	}
	if !reflect.DeepEqual(result.Quality.Queries, restoredQuality.Queries) {
		return result, fmt.Errorf("restored rankings/scores differ")
	}
	result.Restore = restoreResult{FileSHA256: backupHash, RecordSHA256: digest, Bytes: bytes, Records: count, IdenticalRankings: true, Seconds: time.Since(start).Seconds()}
	// Detect a changed tag during the run; external model replacement must be quiesced.
	if _, err := embedder.identify(ctx); err != nil {
		return result, err
	}
	if embedder.Model != result.Identity.Model {
		return result, fmt.Errorf("Ollama model identity changed during the pilot")
	}
	result.Stage, result.Completed = "complete", true
	return result, nil
}

func main() {
	cfg := config{}
	flag.StringVar(&cfg.Corpus, "corpus", "../../docs", "Markdown directory (500 files, 512 KiB/file, 8 MiB total, 10000 chunks max)")
	flag.StringVar(&cfg.Queries, "queries", "queries.json", "1..100 graded document queries")
	flag.StringVar(&cfg.Output, "output", "data/pilot", "exclusively owned output directory")
	flag.StringVar(&cfg.URL, "ollama", "http://127.0.0.1:11434", "local Ollama origin")
	flag.StringVar(&cfg.Model, "model", "all-minilm:22m", "installed model name including tag")
	flag.StringVar(&cfg.Revision, "revision", "unspecified", "source revision/dirty status supplied by the operator")
	flag.IntVar(&cfg.Dimensions, "dimensions", 384, "dense dimensions (1..4096)")
	flag.IntVar(&cfg.ContextTokens, "context-tokens", 512, "Ollama num_ctx (32..32768); never silently truncate")
	flag.IntVar(&cfg.Threads, "embedding-threads", 2, "Ollama CPU threads (1..16); small models need few threads")
	flag.IntVar(&cfg.ChunkRunes, "chunk-runes", 400, "fixed Markdown rune window (32..2000)")
	flag.IntVar(&cfg.OverlapRunes, "overlap-runes", 80, "window overlap in runes")
	flag.IntVar(&cfg.K, "k", 5, "document rank cutoff (1..20)")
	flag.IntVar(&cfg.Candidates, "candidates", 100, "retrieved chunks before document deduplication (k..1000)")
	flag.IntVar(&cfg.Workers, "concurrency", 4, "concurrent search workers (1..32)")
	flag.IntVar(&cfg.PerWorker, "requests-per-worker", 25, "search requests per worker (1..1000; total <=10000)")
	flag.DurationVar(&cfg.Timeout, "timeout", 10*time.Minute, "overall deadline")
	flag.DurationVar(&cfg.OperationTimeout, "search-timeout", 5*time.Second, "per-load-search deadline")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	r, err := runPilot(ctx, cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, r.Stage+":", err)
		os.Exit(1)
	}
	fmt.Printf("%d files, %d chunks (%d reused); Recall@%d %.3f, MRR %.3f, nDCG %.3f\n", r.Ingestion.Files, r.Ingestion.Chunks, r.Ingestion.Reused, cfg.K, r.Quality.Mean.Recall, r.Quality.Mean.MRR, r.Quality.Mean.NDCG)
	fmt.Printf("%d searches, %d failures; p50 %.2f ms, p95 %.2f ms; restore verified: %d records\nReport: %s\n", r.Load.Requests, r.Load.Failures, r.Load.P50Millis, r.Load.P95Millis, r.Restore.Records, filepath.Join(cfg.Output, "report.json"))
}
