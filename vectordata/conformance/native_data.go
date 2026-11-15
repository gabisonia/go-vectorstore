package conformance

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

const nativeUUIDA = "8f516db6-38ca-4e8f-ad79-d71b015f5419"
const nativeUUIDB = "9f516db6-38ca-4e8f-ad79-d71b015f5419"

type nativeUUID struct{ Text string }

func (u nativeUUID) MarshalText() ([]byte, error)     { return []byte(u.Text), nil }
func (u *nativeUUID) UnmarshalText(text []byte) error { u.Text = string(text); return nil }

type nativeBytes []byte
type nativeDataModel struct {
	ID      string      `vectordata:"key"`
	Owner   nativeUUID  `vectordata:"data,type=uuid,index"`
	Backup  *string     `vectordata:"data,type=uuid"`
	Payload nativeBytes `vectordata:"data,type=bytes,optional,index"`
	Legacy  []byte      `vectordata:"data"`
}

func runNativeData(t *testing.T, factory Factory, caps vectordata.ProviderCapabilities) {
	if !slices.Contains(caps.DataTypes, vectordata.TypeUUID) || !slices.Contains(caps.DataTypes, vectordata.TypeBytes) {
		t.Skip("native UUID/byte data not advertised")
	}
	ctx := context.Background()
	store := factory(t)
	d := vectordata.CollectionDefinition{
		Key: vectordata.KeyProperty{Name: "ID"},
		Data: []vectordata.DataProperty{
			{Name: "Owner", StorageName: `owner";--`, Type: vectordata.TypeUUID, Optional: true, IsIndexed: true},
			{Name: "Payload", StorageName: "payload.raw", Type: vectordata.TypeBytes, Optional: true, IsIndexed: true},
			{Name: "Body", Type: vectordata.TypeString, IsFullTextIndexed: caps.FullTextIndexing},
			{Name: "Legacy", Type: vectordata.TypeJSON},
		},
		Vectors: []vectordata.VectorProperty{{Name: "Vector", Dimensions: 2}},
	}
	c, err := store.EnsureModelCollection(ctx, "native_data", d, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	buffer := []byte{0, 1, 255}
	rows := []vectordata.ModelRecord{
		{Key: "a", Data: map[string]any{"Owner": strings.ToUpper(nativeUUIDA), "Payload": buffer}},
		{Key: "b", Data: map[string]any{"Owner": nativeUUID{nativeUUIDB}, "Payload": []byte{}}},
		{Key: "c", Data: map[string]any{"Owner": nil, "Payload": []byte(nil)}},
		{Key: "d", Data: map[string]any{"Owner": nativeUUIDA, "Payload": nativeBytes{0, 1}}},
		{Key: "e", Data: map[string]any{"Owner": nil, "Payload": []byte{0, 0}}},
	}
	for i := range rows {
		rows[i].Data["Body"] = "alpha"
		rows[i].Data["Legacy"] = []byte{0, 1, 255}
		rows[i].Vectors = map[string][]float32{"Vector": {1, 0}}
	}
	if _, err := c.Insert(ctx, rows); err != nil {
		t.Fatal(err)
	}
	buffer[0] = 99
	row, err := c.Get(ctx, "a", vectordata.RetrievalOptions{})
	if err != nil || row.Data["Owner"] != nativeUUIDA || !reflect.DeepEqual(row.Data["Payload"], []byte{0, 1, 255}) || row.Data["Legacy"] != "AAH/" {
		t.Fatalf("native round trip: %v, %v", row, err)
	}
	row.Data["Payload"].([]byte)[0] = 99
	row, err = c.Get(ctx, "a", vectordata.RetrievalOptions{})
	if err != nil || row.Data["Payload"].([]byte)[0] != 0 {
		t.Fatalf("read buffer aliases storage: %v, %v", row, err)
	}
	owner, payload := vectordata.Column("Owner"), vectordata.Column("Payload")
	for _, tt := range []struct {
		name   string
		filter vectordata.Filter
		keys   []string
	}{
		{"UUID uppercase", vectordata.Eq(owner, strings.ToUpper(nativeUUIDA)), []string{"a", "d"}},
		{"UUID text marshaler", vectordata.Eq(owner, nativeUUID{strings.ToUpper(nativeUUIDA)}), []string{"a", "d"}},
		{"UUID membership", vectordata.In(owner, nativeUUID{nativeUUIDB}, nil), []string{"b"}},
		{"UUID negated membership with null", vectordata.Not(vectordata.In(owner, nativeUUIDA, nil)), nil},
		{"UUID greater", vectordata.Gt(owner, nativeUUIDA), []string{"b"}},
		{"UUID less", vectordata.Lt(owner, nativeUUIDB), []string{"a", "d"}},
		{"UUID null", vectordata.Eq(owner, nil), []string{"c", "e"}},
		{"UUID exists", vectordata.Exists(owner), []string{"a", "b", "d"}},
		{"bytes null", vectordata.Eq(payload, nil), []string{"c"}},
		{"bytes typed null", vectordata.Eq(payload, []byte(nil)), []string{"c"}},
		{"bytes empty", vectordata.Eq(payload, []byte{}), []string{"b"}},
		{"bytes membership", vectordata.In(payload, nativeBytes{0, 1}, nil), []string{"d"}},
		{"bytes negated membership with null", vectordata.Not(vectordata.In(payload, []byte{0, 1}, nil)), nil},
		{"bytes negated equality", vectordata.Not(vectordata.Eq(payload, []byte{0, 1})), []string{"a", "b", "e"}},
		{"bytes greater prefix", vectordata.Gt(payload, []byte{0, 1}), []string{"a"}},
		{"bytes at least", vectordata.Gte(payload, []byte{0, 1}), []string{"a", "d"}},
		{"bytes less", vectordata.Lt(payload, []byte{0, 1}), []string{"b", "e"}},
		{"bytes exists", vectordata.Exists(payload), []string{"a", "b", "d", "e"}},
		{"nested", vectordata.And(vectordata.Or(vectordata.Eq(owner, nativeUUIDA), vectordata.Eq(owner, nativeUUIDB)), vectordata.Not(vectordata.Eq(payload, []byte{}))), []string{"a", "d"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := c.Query(ctx, tt.filter, 10, vectordata.QueryOptions{})
			if err != nil {
				t.Fatal(err)
			}
			keys := make([]string, len(got))
			for i := range got {
				keys[i] = got[i].Key.(string)
			}
			slices.Sort(keys)
			if !slices.Equal(keys, tt.keys) {
				t.Fatalf("keys=%v, want=%v", keys, tt.keys)
			}
			if count, err := c.Count(ctx, tt.filter); err != nil || count != int64(len(tt.keys)) {
				t.Fatalf("count=%d, error=%v", count, err)
			}
		})
	}
	for _, tt := range []struct {
		property   string
		descending bool
		want       []string
	}{
		{"Owner", false, []string{"a", "d", "b", "c", "e"}}, {"Owner", true, []string{"b", "a", "d", "c", "e"}},
		{"Payload", false, []string{"b", "e", "d", "a", "c"}}, {"Payload", true, []string{"a", "d", "e", "b", "c"}},
	} {
		got, err := c.Query(ctx, nil, 5, vectordata.QueryOptions{OrderBy: []vectordata.SortField{{Property: tt.property, Descending: tt.descending}}})
		if err != nil {
			t.Fatal(err)
		}
		keys := make([]string, len(got))
		for i := range got {
			keys[i] = got[i].Key.(string)
		}
		if !slices.Equal(keys, tt.want) {
			t.Fatalf("ordering %s desc=%v: %v", tt.property, tt.descending, keys)
		}
	}
	for _, invalid := range []vectordata.Filter{vectordata.Eq(owner, "bad"), vectordata.Eq(payload, "base64"), vectordata.Contains(payload, byte(0)), vectordata.Gt(payload, []byte(nil)), vectordata.Eq(vectordata.DataPath("Owner", "x"), "x")} {
		if _, err := c.Query(ctx, invalid, 1, vectordata.QueryOptions{}); !errors.Is(err, vectordata.ErrInvalidFilter) {
			t.Fatalf("invalid native filter: %v", err)
		}
	}
	searchFilter := vectordata.Eq(owner, nativeUUID{strings.ToUpper(nativeUUIDA)})
	if got, err := c.Search(ctx, []float32{1, 0}, 10, vectordata.ModelSearchOptions{Filter: searchFilter}); err != nil || len(got) != 2 {
		t.Fatalf("filtered vector search: %v, %v", got, err)
	}
	if caps.HybridSearch {
		got, err := c.(vectordata.HybridSearcher).HybridSearch(ctx, []float32{1, 0}, "alpha", 10, vectordata.HybridSearchOptions{ModelSearchOptions: vectordata.ModelSearchOptions{Filter: searchFilter}})
		if err != nil || len(got) != 2 {
			t.Fatalf("filtered hybrid search: %v, %v", got, err)
		}
	}
	invalid := []vectordata.ModelRecord{{Key: "new", Data: map[string]any{"Owner": nativeUUIDA, "Payload": []byte{}, "Body": "alpha", "Legacy": 1}, Vectors: map[string][]float32{"Vector": {1, 0}}}, {Key: "a", Data: map[string]any{"Owner": "invalid", "Payload": []byte{}, "Body": "changed", "Legacy": 1}, Vectors: map[string][]float32{"Vector": {1, 0}}}}
	if _, err := c.Upsert(ctx, invalid); !errors.Is(err, vectordata.ErrInvalidRecord) {
		t.Fatalf("invalid UUID write: %v", err)
	}
	if count, err := c.Count(ctx, nil); err != nil || count != 5 {
		t.Fatalf("partial native write: %d, %v", count, err)
	}
	row, err = c.Get(ctx, "a", vectordata.RetrievalOptions{})
	if err != nil || row.Data["Owner"] != nativeUUIDA {
		t.Fatalf("invalid batch overwrote record: %v, %v", row, err)
	}
	// Typed native data is opt-in; the unannotated byte field retains its JSON type.
	typed, err := vectordata.EnsureTypedModelCollection[string, nativeDataModel](ctx, store, "typed_native", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	legacy := []byte{0, 1, 255}
	backup := strings.ToUpper(nativeUUIDB)
	if _, err := typed.Insert(ctx, []nativeDataModel{{ID: "a", Owner: nativeUUID{strings.ToUpper(nativeUUIDA)}, Backup: &backup, Payload: nativeBytes{}, Legacy: legacy}, {ID: "b", Owner: nativeUUID{nativeUUIDB}, Payload: nil, Legacy: legacy}}); err != nil {
		t.Fatal(err)
	}
	decoded, err := typed.Get(ctx, "a", vectordata.RetrievalOptions{})
	if err != nil || decoded.Owner.Text != nativeUUIDA || decoded.Payload == nil || len(decoded.Payload) != 0 || !reflect.DeepEqual(decoded.Legacy, legacy) || decoded.Backup == nil || *decoded.Backup != nativeUUIDB {
		t.Fatalf("typed native round trip: %+v, %v", decoded, err)
	}
	decoded, err = typed.Get(ctx, "b", vectordata.RetrievalOptions{})
	if err != nil || decoded.Payload != nil || decoded.Backup != nil {
		t.Fatalf("typed null bytes: %+v, %v", decoded, err)
	}
}
