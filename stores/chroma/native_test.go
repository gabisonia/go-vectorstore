package chroma

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestNativeReadContract(t *testing.T) {
	const id = "313939d8-0101-4a0a-8000-000000000001"
	const base = "/api/v2/tenants/default_tenant/databases/default_database/collections"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case base + "/articles":
			fmt.Fprintf(w, `{"id":%q,"name":"articles","dimension":null,"configuration_json":{"hnsw":{"space":"cosine"}}}`, id)
		case base + "/" + id + "/get":
			var body struct{ IDs []string }
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.IDs) != 1 {
				t.Error("invalid get request")
				w.WriteHeader(400)
				return
			}
			switch body.IDs[0] {
			case "article":
				fmt.Fprint(w, `{"ids":["article"],"documents":["example"],"metadatas":[{"sequence":9007199254740993}],"embeddings":[[1,0]]}`)
			case "missing":
				fmt.Fprint(w, `{"ids":[]}`)
			default:
				fmt.Fprint(w, `{"ids":["other"]}`)
			}
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	c, err := AttachNative(t.Context(), srv.URL, NativeSchema{Collection: "articles", Dimensions: 2}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p, err := c.Get(t.Context(), "article")
	if err != nil || p.Metadata["sequence"] != json.Number("9007199254740993") {
		t.Fatalf("metadata: %+v, %v", p, err)
	}
	if _, err = c.Get(t.Context(), "missing"); !errors.Is(err, vectordata.ErrNotFound) {
		t.Fatalf("missing record: %v", err)
	}
	if _, err = c.Get(t.Context(), "invalid"); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatalf("invalid response: %v", err)
	}
	if err = c.Upsert(t.Context(), NativePoint{ID: "a", Embedding: []float32{1}}); !errors.Is(err, vectordata.ErrDimensionMismatch) {
		t.Fatalf("wrong dimensions: %v", err)
	}
}
