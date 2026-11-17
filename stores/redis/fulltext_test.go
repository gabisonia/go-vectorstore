package redis

import (
	"errors"
	"fmt"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestNativeFullTextSchemaAndCompatibility(t *testing.T) {
	for _, mode := range []StorageMode{StorageHash, StorageJSON} {
		t.Run(string(mode), func(t *testing.T) { testNativeFullTextSchemaAndCompatibility(t, mode) })
	}
}

func testNativeFullTextSchemaAndCompatibility(t *testing.T, mode StorageMode) {
	s, err := New(StoreOptions{NativeIndexes: true, StorageMode: mode})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	name := `title."[*]雪`
	d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: name, Type: vectordata.TypeString, IsIndexed: true, IsFullTextIndexed: true}, {Name: "Body", Type: vectordata.TypeString, Optional: true, IsFullTextIndexed: true}, {Name: "Plain", Type: vectordata.TypeString}}}
	base, err := s.GetModelCollection("text", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	c := base.(*Collection)
	if c.nativeIndexVersion() != 2 || c.nativeJSONIndexVersion() != 5 || !s.Capabilities().FullTextIndexing || !s.Capabilities().HybridSearch || s.Capabilities().HybridKeywordTerms {
		t.Fatal("full-text capability/version mismatch")
	}
	versions, format := []int{1, 2}, 2
	if mode == StorageJSON {
		versions, format = []int{3, 4, 5}, 3
	}
	for _, version := range versions {
		fields, err := c.nativeFields(hashManifest{Format: format, IndexVersion: version})
		if err != nil {
			t.Fatal(err)
		}
		text := map[string]string{}
		for _, f := range fields {
			if f.Kind == "TEXT" {
				text[f.Alias] = f.Name
			}
		}
		titlePath, bodyPath := "d_"+encodeName(name), "d_"+encodeName("Body")
		if mode == StorageJSON {
			titlePath, bodyPath = "$.__vs_text.text0", "$.__vs_text.text1"
		}
		hasText := version == 2 || version == 5
		if !hasText && len(text) != 0 || hasText && (len(text) != 2 || text["text0"] != titlePath || text["text1"] != bodyPath) {
			t.Fatalf("version %d text schema: %v", version, text)
		}
	}
	plan, supported, err := c.compileNativeFilter(vectordata.Eq(vectordata.Column(name), "HELLO, world"))
	if err != nil || !supported || plan.query != "@t0:{$p0}" {
		t.Fatalf("full-text changed exact scalar filtering: %+v %v", plan, err)
	}
	if _, supported, err := c.compileNativeFilter(vectordata.Eq(vectordata.Column("Body"), "word")); err != nil || supported {
		t.Fatalf("TEXT-only property enabled scalar pushdown: %t %v", supported, err)
	}
	d.Data = make([]vectordata.DataProperty, 128)
	for i := range d.Data {
		d.Data[i] = vectordata.DataProperty{Name: fmt.Sprintf("Text%d", i), Type: vectordata.TypeString, IsFullTextIndexed: true}
	}
	// The supported boundary is checked locally, including when fields are TEXT
	// only. No server is available to this constructor/metadata test.
	if _, err := s.GetModelCollection("limit", d, vectordata.ModelCollectionOptions{}); err != nil {
		t.Fatalf("128 TEXT properties rejected: %v", err)
	}
	d.Data = append(d.Data, vectordata.DataProperty{Name: "Extra", Type: vectordata.TypeString, IsFullTextIndexed: true})
	if _, err := s.GetModelCollection("limit", d, vectordata.ModelCollectionOptions{}); !errors.Is(err, vectordata.ErrUnsupported) {
		t.Fatalf("129 TEXT properties accepted: %v", err)
	}
}
