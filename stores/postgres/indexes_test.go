package postgres

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestGeneratedIndexNamesFitPostgresIdentifierLimit(t *testing.T) {
	first, err := resolveIndexName("", strings.Repeat("文", 30)+"_vector_hnsw")
	if err != nil || len(first) > 63 || !utf8.ValidString(first) {
		t.Fatalf("invalid generated name %q: %v", first, err)
	}
	second, err := resolveIndexName("", strings.Repeat("文", 30)+"_metadata_gin")
	if err != nil || first == second {
		t.Fatalf("generated names collide: %q, %q", first, second)
	}
	if _, err := resolveIndexName(strings.Repeat("x", 64), "unused"); !errors.Is(err, vectordata.ErrSchemaMismatch) {
		t.Fatalf("expected ErrSchemaMismatch, got %v", err)
	}
}
