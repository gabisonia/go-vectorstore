package redis

import (
	"errors"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestTextIndexAnnotations(t *testing.T) {
	p := vectordata.DataProperty{Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: true}
	configured, err := WithTextIndexOptions(p, TextIndexOptions{NoStem: true})
	if err != nil || p.ProviderAnnotations != "" || configured.ProviderAnnotations == "" {
		t.Fatalf("options mutated input: %+v %v", configured, err)
	}
	options, err := GetTextIndexOptions(configured)
	if err != nil || !options.NoStem {
		t.Fatalf("typed options: %+v %v", options, err)
	}
	reset, err := WithTextIndexOptions(configured, TextIndexOptions{})
	if err != nil || reset != p {
		t.Fatalf("default registration changed: %+v %v", reset, err)
	}
	for _, native := range []bool{false, true} {
		s, err := New(StoreOptions{NativeIndexes: native})
		if err != nil {
			t.Fatal(err)
		}
		d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{configured}}
		base, err := s.GetModelCollection("annotations", d, vectordata.ModelCollectionOptions{})
		if err != nil {
			t.Fatal(err)
		}
		fields, err := base.(*Collection).nativeFields(hashManifest{Format: 2, IndexVersion: 2})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, f := range fields {
			if f.Kind == "TEXT" {
				found = true
				if !f.NoStem {
					t.Fatal("lost NOSTEM field")
				}
			}
		}
		if !found {
			t.Fatal("missing TEXT field")
		}
		s.Close()
	}
}

func TestRedisAnnotationValidationBeforeIO(t *testing.T) {
	for _, native := range []bool{false, true} {
		s, err := New(StoreOptions{NativeIndexes: native})
		if err != nil {
			t.Fatal(err)
		}
		for _, raw := range []string{`{"redis.text.nostem":"true"}`, `{"redis.text.nostem":null}`, `{"redis.text.nostem":1}`, `{"redis.unknown":true}`} {
			a, err := vectordata.ParseProviderAnnotations([]byte(raw))
			if err != nil {
				t.Fatal(err)
			}
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: true, ProviderAnnotations: a}}}
			if _, err := s.GetModelCollection("invalid", d, vectordata.ModelCollectionOptions{}); !errors.Is(err, vectordata.ErrSchemaMismatch) && !errors.Is(err, vectordata.ErrUnsupported) {
				t.Fatalf("accepted %s: %v", raw, err)
			}
		}
		a, _ := vectordata.WithProviderAnnotation(vectordata.ProviderAnnotations(""), TextNoStemAnnotation, true)
		for _, d := range []vectordata.CollectionDefinition{
			{Key: vectordata.KeyProperty{Name: "ID", ProviderAnnotations: a}},
			{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "Body", Type: vectordata.TypeString, ProviderAnnotations: a}}},
			{Key: vectordata.KeyProperty{Name: "ID"}, Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2, ProviderAnnotations: a}}},
		} {
			if _, err := s.GetModelCollection("misplaced", d, vectordata.ModelCollectionOptions{}); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("misplaced option: %v", err)
			}
		}
		s.Close()
	}
}
