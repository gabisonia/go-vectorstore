package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const maxChunks = 10000

type chunk struct {
	ID     string    `vectordata:"key"`
	Source string    `vectordata:"data"`
	Offset int64     `vectordata:"data"`
	Text   string    `vectordata:"data"`
	Hash   string    `vectordata:"data"`
	Vector []float32 `vectordata:"vector,dimensions=384,metric=cosine,source=Text"`
}

type query struct {
	ID       string         `json:"id"`
	Text     string         `json:"text"`
	Relevant map[string]int `json:"relevant"`
}

func checksum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Fixed rune windows make offsets and overlap reproducible, including Unicode.
// All limits are checked before the caller opens the index.
func readCorpus(root string, size, overlap int) ([]chunk, map[string]bool, string, error) {
	if size < 32 || size > 2000 || overlap < 0 || overlap >= size {
		return nil, nil, "", fmt.Errorf("chunk size must be 32..2000 and overlap 0..size-1")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, "", fmt.Errorf("corpus must be a real directory: %v", err)
	}
	var chunks []chunk
	sources := map[string]bool{}
	h := sha256.New()
	total := 0
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("corpus symlink: %s", path)
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("not a regular file: %s", path)
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		b, readErr := io.ReadAll(io.LimitReader(file, (512<<10)+1))
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		total += len(b)
		if len(b) > 512<<10 || total > 8<<20 || len(sources) >= 500 {
			return fmt.Errorf("corpus exceeds file/count/byte limits")
		}
		if !utf8.Valid(b) {
			return fmt.Errorf("invalid UTF-8: %s", path)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		source := filepath.ToSlash(rel)
		sources[source] = true
		// JSON framing avoids ambiguous concatenations of names and contents.
		if err := json.NewEncoder(h).Encode([]string{source, string(b)}); err != nil {
			return err
		}
		runes := []rune(string(b))
		for start := 0; start < len(runes); start += size - overlap {
			end := min(start+size, len(runes))
			text := string(runes[start:end])
			if strings.TrimSpace(text) != "" {
				chunks = append(chunks, chunk{ID: fmt.Sprintf("%s@%d", source, start), Source: source, Offset: int64(start), Text: text, Hash: checksum([]byte(text))})
				if len(chunks) > maxChunks {
					return fmt.Errorf("corpus exceeds %d chunks", maxChunks)
				}
			}
			if end == len(runes) {
				break
			}
		}
		return nil
	})
	if err == nil && len(chunks) == 0 {
		err = fmt.Errorf("corpus has no nonempty Markdown chunks")
	}
	return chunks, sources, hex.EncodeToString(h.Sum(nil)), err
}

func readQueries(path string, sources map[string]bool) ([]query, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, "", err
	}
	if len(b) > 1<<20 {
		return nil, "", fmt.Errorf("query file exceeds 1 MiB")
	}
	var queries []query
	if err := json.Unmarshal(b, &queries); err != nil {
		return nil, "", err
	}
	if len(queries) == 0 || len(queries) > 100 {
		return nil, "", fmt.Errorf("expected 1..100 queries")
	}
	seen := map[string]bool{}
	for _, q := range queries {
		if q.ID == "" || seen[q.ID] || strings.TrimSpace(q.Text) == "" || len([]rune(q.Text)) > 400 || len(q.Relevant) == 0 {
			return nil, "", fmt.Errorf("invalid/duplicate query %q", q.ID)
		}
		seen[q.ID] = true
		for source, grade := range q.Relevant {
			if !sources[source] || grade < 1 || grade > 2 {
				return nil, "", fmt.Errorf("query %s: missing source or invalid grade for %s", q.ID, source)
			}
		}
	}
	return queries, checksum(b), nil
}
