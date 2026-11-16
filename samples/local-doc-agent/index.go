package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gabisonia/go-vectorstore/stores/sqlite"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type chunk struct {
	ID      string    `vectordata:"key"`
	Section string    `vectordata:"data,index"`
	Source  string    `vectordata:"data,index"`
	Heading string    `vectordata:"data"`
	Part    int64     `vectordata:"data"`
	Text    string    `vectordata:"data"`
	Hash    string    `vectordata:"data"`
	Vector  []float32 `vectordata:"vector,dimensions=384,source=Text"`
}
type index struct {
	store  *sqlite.Store
	chunks *vectordata.TypedModelCollection[string, chunk]
}

func openIndex(ctx context.Context, cfg config, embedder vectordata.Embedder) (*index, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.Database), 0755); err != nil {
		return nil, err
	}
	store, err := sqlite.Open(ctx, cfg.Database, sqlite.StoreOptions{Embedder: embedder, EmbeddingBatchSize: 4})
	if err != nil {
		return nil, err
	}
	fail := func(err error) (*index, error) { store.Close(); return nil, err }
	// Dimensions alone do not establish embedding compatibility.
	definition := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Value", Type: vectordata.TypeString}}}
	metadata, err := store.EnsureModelCollection(ctx, "demo_metadata", definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		return fail(err)
	}
	identity := fmt.Sprintf("%s/%d/chunks-v1", cfg.EmbedModel, cfg.Dimensions)
	saved, err := metadata.Get(ctx, "embedding", vectordata.RetrievalOptions{})
	if err == nil && saved.Data["Value"] != identity {
		return fail(fmt.Errorf("embedding model/configuration changed; select a new DATABASE_PATH or reset this demo index"))
	}
	if err != nil && !errors.Is(err, vectordata.ErrNotFound) {
		return fail(err)
	}
	if errors.Is(err, vectordata.ErrNotFound) {
		if _, err := metadata.Insert(ctx, []vectordata.ModelRecord{{Key: "embedding", Data: map[string]any{"Value": identity}}}); err != nil {
			return fail(err)
		}
	}
	d, err := vectordata.DefinitionFor[chunk]()
	if err != nil {
		return fail(err)
	}
	d.Vectors[0].Dimensions = cfg.Dimensions
	records, err := vectordata.EnsureTypedModelCollection[string, chunk](ctx, store, "demo_chunks", &d, vectordata.ModelCollectionOptions{})
	if err != nil {
		return fail(err)
	}
	return &index{store: store, chunks: records}, nil
}
func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
func slug(text string) string {
	var out strings.Builder
	separator := false
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if separator && out.Len() > 0 {
				out.WriteByte('-')
			}
			out.WriteRune(r)
			separator = false
		} else {
			separator = true
		}
	}
	if out.Len() == 0 {
		return "section"
	}
	return out.String()
}

// Read all inputs before changing persistent records. Ignore symlinks and cap the corpus.
func readCorpus(root string) ([]chunk, error) {
	var records []chunk
	files, total := 0, 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".md") {
			return nil
		}
		files++
		if files > 100 {
			return fmt.Errorf("demo supports at most 100 Markdown files")
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > 128<<10 {
			return fmt.Errorf("document too large: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		total += len(data)
		if total > 2<<20 {
			return fmt.Errorf("demo corpus exceeds 2 MiB")
		}
		if !utf8.Valid(data) {
			return fmt.Errorf("document requires UTF-8: %s", path)
		}
		source, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		source = filepath.ToSlash(source)
		used := map[string]bool{}
		heading := "Introduction"
		var body strings.Builder
		fenced := false
		flush := func() {
			text := strings.TrimSpace(body.String())
			body.Reset()
			if text == "" {
				return
			}
			base := source + "#" + slug(heading)
			section := base
			for suffix := 2; used[section]; suffix++ {
				section = fmt.Sprintf("%s-%d", base, suffix)
			}
			used[section] = true
			// Tiny chunks fit all-MiniLM; truncate=false catches token-dense inputs.
			runes := []rune(text)
			for part := 0; len(runes) > 0; part++ {
				n := min(600, len(runes))
				content := string(runes[:n])
				runes = runes[n:]
				records = append(records, chunk{ID: fmt.Sprintf("%s/%04d", section, part), Section: section, Source: source, Heading: heading, Part: int64(part), Text: content, Hash: digest(content)})
			}
		}
		for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
				fenced = !fenced
			}
			if !fenced && strings.HasPrefix(line, "#") {
				title := strings.TrimLeft(line, "#")
				if len(title) < len(line) && strings.HasPrefix(title, " ") {
					flush()
					heading = strings.TrimSpace(title)
					continue
				}
			}
			body.WriteString(line)
			body.WriteByte('\n')
		}
		flush()
		if len(records) > 4000 {
			return fmt.Errorf("demo corpus exceeds 4000 chunks")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("no Markdown content found in DOCS_DIR")
	}
	return records, nil
}
func (i *index) sync(ctx context.Context, records []chunk) (int, error) {
	existing, err := i.chunks.Query(ctx, nil, 5000, vectordata.QueryOptions{})
	if err != nil {
		return 0, err
	}
	previous := map[string]chunk{}
	for _, row := range existing {
		previous[row.ID] = row
	}
	var changed []chunk
	for _, row := range records {
		old, ok := previous[row.ID]
		if !ok || old.Hash != row.Hash || old.Heading != row.Heading {
			changed = append(changed, row)
		}
		delete(previous, row.ID)
	}
	if len(changed) > 0 {
		if _, err := i.chunks.Upsert(ctx, changed); err != nil {
			return 0, err
		}
	}
	stale := make([]string, 0, len(previous))
	for key := range previous {
		stale = append(stale, key)
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		if _, err := i.chunks.Delete(ctx, stale); err != nil {
			return 0, err
		}
	}
	return len(changed), nil
}
