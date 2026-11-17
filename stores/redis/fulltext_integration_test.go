//go:build integration

package redis

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
	goredis "github.com/redis/go-redis/v9"
)

// Exercise real TEXT queries through the same atomic manifest/schema/readiness
// guard as native public reads. This is a test probe, not a new keyword API.
func fullTextKeys(t *testing.T, c *Collection, query string, params ...any) []string {
	t.Helper()
	ctx := context.Background()
	m, raw, indexed, err := c.nativeManifest(ctx)
	if err != nil || !indexed || m.IndexVersion != 2 {
		t.Fatalf("full-text generation unavailable: %+v %t %v", m, indexed, err)
	}
	reply, used, err := c.nativeReply(ctx, m, raw, nativeFilterPlan{query: query, params: params}, len(m.Tokens), "", nil, 0)
	if err != nil || !used {
		t.Fatalf("full-text query: %t %v", used, err)
	}
	rows, err := c.nativeRecords(ctx, m, reply)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		keys = append(keys, fmt.Sprint(row.Key))
	}
	sort.Strings(keys)
	return keys
}

func TestIntegrationNativeFullTextTokenizationAndUpdates(t *testing.T) {
	requireNativeSearch(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{NativeIndexes: true})
	title := `Title."雪`
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: title, StorageName: "headline", Type: vectordata.TypeString, Optional: true, IsIndexed: true, IsFullTextIndexed: true}, {Name: "Body", Type: vectordata.TypeString, Optional: true, IsFullTextIndexed: true}, {Name: "Plain", Type: vectordata.TypeString}}}
	base, err := s.EnsureModelCollection(ctx, "text", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := base.(*Collection)
	titles := []any{"Running gophers, CAFÉ", "run Gopher", nil, "", "the or and", "red-blue", "nul\x00byte", "symbols } | *"}
	bodies := []any{"private orchard", "Elsewhere", "gopher orchard", "東京 雪", nil, "separator", "unicode café", "literal"}
	rows := make([]vectordata.ModelRecord, len(titles))
	for i := range rows {
		rows[i] = vectordata.ModelRecord{Key: int64(i + 1), Data: map[string]any{title: titles[i], "Body": bodies[i], "Plain": "unindexed marker"}}
	}
	if _, err := c.Insert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		query string
		term  string
		want  []string
	}{
		{"@text0:($term)", "gopher", []string{"1", "2"}},
		{"@text0:($term)", "running", []string{"1", "2"}},
		{"@text0:($term)", "café", []string{"1"}},
		{"@text1:($term)", "gopher", []string{"3"}},
		{"@text1:($term)", "雪", []string{"4"}},
		{"@text1:($term)", "café", []string{"7"}},
		{"@text0:($term)", "the", []string{}},
		{"@text0:(red blue)", "", []string{"6"}},
	} {
		var params []any
		if tc.term != "" {
			params = []any{"term", tc.term}
		}
		if got := fullTextKeys(t, c, tc.query, params...); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s term %q: %v want %v", tc.query, tc.term, got, tc.want)
		}
	}
	m, _ := currentManifest(t, c)
	for _, key := range []int{3, 4, 7} {
		r, err := c.Get(ctx, int64(key), vectordata.RetrievalOptions{})
		if err != nil || r.Data[title] != titles[key-1] {
			t.Fatalf("canonical NULL/empty/NUL text changed: %+v %v", r, err)
		}
		projected := c.generationPrefix(m.Revision) + "r:" + encodeName(token(int64(key)))
		present, err := s.client.HExists(ctx, projected, "d_"+encodeName(title)).Result()
		if err != nil || present != (key != 3) {
			t.Fatalf("NULL/empty projection: key=%d present=%t %v", key, present, err)
		}
	}
	native, full := 0, 0
	s.client.AddHook(processHook{run: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		if scriptIs(cmd, nativeExecute) {
			native++
		}
		if scriptIs(cmd, readHash) {
			full++
		}
		return next(ctx, cmd)
	}})
	for _, value := range []string{"Gopher", "red", "run Gopher", "", "nul\x00byte"} {
		want := int64(0)
		if value == "run Gopher" || value == "" || value == "nul\x00byte" {
			want = 1
		}
		if count, err := c.Count(ctx, vectordata.Eq(vectordata.Column(title), value)); err != nil || count != want {
			t.Fatalf("TEXT changed literal equality %q: %d %v", value, count, err)
		}
	}
	if native != 5 || full != 0 {
		t.Fatalf("dual-index equality lost native TAG path: native=%d full=%d", native, full)
	}
	if count, err := c.Count(ctx, vectordata.Eq(vectordata.Column("Body"), "gopher")); err != nil || count != 0 || full == 0 {
		t.Fatalf("TEXT-only equality lost exact fallback: %d %v reads=%d", count, err, full)
	}
	if _, ok := any(c).(vectordata.HybridSearcher); ok || s.Capabilities().HybridSearch {
		t.Fatal("full-text indexing unexpectedly enabled hybrid search")
	}
	rows[0].Data[title] = "replacement galaxy"
	if _, err := c.Upsert(ctx, rows[:1]); err != nil {
		t.Fatal(err)
	}
	if got := fullTextKeys(t, c, "@text0:gopher"); !reflect.DeepEqual(got, []string{"2"}) {
		t.Fatalf("retired text remained searchable: %v", got)
	}
	if err := s.client.Do(ctx, "FT.INFO", m.Index).Err(); err == nil {
		t.Fatal("retired full-text index remained")
	}
	if _, err := c.Delete(ctx, []any{int64(2), int64(3)}); err != nil {
		t.Fatal(err)
	}
	if got := fullTextKeys(t, c, "gopher"); len(got) != 0 {
		t.Fatalf("deleted text remained searchable: %v", got)
	}
}

func TestIntegrationNativeFullTextLegacyIndexAndRebuild(t *testing.T) {
	requireNativeSearch(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{NativeIndexes: true})
	c := nativeLifecycleCollection(t, s)
	// Emulate the previous native writer: the persisted registration retains the
	// full-text hint, but its physical projection/schema ignores that hint.
	old := *c
	old.definition = c.definition.Clone()
	old.definition.Data[0].IsFullTextIndexed = false
	if _, err := old.Upsert(ctx, nativeLifecycleRows("legacy")); err != nil {
		t.Fatal(err)
	}
	m, _ := currentManifest(t, c)
	if m.IndexVersion != 1 || m.Definition != c.registration {
		t.Fatalf("legacy fixture registration/version: %+v", m)
	}
	full := 0
	s.client.AddHook(processHook{run: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		if scriptIs(cmd, readHash) {
			full++
		}
		return next(ctx, cmd)
	}})
	if count, err := c.Count(ctx, vectordata.Eq(vectordata.Column("Value"), "legacy")); err != nil || count != 2 || full != 0 {
		t.Fatalf("legacy native index became unreadable: %d %v reads=%d", count, err, full)
	}
	if err := c.RebuildNativeIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	if got := fullTextKeys(t, c, "@text0:legacy"); !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Fatalf("explicit TEXT rebuild failed: %v", got)
	}
	if _, err := old.Upsert(ctx, nativeLifecycleRows("older")); err != nil {
		t.Fatal(err)
	}
	if count, err := c.Count(ctx, nil); err != nil || count != 2 {
		t.Fatalf("older writer damaged current reader: %d %v", count, err)
	}
	if _, err := c.Upsert(ctx, nativeLifecycleRows("modern")); err != nil {
		t.Fatal(err)
	}
	if got := fullTextKeys(t, c, "@text0:modern"); !reflect.DeepEqual(got, []string{"1", "2"}) {
		t.Fatalf("native write did not restore TEXT indexes: %v", got)
	}
}

func TestIntegrationNativeFullTextStorageUpgradeAndReopen(t *testing.T) {
	requireNativeSearch(t)
	ctx := context.Background()
	for _, mode := range []StorageMode{StorageSnapshot, StorageHash} {
		t.Run(string(mode), func(t *testing.T) {
			plain := integrationStore(t, StoreOptions{StorageMode: mode})
			c := nativeLifecycleCollection(t, plain)
			keys, err := c.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Value": "retained"}}})
			if err != nil || len(keys) != 1 {
				t.Fatalf("initial generated record: %v %v", keys, err)
			}
			native, err := New(StoreOptions{Addr: integrationAddr, Namespace: plain.metadata.Namespace, NativeIndexes: true})
			if err != nil {
				t.Fatal(err)
			}
			defer native.Close()
			base, err := native.GetModelCollection(c.name, c.definition, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			indexed := base.(*Collection)
			if mode == StorageSnapshot {
				err = indexed.MigrateToHashStorage(ctx)
			} else {
				err = indexed.RebuildNativeIndexes(ctx)
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := fullTextKeys(t, indexed, "@text0:retained"); !reflect.DeepEqual(got, []string{fmt.Sprint(keys[0])}) {
				t.Fatalf("storage conversion lost TEXT: %v", got)
			}
			if r, err := c.Get(ctx, keys[0], vectordata.RetrievalOptions{}); err != nil || r.Data["Value"] != "retained" {
				t.Fatalf("plain reader could not reopen version 2: %+v %v", r, err)
			}
			next, err := indexed.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Value": "next"}}})
			if err != nil || len(next) != 1 || next[0] != keys[0].(int64)+1 {
				t.Fatalf("generated sequence changed: %v %v", next, err)
			}
		})
	}
}

func TestIntegrationNativeFullTextSchemaDrift(t *testing.T) {
	requireNativeSearch(t)
	ctx := context.Background()
	for _, change := range []string{"type", "weight", "nostem", "noindex", "nofields", "nooffsets", "nofreqs", "stopwords"} {
		t.Run(change, func(t *testing.T) {
			s := integrationStore(t, StoreOptions{NativeIndexes: true})
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Title", Type: vectordata.TypeString, IsFullTextIndexed: true}}}
			base, err := s.EnsureModelCollection(ctx, "drift", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			c := base.(*Collection)
			if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "one", Data: map[string]any{"Title": "running"}}}); err != nil {
				t.Fatal(err)
			}
			m, _ := currentManifest(t, c)
			if err := s.client.Do(ctx, "FT.DROPINDEX", m.Index).Err(); err != nil {
				t.Fatal(err)
			}
			args := []any{"FT.CREATE", m.Index, "ON", "HASH", "PREFIX", 1, c.generationPrefix(m.Revision)}
			switch change {
			case "nofields":
				args = append(args, "NOFIELDS")
			case "nooffsets":
				args = append(args, "NOOFFSETS")
			case "nofreqs":
				args = append(args, "NOFREQS")
			case "stopwords":
				args = append(args, "STOPWORDS", 0)
			}
			args = append(args, "SCHEMA", "__vs_revision", "AS", "revision", "TAG", "CASESENSITIVE", "__vs_key_tag", "AS", "key", "TAG", "CASESENSITIVE", "d_"+encodeName("Title"), "AS", "text0")
			if change == "type" {
				args = append(args, "TAG", "CASESENSITIVE")
			} else {
				args = append(args, "TEXT")
				switch change {
				case "weight":
					args = append(args, "WEIGHT", 2)
				case "nostem":
					args = append(args, "NOSTEM")
				case "noindex":
					args = append(args, "NOINDEX")
				}
			}
			if err := s.client.Do(ctx, args...).Err(); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Count(ctx, nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("TEXT drift %q accepted: %v", change, err)
			}
			if err := c.RebuildNativeIndexes(ctx); err != nil {
				t.Fatal(err)
			}
			if got := fullTextKeys(t, c, "@text0:run"); !reflect.DeepEqual(got, []string{"one"}) {
				t.Fatalf("TEXT rebuild did not repair drift: %v", got)
			}
		})
	}
}

func TestIntegrationNativeFullTextFieldLimit(t *testing.T) {
	requireNativeSearch(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{NativeIndexes: true})
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}}
	data := map[string]any{}
	for i := range 128 {
		name := fmt.Sprintf("Text%d", i)
		d.Data = append(d.Data, vectordata.DataProperty{Name: name, Type: vectordata.TypeString, IsFullTextIndexed: true})
		data[name] = "needle"
	}
	base, err := s.EnsureModelCollection(ctx, "limit", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := base.(*Collection)
	if _, err := c.Insert(ctx, []vectordata.ModelRecord{{Key: "one", Data: data}}); err != nil {
		t.Fatal(err)
	}
	if got := fullTextKeys(t, c, "@text127:needle"); !reflect.DeepEqual(got, []string{"one"}) {
		t.Fatalf("last supported TEXT field was not indexed: %v", got)
	}
}

func TestIntegrationNativeFullTextPublicationConflict(t *testing.T) {
	requireNativeSearch(t)
	ctx := context.Background()
	s := integrationStore(t, StoreOptions{NativeIndexes: true})
	c := nativeLifecycleCollection(t, s)
	other, err := New(StoreOptions{Addr: integrationAddr, Namespace: s.metadata.Namespace, NativeIndexes: true})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	writer, err := other.GetModelCollection(c.name, c.definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	s.client.AddHook(hashFailureHook{process: func(ctx context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
		if scriptIs(cmd, publishHash) {
			attempts++
			if attempts == 1 {
				if _, err := writer.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Value": "winner"}}}); err != nil {
					return err
				}
			}
		}
		return next(ctx, cmd)
	}})
	keys, err := c.Insert(ctx, []vectordata.ModelRecord{{Data: map[string]any{"Value": "contender"}}})
	if err != nil || !reflect.DeepEqual(keys, []any{int64(2)}) || attempts != 2 {
		t.Fatalf("TEXT publication conflict: %v %v attempts=%d", keys, err, attempts)
	}
	for term, key := range map[string]string{"winner": "1", "contender": "2"} {
		if got := fullTextKeys(t, c, "@text0:($term)", "term", term); !reflect.DeepEqual(got, []string{key}) {
			t.Fatalf("TEXT publication lost %s: %v", term, got)
		}
	}
	indexes, err := s.client.Do(ctx, "FT._LIST").StringSlice()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, index := range indexes {
		if strings.HasPrefix(index, s.indexPrefix(c.name)) {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("conflicting TEXT index was not retired: %d", count)
	}
}
