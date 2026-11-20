package couchbase

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

const index = `{"indexDef":{"type":"fulltext-index","sourceName":"parity","params":{
	"doc_config":{"mode":"scope.collection.type_field"},"mapping":{"types":{"docs.articles":{
	"enabled":true,"properties":{
	"body":{"enabled":true,"fields":[{"name":"body","type":"text","analyzer":"keyword","index":true}]},
	"direction":{"enabled":true,"fields":[{"name":"direction","type":"vector","similarity":"cosine","dims":2,"index":true}]}
	}}}}}}}`

func TestNativeReplies(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		run         func(*NativeCollection) error
		want        error
	}{
		{"wrong acknowledgement", `{"status":"success","results":["other"]}`, func(c *NativeCollection) error { return c.Delete(t.Context(), "id") }, ErrNativeOutcomeUnknown},
		{"lost acknowledgement", `{"status":"success"}`, func(c *NativeCollection) error { return c.Delete(t.Context(), "id") }, ErrNativeOutcomeUnknown},
		{"missing record", `{"status":"success","results":[]}`, func(c *NativeCollection) error { _, err := c.Get(t.Context(), "id"); return err }, vectordata.ErrNotFound},
		{"missing score", `{"status":{"failed":0},"hits":[{"id":"seed"}]}`, func(c *NativeCollection) error { _, err := c.Search(t.Context(), []float32{1, 0}, 1, nil); return err }, vectordata.ErrInvalidRecord},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					fmt.Fprint(w, index)
					return
				}
				fmt.Fprint(w, tc.reply)
			}))
			defer server.Close()
			c, err := AttachNative(t.Context(), server.URL, server.URL, NativeSchema{"parity", "docs", "articles", "vectors", "body", "direction", 2}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := tc.run(c); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func ExampleAttachNative() {
	// Supply a transport configured for both service origins in production.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fmt.Fprint(w, index)
			return
		}
		fmt.Fprint(w, `{"status":{"failed":0},"hits":[{"id":"intro","score":1}]}`)
	}))
	defer server.Close()
	ctx := context.Background()
	articles, err := AttachNative(ctx, server.URL, server.URL, NativeSchema{
		Bucket: "parity", Scope: "docs", Collection: "articles", Index: "vectors", Text: "body", Vector: "direction", Dimensions: 2,
	}, nil)
	if err != nil {
		panic(err)
	}
	body := "guide"
	hits, err := articles.Search(ctx, []float32{1, 0}, 1, &body)
	if err != nil {
		panic(err)
	}
	fmt.Println(hits[0].ID)
	// Output: intro
}
