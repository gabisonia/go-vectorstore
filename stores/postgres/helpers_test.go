package postgres

import (
	"reflect"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestQuoteAndQualifiedTableEscapeIdentifiers(t *testing.T) {
	if got := quoteIdent(`weird"name`); got != `"weird""name"` {
		t.Fatalf("quoteIdent escaped incorrectly: %s", got)
	}
	if got := qualifiedTable(`tenant"1`, `docs"archive`); got != `"tenant""1"."docs""archive"` {
		t.Fatalf("qualifiedTable escaped incorrectly: %s", got)
	}
}

func TestDefaultMetricAndMode(t *testing.T) {
	if got := defaultMetric(""); got != vectordata.DistanceCosine {
		t.Fatalf("defaultMetric empty = %q", got)
	}
	if got := defaultMetric(vectordata.DistanceL2); got != vectordata.DistanceL2 {
		t.Fatalf("defaultMetric l2 = %q", got)
	}
	if got := defaultMode("", true); got != vectordata.EnsureStrict {
		t.Fatalf("defaultMode strict default = %q", got)
	}
	if got := defaultMode("", false); got != vectordata.EnsureAutoMigrate {
		t.Fatalf("defaultMode auto-migrate default = %q", got)
	}
	if got := defaultMode(vectordata.EnsureStrict, false); got != vectordata.EnsureStrict {
		t.Fatalf("defaultMode explicit strict = %q", got)
	}
}

func TestMetricOperatorAndOpClass(t *testing.T) {
	tests := []struct {
		metric   vectordata.DistanceMetric
		operator string
		opClass  string
	}{
		{metric: vectordata.DistanceCosine, operator: "<=>", opClass: "vector_cosine_ops"},
		{metric: vectordata.DistanceL2, operator: "<->", opClass: "vector_l2_ops"},
		{metric: vectordata.DistanceInnerProduct, operator: "<#>", opClass: "vector_ip_ops"},
	}

	for _, tt := range tests {
		t.Run(string(tt.metric), func(t *testing.T) {
			operator, err := metricOperator(tt.metric)
			if err != nil {
				t.Fatalf("metricOperator: %v", err)
			}
			if operator != tt.operator {
				t.Fatalf("operator = %q, want %q", operator, tt.operator)
			}
			opClass, err := metricOpClass(tt.metric)
			if err != nil {
				t.Fatalf("metricOpClass: %v", err)
			}
			if opClass != tt.opClass {
				t.Fatalf("opClass = %q, want %q", opClass, tt.opClass)
			}
		})
	}
}

func TestVectorLiteral(t *testing.T) {
	if got := vectorLiteral([]float32{1, -2.5, 0.125}); got != "[1,-2.5,0.125]" {
		t.Fatalf("unexpected vector literal: %s", got)
	}
}

func TestParseVectorText(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want []float32
	}{
		{name: "empty", raw: "", want: nil},
		{name: "empty vector", raw: "[]", want: []float32{}},
		{name: "values", raw: " [1, -2.5, 0.125] ", want: []float32{1, -2.5, 0.125}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseVectorText(tt.raw)
			if err != nil {
				t.Fatalf("parseVectorText: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseVectorText() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseVectorTextRejectsInvalidInput(t *testing.T) {
	for _, raw := range []string{"1,2", "[bad]"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := parseVectorText(raw); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestMetadataJSONAndParseMetadata(t *testing.T) {
	encoded, err := metadataJSON(nil)
	if err != nil {
		t.Fatalf("metadataJSON nil: %v", err)
	}
	if string(encoded) != "{}" {
		t.Fatalf("nil metadata encoded as %s", encoded)
	}

	parsed, err := parseMetadata([]byte(`{"rank":2,"category":"news"}`))
	if err != nil {
		t.Fatalf("parseMetadata object: %v", err)
	}
	if parsed["category"] != "news" || parsed["rank"] != float64(2) {
		t.Fatalf("unexpected metadata: %#v", parsed)
	}

	for _, raw := range [][]byte{nil, []byte(`null`)} {
		parsed, err := parseMetadata(raw)
		if err != nil {
			t.Fatalf("parseMetadata empty/null: %v", err)
		}
		if len(parsed) != 0 {
			t.Fatalf("expected empty metadata, got %#v", parsed)
		}
	}

	if _, err := parseMetadata([]byte(`{"bad"`)); err == nil {
		t.Fatal("expected parseMetadata error")
	}
}

func TestParseVectorDimension(t *testing.T) {
	dim, err := parseVectorDimension(" vector(1536) ")
	if err != nil {
		t.Fatalf("parseVectorDimension: %v", err)
	}
	if dim != 1536 {
		t.Fatalf("dimension = %d", dim)
	}
}

func TestParseVectorDimensionRejectsInvalidInput(t *testing.T) {
	for _, raw := range []string{"integer", "vector()", "vector(0)", "vector(-1)"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := parseVectorDimension(raw); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
