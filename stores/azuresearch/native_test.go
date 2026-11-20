package azuresearch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestWriteOutcomes(t *testing.T) {
	const schema = `{"fields":[{"name":"Id","type":"Edm.String","key":true,"retrievable":true},{"name":"Text","type":"Edm.String","retrievable":true},{"name":"Vector","type":"Collection(Edm.Single)","dimensions":2,"searchable":true,"vectorSearchProfile":"dense"}],"vectorSearch":{"profiles":[{"name":"dense","algorithm":"flat"}],"algorithms":[{"name":"flat","kind":"exhaustiveKnn","exhaustiveKnnParameters":{"metric":"cosine"}}]}}`
	for _, tc := range []struct {
		name, reply string
		status      int
		unknown     bool
	}{
		{"acknowledged", `{"value":[{"key":"a","status":true}]}`, 200, false},
		{"wrong key", `{"value":[{"key":"b","status":true}]}`, 200, true},
		{"missing status", `{"value":[{"key":"a"}]}`, 200, true},
		{"lost reply", "", 200, true},
		{"rejected", `{"value":[{"key":"a","status":false,"statusCode":400}]}`, 207, false},
		{"forbidden", "", 403, false},
		{"server error", "", 503, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var writes atomic.Int32
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer test" || r.URL.Query().Get("api-version") != apiVersion {
					t.Error("missing credential or version")
				}
				if r.Method == "GET" {
					_, _ = w.Write([]byte(schema))
					return
				}
				writes.Add(1)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.reply))
			}))
			defer srv.Close()
			opts := Options{Endpoint: srv.URL, Token: func(context.Context) (string, error) { return "test", nil }, Transport: srv.Client().Transport}
			s := NativeSchema{Key: "Id", Text: "Text", Vector: "Vector", Dimensions: 3}
			if _, err := AttachNative(t.Context(), opts, "documents", s); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("wrong dimensions: %v", err)
			}
			s.Dimensions = 2
			c, err := AttachNative(t.Context(), opts, "documents", s)
			if err != nil {
				t.Fatal(err)
			}
			err = c.Upsert(t.Context(), NativeDocument{ID: "a", Text: "text", Vector: []float32{1, 0}})
			if (err == nil) != (tc.name == "acknowledged") || errors.Is(err, ErrNativeOutcomeUnknown) != tc.unknown || writes.Load() != 1 {
				t.Fatalf("err=%v writes=%d", err, writes.Load())
			}
		})
	}
}
