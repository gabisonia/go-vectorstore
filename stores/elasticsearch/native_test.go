package elasticsearch

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestNativeResponses(t *testing.T) {
	for _, test := range []struct {
		name, operation, reply string
		status                 int
		want                   error
	}{
		{"mismatched write acknowledgement", "write", `{"_id":"other","result":"updated"}`, 200, ErrNativeOutcomeUnknown},
		{"lost write acknowledgement", "write", "", 503, ErrNativeOutcomeUnknown},
		{"missing document", "delete", "", 404, &ResponseError{404}},
		{"empty results", "search", `{"hits":{"hits":[]}}`, 200, nil},
		{"missing score", "search", `{"hits":{"hits":[{"_id":"key"}]}}`, 200, vectordata.ErrInvalidRecord},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/docs/_mapping" {
					io.WriteString(w, `{"docs":{"mappings":{"properties":{"body":{"type":"text"},"vector":{"type":"dense_vector","dims":2,"similarity":"cosine"}}}}}`)
					return
				}
				w.WriteHeader(test.status)
				io.WriteString(w, test.reply)
			}))
			defer server.Close()
			ctx := t.Context()
			index, err := AttachNative(ctx, server.URL, NativeSchema{"docs", "body", "vector", 2}, nil)
			if err != nil {
				t.Fatal(err)
			}
			switch test.operation {
			case "write":
				err = index.Upsert(ctx, NativeDocument{"key", "text", []float32{1, 0}})
			case "delete":
				err = index.Delete(ctx, "key")
			case "search":
				_, err = index.Search(ctx, []float32{1, 0}, 1, 10)
			}
			if want, ok := test.want.(*ResponseError); ok {
				var status *ResponseError
				if !errors.As(err, &status) || status.StatusCode != want.StatusCode || errors.Is(err, ErrNativeOutcomeUnknown) {
					t.Fatalf("unexpected delete error: %v", err)
				}
			} else if !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
}
