package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/gabisonia/go-vectorstore/stores/sqlite"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type index struct {
	store  *sqlite.Store
	chunks *vectordata.TypedModelCollection[string, chunk]
}

type identity struct {
	Format                   int
	Model                    modelIdentity
	ChunkRunes, OverlapRunes int
	Metric                   string
}

var metadataDefinition = vectordata.CollectionDefinition{
	Key:  vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyString},
	Data: []vectordata.DataProperty{{Name: "Value", Type: vectordata.TypeString}},
}

func openIndex(ctx context.Context, path string, embedder *ollama, id identity, create bool) (*index, error) {
	store, err := sqlite.Open(ctx, path, sqlite.StoreOptions{Embedder: embedder, EmbeddingBatchSize: 16, VectorProjections: true})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*index, error) { store.Close(); return nil, err }
	meta, err := store.GetModelCollection("pilot_metadata", metadataDefinition, vectordata.ModelCollectionOptions{})
	if err != nil {
		return fail(err)
	}
	if create {
		if err := meta.EnsureExists(ctx); err != nil {
			return fail(err)
		}
	}
	encoded, err := json.Marshal(id)
	if err != nil {
		return fail(err)
	}
	saved, err := meta.Get(ctx, "identity", vectordata.RetrievalOptions{})
	if errors.Is(err, vectordata.ErrNotFound) && create {
		// Only initialize metadata in a fresh database, never bless existing chunks.
		names, listErr := store.ListCollectionNames(ctx)
		if listErr != nil {
			return fail(listErr)
		}
		if len(names) != 1 || names[0] != "pilot_metadata" {
			return fail(fmt.Errorf("missing identity in existing index"))
		}
		_, err = meta.Insert(ctx, []vectordata.ModelRecord{{Key: "identity", Data: map[string]any{"Value": string(encoded)}}})
	} else if err == nil && saved.Data["Value"] != string(encoded) {
		return fail(fmt.Errorf("model digest/dimensions/chunk configuration changed; use a new output directory"))
	}
	if err != nil {
		return fail(err)
	}
	d, err := vectordata.DefinitionFor[chunk]()
	if err != nil {
		return fail(err)
	}
	d.Vectors[0].Dimensions = id.Model.Dimensions
	rows, err := vectordata.GetTypedModelCollection[string, chunk](store, "pilot_chunks", &d, vectordata.ModelCollectionOptions{})
	if err != nil {
		return fail(err)
	}
	if create {
		if err := rows.EnsureExists(ctx); err != nil {
			return fail(err)
		}
	} else {
		exists, err := rows.Exists(ctx)
		if err != nil {
			return fail(err)
		}
		if !exists {
			return fail(fmt.Errorf("restored collection missing"))
		}
	}
	return &index{store: store, chunks: rows}, nil
}

type ingestion struct {
	Files, Chunks, Reused, Upserted, Deleted int
	Seconds                                  float64
}

func (idx *index) all(ctx context.Context, vectors bool) ([]chunk, error) {
	rows, err := idx.chunks.Query(ctx, nil, maxChunks+1, vectordata.QueryOptions{IncludeVectors: vectors})
	if err != nil {
		return nil, err
	}
	if len(rows) > maxChunks {
		return nil, fmt.Errorf("stored corpus exceeds pilot limit")
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].ID < rows[j].ID })
	return rows, nil
}

func (idx *index) sync(ctx context.Context, desired []chunk) (ingestion, error) {
	result := ingestion{Chunks: len(desired)}
	old, err := idx.all(ctx, false)
	if err != nil {
		return result, err
	}
	byID := map[string]chunk{}
	for _, row := range old {
		byID[row.ID] = row
	}
	var changed []chunk
	for _, row := range desired {
		previous, exists := byID[row.ID]
		if exists && previous.Hash == row.Hash && previous.Text == row.Text && previous.Source == row.Source && previous.Offset == row.Offset {
			result.Reused++
		} else {
			changed = append(changed, row)
		}
		delete(byID, row.ID)
	}
	keys := make([]string, 0, len(byID))
	for key := range byID {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		deleted, err := idx.chunks.Delete(ctx, keys)
		if err != nil {
			return result, err
		}
		result.Deleted = int(deleted)
	}
	// Remove stale IDs first to preserve the chunk ceiling even if a batch fails.
	// Each batch is atomic; an interrupted whole-corpus sync is repaired on rerun.
	for start := 0; start < len(changed); start += 32 {
		batch := changed[start:min(start+32, len(changed))]
		if _, err := idx.chunks.Upsert(ctx, batch); err != nil {
			return result, err
		}
		result.Upserted += len(batch)
	}
	return result, nil
}

func (idx *index) digest(ctx context.Context) (string, int, error) {
	rows, err := idx.all(ctx, true)
	if err != nil {
		return "", 0, err
	}
	bytes, err := json.Marshal(rows)
	if err != nil {
		return "", 0, err
	}
	return checksum(bytes), len(rows), nil
}

const owner = "go-vectorstore-docs-pilot-v1\n"

// The output directory and lock are exclusively owned by this single-process CLI.
func ownDirectory(dir string) (func(), error) {
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("output must be a real directory")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			return nil, fmt.Errorf("output contains nonregular entry %s", entry.Name())
		}
	}
	marker := filepath.Join(dir, ".pilot-owner")
	if len(entries) == 0 {
		f, err := os.OpenFile(marker, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		_, writeErr := f.WriteString(owner)
		closeErr := f.Close()
		if writeErr != nil {
			return nil, writeErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
	} else {
		b, err := os.ReadFile(marker)
		if err != nil || string(b) != owner {
			return nil, fmt.Errorf("refusing an unowned nonempty output directory")
		}
	}
	lock := filepath.Join(dir, ".pilot-lock")
	f, err := os.OpenFile(lock, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("pilot already running or stale .pilot-lock: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(lock)
		return nil, err
	}
	return func() { os.Remove(lock) }, nil
}

// Call only after every writer, search worker and store handle has closed.
func offlineCopy(source, destination string) (string, int64, error) {
	if info, err := os.Stat(destination + "-wal"); err == nil && info.Size() != 0 {
		return "", 0, fmt.Errorf("destination has a nonempty WAL: refuse restore over unclean state")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", 0, err
	}
	if info, err := os.Stat(source + "-wal"); err == nil && info.Size() != 0 {
		return "", 0, fmt.Errorf("nonempty WAL: refuse unsafe database copy")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", 0, err
	}
	in, err := os.Open(source)
	if err != nil {
		return "", 0, err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(out, h), in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return "", 0, err
	}
	// Re-read destination; a stream hash alone wouldn't verify the copied file.
	verify, err := os.Open(destination)
	if err != nil {
		return "", 0, err
	}
	defer verify.Close()
	v := sha256.New()
	nv, err := io.Copy(v, verify)
	if err != nil {
		return "", 0, err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if nv != n || hex.EncodeToString(v.Sum(nil)) != digest {
		return "", 0, fmt.Errorf("backup byte/hash mismatch")
	}
	return digest, n, nil
}
