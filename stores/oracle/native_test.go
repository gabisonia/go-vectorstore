package oracle

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestValidationBeforeIO(t *testing.T) {
	c := &NativeCollection{dimensions: 2}
	for _, v := range [][]float32{nil, {1}, {0, 0}, {1, float32(math.Inf(1))}} {
		if err := c.Upsert(t.Context(), NativePoint{Vector: v}); err == nil {
			t.Fatalf("accepted vector %v", v)
		}
	}
	if _, err := c.Search(t.Context(), []float32{1, 0}, 0, nil); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatal(err)
	}
	if got := quote(`case"Sensitive`); got != `"case""Sensitive"` {
		t.Fatalf("identifier: %s", got)
	}
}

// The caller registers an Oracle database/sql driver and supplies the database pool.
func ExampleAttachNative() {
	search := func(ctx context.Context, db *sql.DB) ([]NativeHit, error) {
		articles, err := AttachNative(ctx, db, NativeSchema{
			Table: "ARTICLES", Key: "ID", Category: "CATEGORY", Vector: "DIRECTION", Dimensions: 2,
		})
		if err != nil {
			return nil, err
		}
		category := int32(1)
		return articles.Search(ctx, []float32{1, 0}, 5, &category)
	}
	_ = search
}
