package vectordata_test

import (
	"bytes"
	"context"
	"encoding/json"
	"math"
	"math/big"
	"reflect"
	"sort"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func FuzzDecimal(f *testing.F) {
	for _, seed := range []string{"0", "-0e1000000000", "+001.2300e+2", "1e-28", "9007199254740993.000000001", "79228162514264337593543950336", "1e2147483648", "", "null"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		if len(text) > 4096 {
			return
		}
		value, err := vectordata.ParseDecimal(text)
		if err != nil {
			return
		}
		again, err := vectordata.ParseDecimal(value.String())
		if err != nil || again != value {
			t.Fatalf("unstable canonical value %q: %v", value, err)
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var decoded vectordata.Decimal
		if err := json.Unmarshal(raw, &decoded); err != nil || decoded != value {
			t.Fatalf("JSON changed exact decimal: %s %v", raw, err)
		}
		// Nonzero accepted decimals cannot have huge exponents at this input bound.
		// Avoid expanding a zero with an arbitrarily large exponent in math/big.
		if value != "0" {
			a, ok := new(big.Rat).SetString(text)
			if !ok {
				t.Fatal("accepted nonnumeric decimal")
			}
			b, ok := new(big.Rat).SetString(value.String())
			if !ok || a.Cmp(b) != 0 {
				t.Fatalf("decimal rounded: %q -> %q", text, value)
			}
		}
	})
}
func FuzzFloat16(f *testing.F) {
	for _, seed := range []uint32{0, 0x80000000, 0x3f800000, 0x38800000, 0x477fe000, 0x477ff000, 0x7f800000, 0x7fc00000, 1} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, bits uint32) {
		x := math.Float32frombits(bits)
		p := vectordata.VectorProperty{Dimensions: 1, Metric: vectordata.DistanceL2, Precision: vectordata.VectorFloat16}
		got, err := vectordata.NormalizeModelVector([]float32{x}, p)
		abs := math.Abs(float64(x))
		if math.IsNaN(abs) || math.IsInf(abs, 0) || abs >= 65520 {
			if err == nil {
				t.Fatalf("accepted float16 overflow/nonfinite %g", x)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		// Independent reference: search the ordered set of finite binary16 values,
		// then choose the nearest value with an even mantissa on a tie.
		upper := sort.Search(0x7c00, func(i int) bool { return float64(halfValue(i)) >= abs })
		index := upper
		if upper == 0x7c00 {
			index = upper - 1
		} else if upper > 0 {
			low := abs - float64(halfValue(upper-1))
			high := float64(halfValue(upper)) - abs
			if low < high || low == high && (upper-1)&1 == 0 {
				index = upper - 1
			}
		}
		want := float32(math.Copysign(float64(halfValue(index)), float64(x)))
		if math.Float32bits(got[0]) != math.Float32bits(want) {
			t.Fatalf("round(%g)=%g want %g", x, got[0], want)
		}
	})
}
func FuzzSparseDistance(f *testing.F) {
	f.Add([]byte{8, 0, 8, 2, 4, 7, 255})
	f.Add([]byte{2})
	f.Add([]byte{3, 2, 1, 1, 2})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) == 0 || len(data) > 512 {
			return
		}
		dimensions := 2 + int(data[0]%31)
		var indices []int
		var values []float32
		for i := 1; i+1 < len(data); i += 2 {
			indices = append(indices, int(data[i])%dimensions)
			values = append(values, float32(int8(data[i+1]))/8)
		}
		a, err := vectordata.NewSparseVector(dimensions, indices, values)
		if err != nil {
			return
		}
		b, err := vectordata.NewSparseVector(dimensions, []int{0, dimensions - 1}, []float32{1, -.5})
		if err != nil {
			t.Fatal(err)
		}
		dense := make([]float64, dimensions)
		for i, index := range a.Indices {
			dense[index] = float64(a.Values[i])
		}
		var squared, l1, dot, aa, bb float64
		for i, x := range dense {
			var y float64
			if i == 0 {
				y = 1
			}
			if i == dimensions-1 {
				y = -.5
			}
			delta := x - y
			squared += delta * delta
			l1 += math.Abs(delta)
			dot += x * y
			aa += x * x
			bb += y * y
		}
		expected := map[vectordata.DistanceMetric]float64{vectordata.DistanceL2: math.Sqrt(squared), vectordata.DistanceL1: l1, vectordata.DistanceInnerProduct: -dot}
		if aa != 0 {
			expected[vectordata.DistanceCosine] = 1 - math.Max(-1, math.Min(1, dot/(math.Sqrt(aa)*math.Sqrt(bb))))
		}
		for metric, want := range expected {
			got, err := vectordata.SparseDistance(a, b, metric)
			if err != nil || math.Abs(got-want) > 1e-12 {
				t.Fatalf("%s sparse=%g dense=%g error=%v", metric, got, want, err)
			}
		}
	})
}
func FuzzStructCodec(f *testing.F) {
	f.Add("document", int64(9007199254740993), []byte(`{"n":9007199254740993,"null":null}`))
	f.Add("", int64(-1), []byte(`null`))
	f.Add("x", int64(0), []byte(`[]`))
	type model struct {
		ID      string    `vectordata:"key"`
		Rank    int64     `vectordata:"data"`
		Details any       `vectordata:"data,type=json,optional"`
		Vector  []float32 `vectordata:"vector,dimensions=2,precision=float16"`
	}
	codec, err := vectordata.NewStructCodec[model](nil)
	if err != nil {
		f.Fatal(err)
	}
	d := codec.Definition()
	f.Fuzz(func(t *testing.T, key string, rank int64, data []byte) {
		if len(key) > 1024 || len(data) > 8192 {
			return
		}
		var value any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			return
		}
		r, err := codec.Encode(model{ID: key, Rank: rank, Details: value, Vector: []float32{1.1, 2.2}})
		if err != nil {
			t.Fatal(err)
		}
		prepared, err := vectordata.PrepareModelRecords(context.Background(), d, vectordata.ModelCollectionOptions{}, []vectordata.ModelRecord{r})
		if err != nil {
			return
		}
		decoded, err := codec.Decode(prepared[0])
		if err != nil {
			t.Fatal(err)
		}
		next, err := codec.Encode(decoded)
		if err != nil {
			t.Fatal(err)
		}
		again, err := vectordata.PrepareModelRecords(context.Background(), d, vectordata.ModelCollectionOptions{}, []vectordata.ModelRecord{next})
		if err != nil || !reflect.DeepEqual(again, prepared) {
			t.Fatalf("codec round trip changed canonical record: %v", err)
		}
	})
}
func FuzzModelFilter(f *testing.F) {
	f.Add(int64(1), int64(2), "a", byte(0))
	f.Add(int64(9007199254740993), int64(9007199254740992), "' OR true --", byte(3))
	d, err := (vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID", Type: vectordata.KeyInt64}, Data: []vectordata.DataProperty{{Name: "N", Type: vectordata.TypeInt64, Optional: true}, {Name: "J", Type: vectordata.TypeJSON, Optional: true}}}).Normalize()
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, actual, needle int64, path string, mode byte) {
		if len(path) > 1024 {
			return
		}
		record := vectordata.ModelRecord{Key: int64(1), Data: map[string]any{"N": actual, "J": map[string]any{path: json.Number(new(big.Int).SetInt64(actual).String())}}}
		ref := vectordata.Column("N")
		var value any = needle
		if mode&1 != 0 {
			ref = vectordata.DataPath("J", path)
			value = json.Number(new(big.Int).SetInt64(needle).String())
		}
		if mode&2 != 0 {
			delete(record.Data, "N")
			record.Data["J"] = map[string]any{}
		}
		eq := vectordata.Eq(ref, value)
		gt := vectordata.Gt(ref, value)
		for _, test := range []struct {
			filter vectordata.Filter
			want   bool
		}{
			{eq, mode&2 == 0 && actual == needle}, {gt, mode&2 == 0 && actual > needle},
			{vectordata.And(eq, vectordata.Not(eq)), false},
			{vectordata.Or(eq, vectordata.Not(eq)), mode&2 == 0},
		} {
			got, err := vectordata.MatchModelFilter(test.filter, record, d)
			if err != nil || got != test.want {
				t.Fatalf("filter truth/precision: got=%t want=%t error=%v", got, test.want, err)
			}
		}
	})
}
