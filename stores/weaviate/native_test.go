package weaviate

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
)

func TestUpsertAcknowledgement(t *testing.T) {
	id := uuid.MustParse("313939d8-0101-4a0a-8000-000000000001")
	for _, tc := range []struct {
		name, body       string
		code             int
		success, unknown bool
	}{
		{"confirmed", fmt.Sprintf(`[{"id":%q,"result":{"status":"SUCCESS"}}]`, id), 200, true, false},
		{"object rejected", fmt.Sprintf(`[{"id":%q,"result":{"status":"FAILED","errors":{"error":[{"message":"invalid vector"}]}}}]`, id), 200, false, false},
		{"forbidden", "", 403, false, false},
		{"wrong ID", `[{"id":"313939d8-0101-4a0a-8000-000000000002","result":{"status":"SUCCESS"}}]`, 200, false, true},
		{"no receipt", "[]", 200, false, true},
		{"partial reply", "[", 200, false, true},
		{"server failure", "", 503, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var writes atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/schema/Article":
					fmt.Fprint(w, `{"class":"Article","properties":[{"name":"body","dataType":["text"]}],"vectorConfig":{"direction":{"vectorizer":{"none":{}},"vectorIndexType":"flat","vectorIndexConfig":{"distance":"cosine"}}}}`)
				case "/v1/batch/objects":
					writes.Add(1)
					w.WriteHeader(tc.code)
					fmt.Fprint(w, tc.body)
				default:
					t.Errorf("unexpected request %s", r.URL)
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			c, err := AttachNative(t.Context(), srv.URL, NativeSchema{Class: "Article", Text: "body", Vector: "direction", Dimensions: 2}, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = c.Upsert(t.Context(), NativePoint{ID: id, Text: "test", Vector: []float32{1, 0}})
			if (err == nil) != tc.success || errors.Is(err, ErrNativeOutcomeUnknown) != tc.unknown || writes.Load() != 1 {
				t.Fatalf("writes=%d err=%v", writes.Load(), err)
			}
		})
	}
}
