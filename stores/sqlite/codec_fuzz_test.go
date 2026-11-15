package sqlite

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func FuzzStoredRecord(f *testing.F) {
	d, err := (vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "J", Type: vectordata.TypeJSON, Optional: true}, {Name: "B", Type: vectordata.TypeBytes, Optional: true}, {Name: "D", Type: vectordata.TypeDecimal, Optional: true}}, Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 2, Optional: true}}}).Normalize()
	if err != nil {
		f.Fatal(err)
	}
	for _, seed := range []string{`{"Key":"a","Data":{"J":{"Value":{"n":9007199254740993}},"B":{"Value":"AP8="},"D":{"Value":0.1}},"Vectors":{"V":[1,0]}}`, `{"Key":"a","Data":{"J":{"Null":true},"B":{"Null":true},"D":{"Null":true}}}`, `null`, `{`} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > 16384 {
			return
		}
		record, err := decodeRecord(payload, d)
		if err != nil {
			return
		}
		prepared, err := vectordata.PrepareModelRecords(context.Background(), d, vectordata.ModelCollectionOptions{}, []vectordata.ModelRecord{record})
		if err != nil {
			return
		}
		encoded, err := encodeRecord(prepared[0])
		if err != nil {
			return
		} // Native invalid UTF-8 is rejected by persistence.
		next, err := decodeRecord(encoded, d)
		if err != nil {
			t.Fatal(err)
		}
		again, err := vectordata.PrepareModelRecords(context.Background(), d, vectordata.ModelCollectionOptions{}, []vectordata.ModelRecord{next})
		if err != nil {
			t.Fatal(err)
		}
		a, err := json.Marshal(prepared[0])
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(again[0])
		if err != nil || string(a) != string(b) {
			t.Fatalf("stored round trip changed data: %s -> %s, %v", a, b, err)
		}
	})
}
