package filtersql

import (
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata/filterast"
)

func FuzzBoundFilterOperands(f *testing.F) {
	f.Add("price", "' OR true; DROP TABLE docs; --", int64(9007199254740993))
	f.Add("", "null", int64(-1))
	f.Add(" a.b ", "\\\"", int64(0))
	f.Fuzz(func(t *testing.T, path, value string, number int64) {
		if len(path) > 1024 || len(value) > 4096 {
			return
		}
		build := func(p, v string, n int64) filterast.Filter {
			return filterast.And(filterast.Eq(filterast.Metadata(p), v), filterast.Gt(filterast.Metadata(p), n))
		}
		for _, containment := range []bool{false, true} {
			cfg := FilterSQLConfig{MetadataExpr: `"metadata"`, UseMetadataContainment: containment}
			query, args, next, err := CompileFilterSQL(build(path, value, number), cfg, 3)
			if err != nil {
				t.Fatal(err)
			}
			reference, referenceArgs, referenceNext, err := CompileFilterSQL(build(path, "sentinel", 1), cfg, 3)
			if err != nil {
				t.Fatal(err)
			}
			if query != reference || len(args) != len(referenceArgs) || next != referenceNext || next != 3+len(args) {
				t.Fatalf("operand changed SQL structure/placeholders: %s vs %s", query, reference)
			}
			again, againArgs, _, err := CompileFilterSQL(build(path, value, number), cfg, 3)
			if err != nil || query != again || !reflect.DeepEqual(args, againArgs) {
				t.Fatalf("nondeterministic compilation: %v", err)
			}
		}
	})
}
