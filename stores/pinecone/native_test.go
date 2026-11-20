package pinecone

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestNativeReplies(t *testing.T) {
	for _, tc := range []struct {
		name, reply, operation string
		want                   error
	}{
		{"REST delete acknowledgement", `{}`, "delete", nil},
		{"emulator delete acknowledgement", `""`, "delete", nil},
		{"error-shaped acknowledgement", `{"message":"failed"}`, "delete", ErrNativeOutcomeUnknown},
		{"wrong upsert count", `{"upsertedCount":0}`, "upsert", ErrNativeOutcomeUnknown},
		{"cross-namespace reply", `{"namespace":"other","vectors":{}}`, "get", vectordata.ErrInvalidRecord},
		{"missing ID", `{"namespace":"articles","vectors":{}}`, "get", vectordata.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("X-Pinecone-API-Version"); got != "2025-01" {
					t.Errorf("version %q", got)
				}
				if r.URL.Path == "/indexes/docs" {
					fmt.Fprintf(w, `{"name":"docs","host":%q,"metric":"cosine","dimension":2,"status":{"ready":true}}`, r.Host)
					return
				}
				fmt.Fprint(w, tc.reply)
			}))
			defer server.Close()
			s := NativeSchema{"docs", "articles", "body", 2}
			if _, err := AttachNative(t.Context(), server.URL, "https://unexpected.example", s, nil); !errors.Is(err, vectordata.ErrSchemaMismatch) {
				t.Fatalf("host mismatch: %v", err)
			}
			c, err := AttachNative(t.Context(), server.URL, server.URL, s, nil)
			if err != nil {
				t.Fatal(err)
			}
			switch tc.operation {
			case "delete":
				err = c.Delete(t.Context(), "id")
			case "upsert":
				err = c.Upsert(t.Context(), NativeDocument{"id", "keep", []float32{1, 0}})
			case "get":
				_, err = c.Get(t.Context(), "id")
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v want %v", err, tc.want)
			}
		})
	}
}

func ExampleAttachNative() {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/indexes/") {
			fmt.Fprintf(w, `{"name":"docs","host":%q,"metric":"cosine","dimension":2,"status":{"ready":true}}`, r.Host)
			return
		}
		fmt.Fprint(w, `{"namespace":"articles","matches":[{"id":"intro","score":1}]}`)
	}))
	defer server.Close()
	ctx := context.Background()
	c, err := AttachNative(ctx, server.URL, server.URL, NativeSchema{Index: "docs", Namespace: "articles", Text: "body", Dimensions: 2}, nil)
	if err != nil {
		panic(err)
	}
	body := "guide"
	hits, err := c.Search(ctx, []float32{1, 0}, 1, &body)
	if err != nil {
		panic(err)
	}
	fmt.Println(hits[0].ID)
	// Output: intro
}
