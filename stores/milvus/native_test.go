package milvus

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

const description = `{"code":0,"data":{"collectionName":"docs","fields":[
	{"name":"key","type":"VarChar","primaryKey":true,"params":[{"key":"max_length","value":"16"}]},
	{"name":"body","type":"VarChar","nullable":true,"params":[{"key":"max_length","value":"32"}]},
	{"name":"vec","type":"FloatVector","params":[{"key":"dim","value":"2"}]}],
	"indexes":[{"fieldName":"vec","metricType":"COSINE"}]}}`

func TestNativeResponseContracts(t *testing.T) {
	for _, test := range []struct {
		name, schema, reply, operation string
		want                           error
	}{
		{"wrong metric", strings.Replace(description, "COSINE", "IP", 1), "", "attach", vectordata.ErrSchemaMismatch},
		{"implicit IDs", strings.Replace(description, `"fields":`, `"autoId":true,"fields":`, 1), "", "attach", vectordata.ErrSchemaMismatch},
		{"mismatched acknowledgement", description, `{"code":0,"data":{"upsertCount":1,"upsertIds":["other"]}}`, "upsert", ErrNativeOutcomeUnknown},
		{"absent success code", description, `{"data":{"deleteCount":1}}`, "delete", ErrNativeOutcomeUnknown},
		{"invalid write input", description, "", "invalid", vectordata.ErrInvalidRecord},
		{"absent document", description, `{"code":0,"data":[]}`, "get", vectordata.ErrNotFound},
		{"nullable text", description, `{"code":0,"data":[{"key":"k","body":null,"vec":[1,0]}]}`, "get", nil},
		{"omitted text", description, `{"code":0,"data":[{"key":"k","vec":[1,0]}]}`, "get", vectordata.ErrInvalidRecord},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/vectordb/collections/describe" {
					io.WriteString(w, test.schema)
					return
				}
				if test.reply == "" {
					t.Errorf("unexpected %s", r.URL.Path)
				}
				io.WriteString(w, test.reply)
			}))
			defer server.Close()
			ctx := t.Context()
			c, err := AttachNative(ctx, server.URL, NativeSchema{"docs", "key", "body", "vec", 2}, nil)
			if err == nil {
				switch test.operation {
				case "upsert":
					err = c.Upsert(ctx, NativeDocument{ID: "k", Vector: []float32{1, 0}})
				case "delete":
					err = c.Delete(ctx, "k")
				case "get":
					_, err = c.Get(ctx, "k")
				case "invalid":
					err = c.Upsert(ctx, NativeDocument{ID: strings.Repeat("x", 17), Vector: []float32{1, 0}})
				}
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}
}
