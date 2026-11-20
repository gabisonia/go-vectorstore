package milvus_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/gabisonia/go-vectorstore/stores/milvus"
)

func ExampleAttachNative() {
	// An example Milvus REST endpoint describing an existing collection.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/vectordb/collections/describe":
			fmt.Fprint(w, `{"code":0,"data":{"collectionName":"articles","fields":[
				{"name":"id","type":"VarChar","primaryKey":true,"params":[{"key":"max_length","value":"32"}]},
				{"name":"body","type":"VarChar","params":[{"key":"max_length","value":"256"}]},
				{"name":"direction","type":"FloatVector","params":[{"key":"dim","value":"2"}]}],
				"indexes":[{"fieldName":"direction","metricType":"COSINE"}]}}`)
		case "/v2/vectordb/entities/search":
			fmt.Fprint(w, `{"code":0,"data":[{"id":"intro","distance":1}]}`)
		}
	}))
	defer server.Close()

	ctx := context.Background()
	articles, err := milvus.AttachNative(ctx, server.URL, milvus.NativeSchema{
		Collection: "articles", Key: "id", Text: "body", Vector: "direction", Dimensions: 2,
	}, nil)
	if err != nil {
		panic(err)
	}
	category := "guide"
	hits, err := articles.Search(ctx, []float32{1, 0}, 1, &category)
	if err != nil {
		panic(err)
	}
	fmt.Println(hits[0].ID, hits[0].Score)
	// Output: intro 1
}
