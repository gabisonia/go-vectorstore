package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/gabisonia/go-vectorstore/stores/sqlite"
	"github.com/gabisonia/go-vectorstore/stores/sqlitevec"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type metric struct {
	NS      int64  `json:"duration_ns"`
	Bytes   uint64 `json:"go_allocated_bytes"`
	Allocs  uint64 `json:"go_allocations"`
	Calls   int    `json:"calls"`
	Records int    `json:"records"`
}

func measure[T any](call func() (T, error)) (T, metric, error) {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	value, err := call()
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)
	return value, metric{NS: int64(elapsed), Bytes: after.TotalAlloc - before.TotalAlloc, Allocs: after.Mallocs - before.Mallocs, Calls: 1}, err
}

func (m *metric) add(other metric) {
	m.NS += other.NS
	m.Bytes += other.Bytes
	m.Allocs += other.Allocs
	m.Calls += other.Calls
	m.Records += other.Records
}

type storage struct {
	Main int64 `json:"main_bytes"`
	WAL  int64 `json:"wal_bytes"`
	SHM  int64 `json:"shm_bytes"`
}

func fileStorage(path string) (storage, error) {
	var result storage
	for _, part := range []struct {
		suffix string
		target *int64
	}{{"", &result.Main}, {"-wal", &result.WAL}, {"-shm", &result.SHM}} {
		info, err := os.Stat(path + part.suffix)
		if errors.Is(err, os.ErrNotExist) && part.suffix != "" {
			continue
		}
		if err != nil {
			return storage{}, err
		}
		*part.target = info.Size()
	}
	return result, nil
}

type queryResult struct {
	Mode            string `json:"mode"`
	Cache           string `json:"cache"`
	EligiblePercent int    `json:"eligible_percent"`
	EligibleRecords int    `json:"eligible_records"`
	Top             int    `json:"top"`
	Sample          int    `json:"sample"`
	Matches         int    `json:"matches"`
	Metric          metric `json:"measurement"`
	OpenNS          int64  `json:"open_ns,omitempty"`
	Digest          string `json:"page_sha256"`
}

type fixtureReport struct {
	Type                 string             `json:"type"`
	Records              int                `json:"records"`
	Dimensions           int                `json:"dimensions"`
	Distribution         string             `json:"distribution"`
	Load                 map[string]metric  `json:"load"`
	LoadCloseNS          int64              `json:"load_close_ns"`
	StorageAfterLoad     map[string]storage `json:"storage_after_load"`
	Queries              []queryResult      `json:"queries"`
	Upsert               map[string]metric  `json:"upsert"`
	UpdatedRecords       int                `json:"updated_records"`
	Rebuild              metric             `json:"rebuild"`
	StorageRebuildOpen   storage            `json:"storage_after_rebuild_open"`
	FinalCloseNS         int64              `json:"final_close_ns"`
	StorageAfterWorkload map[string]storage `json:"storage_after_workload"`
}

func definition(dimensions int) vectordata.CollectionDefinition {
	return vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString}, {Name: "Group", Type: vectordata.TypeInt64}}, Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: dimensions, Metric: vectordata.DistanceL1}}}
}

type reader struct {
	mode, path string
	d          vectordata.CollectionDefinition
	store      *sqlite.Store
	collection vectordata.ModelCollection
}

func (r *reader) open(ctx context.Context, create bool) error {
	var err error
	if r.mode == "sqlitevec" {
		r.store, err = sqlitevec.Open(ctx, r.path, sqlitevec.StoreOptions{})
	} else {
		r.store, err = sqlite.Open(ctx, r.path, sqlite.StoreOptions{VectorProjections: r.mode == "packed_go"})
	}
	if err != nil {
		return err
	}
	if create {
		r.collection, err = r.store.EnsureModelCollection(ctx, "records", r.d, vectordata.ModelCollectionOptions{})
	} else {
		r.collection, err = r.store.GetModelCollection("records", r.d, vectordata.ModelCollectionOptions{})
	}
	if err != nil {
		r.close()
	}
	return err
}

func (r *reader) close() error {
	if r.store == nil {
		return nil
	}
	err := r.store.Close()
	r.store = nil
	r.collection = nil
	return err
}

func timedWrite(ctx context.Context, c vectordata.ModelCollection, rows []vectordata.ModelRecord, upsert bool) (metric, error) {
	keys, m, err := measure(func() ([]any, error) {
		if upsert {
			return c.Upsert(ctx, rows)
		}
		return c.Insert(ctx, rows)
	})
	if err != nil {
		return m, err
	}
	if len(keys) != len(rows) {
		return m, fmt.Errorf("write returned %d keys for %d records", len(keys), len(rows))
	}
	for i, key := range keys {
		if key != rows[i].Key {
			return m, fmt.Errorf("write key %v, expected %v", key, rows[i].Key)
		}
	}
	m.Records = len(rows)
	return m, nil
}

func runFixture(ctx context.Context, c config, records, dimensions int, logf func(string, ...any)) (report fixtureReport, err error) {
	dir, err := os.MkdirTemp(c.Parent, "vectorstore-workload-")
	if err != nil {
		return report, err
	}
	defer func() { err = errors.Join(err, os.RemoveAll(dir)) }()
	d := definition(dimensions)
	canonical := &reader{mode: "canonical", path: filepath.Join(dir, "canonical.db"), d: d}
	native := &reader{mode: "sqlitevec", path: filepath.Join(dir, "projected.db"), d: d}
	packed := &reader{mode: "packed_go", path: native.path, d: d}
	for _, r := range []*reader{canonical, native, packed} {
		defer r.close()
	}
	report = fixtureReport{Type: "fixture", Records: records, Dimensions: dimensions, Distribution: c.Distribution, Load: map[string]metric{}, Upsert: map[string]metric{}, StorageAfterLoad: map[string]storage{}, StorageAfterWorkload: map[string]storage{}}
	for _, r := range []*reader{canonical, native} {
		if err := r.open(ctx, true); err != nil {
			return report, err
		}
	}
	logf("records=%d dimensions=%d phase=load distribution=%s", records, dimensions, c.Distribution)
	query := fixtureVector(-1, dimensions, 0, "random")
	body := strings.Repeat("x", c.Payload)
	maxTop := min(slices.Max(c.Tops), records)
	oracles := map[int]*oracle{100: {limit: maxTop}, 1: {limit: maxTop}, 10: {limit: maxTop}}
	for start, batchID := 0, 0; start < records; batchID++ {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		end := start + min(c.Batch, records-start)
		rows := make([]vectordata.ModelRecord, end-start)
		for i := start; i < end; i++ {
			key := int64(i)
			vector := fixtureVector(key, dimensions, 0, c.Distribution)
			rows[i-start] = vectordata.ModelRecord{Key: key, Data: map[string]any{"Body": body, "Group": key % 100}, Vectors: map[string][]float32{"V": vector}}
			distance := referenceDistance(query, vector)
			oracles[100].add(key, distance)
			if i%100 == 0 {
				oracles[1].add(key, distance)
			}
			if i%100 < 10 {
				oracles[10].add(key, distance)
			}
		}
		order := []*reader{canonical, native}
		if batchID%2 != 0 {
			slices.Reverse(order)
		}
		for _, r := range order {
			m, err := timedWrite(ctx, r.collection, rows, false)
			if err != nil {
				return report, err
			}
			name := "canonical"
			if r == native {
				name = "projected"
			}
			total := report.Load[name]
			total.add(m)
			report.Load[name] = total
		}
		start = end
		if start == records || start/10000 != (start-len(rows))/10000 {
			logf("records=%d dimensions=%d loaded=%d", records, dimensions, start)
		}
	}
	startClose := time.Now()
	if err := errors.Join(canonical.close(), native.close()); err != nil {
		return report, err
	}
	report.LoadCloseNS = int64(time.Since(startClose))
	for name, path := range map[string]string{"canonical": canonical.path, "projected": native.path} {
		size, err := fileStorage(path)
		if err != nil {
			return report, err
		}
		report.StorageAfterLoad[name] = size
	}
	readers := []*reader{canonical, packed, native}
	for _, r := range readers {
		if err := r.open(ctx, false); err != nil {
			return report, err
		}
		n, err := r.collection.Count(ctx, nil)
		if err != nil {
			return report, err
		}
		if n != int64(records) {
			return report, fmt.Errorf("%s count=%d, expected %d", r.mode, n, records)
		}
	}
	caseID := 0
	for _, top := range c.Tops {
		for _, eligible := range []int{100, 1, 10} {
			var filter vectordata.Filter
			if eligible == 1 {
				filter = vectordata.Eq(vectordata.Column("Group"), int64(0))
			}
			if eligible == 10 {
				filter = vectordata.Lt(vectordata.Column("Group"), int64(10))
			}
			opts := vectordata.ModelSearchOptions{Filter: filter}
			oracle := oracles[eligible]
			want := oracle.hits[:min(top, len(oracle.hits))]
			for _, r := range readers {
				hits, err := r.collection.Search(ctx, query, top, opts)
				if err != nil {
					return report, err
				}
				if _, err := validatePage(hits, want, body); err != nil {
					return report, fmt.Errorf("%s warm-up: %w", r.mode, err)
				}
			}
			for sample := 1; sample <= c.Samples; sample++ {
				for i := range readers {
					r := readers[(i+caseID+sample)%len(readers)]
					result, err := measureQuery(ctx, r, query, top, opts, want, body, eligible, oracle.count, "warm", sample)
					if err != nil {
						return report, err
					}
					report.Queries = append(report.Queries, result)
					logf("records=%d dimensions=%d mode=%s cache=warm eligible=%d top=%d sample=%d ms=%.2f", records, dimensions, r.mode, eligible, top, sample, float64(result.Metric.NS)/1e6)
				}
			}
			for i := range readers {
				r := readers[(i+caseID)%len(readers)]
				if err := r.close(); err != nil {
					return report, err
				}
				started := time.Now()
				if err := r.open(ctx, false); err != nil {
					return report, err
				}
				openNS := int64(time.Since(started))
				result, err := measureQuery(ctx, r, query, top, opts, want, body, eligible, oracle.count, "reopened", 1)
				if err != nil {
					return report, err
				}
				result.OpenNS = openNS
				report.Queries = append(report.Queries, result)
				logf("records=%d dimensions=%d mode=%s cache=reopened eligible=%d top=%d ms=%.2f", records, dimensions, r.mode, eligible, top, float64(result.Metric.NS)/1e6)
			}
			caseID++
		}
	}
	// Change payloads/vectors in a bounded update set. Key 0 becomes an exact
	// query match so a stale projection cannot pass the post-write check.
	report.UpdatedRecords = min(records, 1024)
	updatedBody := strings.Repeat("y", c.Payload)
	logf("records=%d dimensions=%d phase=upsert updated=%d", records, dimensions, report.UpdatedRecords)
	for start, batchID := 0, 0; start < report.UpdatedRecords; batchID++ {
		end := start + min(c.Batch, report.UpdatedRecords-start)
		rows := make([]vectordata.ModelRecord, end-start)
		for i := start; i < end; i++ {
			key := int64(i)
			v := fixtureVector(key, dimensions, 1, "random")
			if i == 0 {
				v = slices.Clone(query)
			}
			rows[i-start] = vectordata.ModelRecord{Key: key, Data: map[string]any{"Body": updatedBody, "Group": key % 100}, Vectors: map[string][]float32{"V": v}}
		}
		order := []*reader{canonical, native}
		if batchID%2 != 0 {
			slices.Reverse(order)
		}
		for _, r := range order {
			m, err := timedWrite(ctx, r.collection, rows, true)
			if err != nil {
				return report, err
			}
			name := "canonical"
			if r == native {
				name = "projected"
			}
			total := report.Upsert[name]
			total.add(m)
			report.Upsert[name] = total
		}
		start = end
	}
	verifyUpdated := func() error {
		for _, r := range readers {
			hits, err := r.collection.Search(ctx, query, 1, vectordata.ModelSearchOptions{})
			if err != nil {
				return err
			}
			if _, err := validatePage(hits, []expectedHit{{key: 0, distance: 0}}, updatedBody); err != nil {
				return fmt.Errorf("%s updated projection: %w", r.mode, err)
			}
		}
		return nil
	}
	if err := verifyUpdated(); err != nil {
		return report, err
	}
	logf("records=%d dimensions=%d phase=rebuild", records, dimensions)
	_, report.Rebuild, err = measure(func() (struct{}, error) {
		return struct{}{}, native.collection.(*sqlite.Collection).RebuildVectorProjections(ctx)
	})
	if err != nil {
		return report, err
	}
	report.Rebuild.Records = records
	if report.StorageRebuildOpen, err = fileStorage(native.path); err != nil {
		return report, err
	}
	if err := verifyUpdated(); err != nil {
		return report, err
	}
	startClose = time.Now()
	if err := errors.Join(canonical.close(), packed.close(), native.close()); err != nil {
		return report, err
	}
	report.FinalCloseNS = int64(time.Since(startClose))
	for name, path := range map[string]string{"canonical": canonical.path, "projected": native.path} {
		size, err := fileStorage(path)
		if err != nil {
			return report, err
		}
		report.StorageAfterWorkload[name] = size
	}
	logf("records=%d dimensions=%d phase=complete rebuild_seconds=%.2f", records, dimensions, float64(report.Rebuild.NS)/1e9)
	return report, nil
}

func measureQuery(ctx context.Context, r *reader, query []float32, top int, opts vectordata.ModelSearchOptions, want []expectedHit, body string, eligible, eligibleCount int, cache string, sample int) (queryResult, error) {
	runtime.GC()
	hits, m, err := measure(func() ([]vectordata.ModelSearchResult, error) { return r.collection.Search(ctx, query, top, opts) })
	if err != nil {
		return queryResult{}, fmt.Errorf("%s %s: %w", r.mode, cache, err)
	}
	digest, err := validatePage(hits, want, body)
	if err != nil {
		return queryResult{}, fmt.Errorf("%s %s top=%d eligible=%d: %w", r.mode, cache, top, eligible, err)
	}
	m.Records = len(hits)
	return queryResult{Mode: r.mode, Cache: cache, EligiblePercent: eligible, EligibleRecords: eligibleCount, Top: top, Sample: sample, Matches: len(hits), Metric: m, Digest: digest}, nil
}
