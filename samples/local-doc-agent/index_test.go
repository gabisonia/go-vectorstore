package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func fixtureIndex(t *testing.T) (*index, config) {
	t.Helper()
	cfg := config{Database: filepath.Join(t.TempDir(), "index.db"), EmbedModel: "fixture", Dimensions: 2, Timeout: time.Second}
	embedder := vectordata.EmbeddingFunc(func(ctx context.Context, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i := range texts {
			out[i] = []float32{1, 0}
		}
		return out, nil
	})
	i, err := openIndex(context.Background(), cfg, embedder)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { i.store.Close() })
	return i, cfg
}
func TestIndexUpdatesRemovalsAndModelIdentity(t *testing.T) {
	ctx := context.Background()
	i, cfg := fixtureIndex(t)
	corpus := []chunk{{ID: "one/0000", Section: "one", Source: "a.md", Heading: "A", Text: "old", Hash: digest("old")}, {ID: "two/0000", Section: "two", Source: "b.md", Heading: "B", Text: "removed", Hash: digest("removed")}}
	if n, err := i.sync(ctx, corpus); err != nil || n != 2 {
		t.Fatalf("initial sync: %d %v", n, err)
	}
	corpus = corpus[:1]
	corpus[0].Text = "new"
	corpus[0].Hash = digest("new")
	if n, err := i.sync(ctx, corpus); err != nil || n != 1 {
		t.Fatalf("changed sync: %d %v", n, err)
	}
	if _, err := i.read(ctx, sectionInput{SectionID: "two"}); err == nil {
		t.Fatal("removed document still readable")
	}
	read, err := i.read(ctx, sectionInput{SectionID: "one"})
	if err != nil || read.Text != "new" {
		t.Fatalf("stale document: %+v %v", read, err)
	}
	cfg.EmbedModel = "different"
	if other, err := openIndex(ctx, cfg, nil); err == nil {
		other.store.Close()
		t.Fatal("accepted a different model with same dimensions")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := i.chunks.SearchText(canceled, "q", 1, vectordata.ModelSearchOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
func TestCorpusParsingAndLimits(t *testing.T) {
	root := t.TempDir()
	body := "# Intro\n\n## A\n" + strings.Repeat("long text ", 100) + "\n```go\n## Not a heading\n```\n## A\nAgain.\n"
	if err := os.WriteFile(filepath.Join(root, "doc.md"), []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	rows, err := readCorpus(root)
	if err != nil {
		t.Fatal(err)
	}
	sections := map[string]bool{}
	for _, row := range rows {
		sections[row.Section] = true
		if len([]rune(row.Text)) > 600 {
			t.Fatal("oversized chunk")
		}
		if row.Heading == "Not a heading" {
			t.Fatal("parsed fenced code as heading")
		}
	}
	if !sections["doc.md#a"] || !sections["doc.md#a-2"] {
		t.Fatalf("duplicate heading IDs: %v", sections)
	}
	if err := os.WriteFile(filepath.Join(root, "bad.md"), []byte{255}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readCorpus(root); err == nil {
		t.Fatal("accepted invalid UTF-8")
	}
}
