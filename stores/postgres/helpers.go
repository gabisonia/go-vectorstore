package postgres

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

const (
	idColumn       = "id"
	vectorColumn   = "vector"
	metadataColumn = "metadata"
	contentColumn  = "content"
)

func quoteIdent(ident string) string {
	return `"` + strings.ReplaceAll(ident, `"`, `""`) + `"`
}

func qualifiedTable(schema, table string) string {
	return quoteIdent(schema) + "." + quoteIdent(table)
}

func defaultMetric(metric vectordata.DistanceMetric) vectordata.DistanceMetric {
	if metric == "" {
		return vectordata.DistanceCosine
	}
	return metric
}

func defaultMode(mode vectordata.EnsureMode, strictByDefault bool) vectordata.EnsureMode {
	if mode != "" {
		return mode
	}
	if strictByDefault {
		return vectordata.EnsureStrict
	}
	return vectordata.EnsureAutoMigrate
}

func metricOperator(metric vectordata.DistanceMetric) (string, error) {
	switch metric {
	case vectordata.DistanceCosine:
		return "<=>", nil
	case vectordata.DistanceL2, vectordata.DistanceL2Squared:
		return "<->", nil
	case vectordata.DistanceL1:
		return "<+>", nil
	case vectordata.DistanceInnerProduct:
		return "<#>", nil
	default:
		return "", fmt.Errorf("%w: unsupported distance metric %q", vectordata.ErrSchemaMismatch, metric)
	}
}

func metricOpClass(metric vectordata.DistanceMetric) (string, error) {
	switch metric {
	case vectordata.DistanceCosine:
		return "vector_cosine_ops", nil
	case vectordata.DistanceL2, vectordata.DistanceL2Squared:
		return "vector_l2_ops", nil
	case vectordata.DistanceL1:
		return "vector_l1_ops", nil
	case vectordata.DistanceInnerProduct:
		return "vector_ip_ops", nil
	default:
		return "", fmt.Errorf("%w: unsupported distance metric %q", vectordata.ErrSchemaMismatch, metric)
	}
}

// reportedDistanceSQL separates the public distance from the native operator
// used for ANN ordering. Squaring the ORDER BY expression would prevent pgvector
// from using its L2 operator class, even though nearest-neighbor ordering agrees.
func reportedDistanceSQL(raw string, metric vectordata.DistanceMetric) string {
	if metric == vectordata.DistanceL2Squared {
		return "power(" + raw + ", 2)"
	}
	return raw
}

func vectorLiteral(v []float32) string {
	var b strings.Builder
	b.Grow(len(v)*12 + 2)
	var element [32]byte
	b.WriteByte('[')
	for i, n := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(strconv.AppendFloat(element[:0], float64(n), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

func parseVectorText(raw string) ([]float32, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if len(raw) < 2 || raw[0] != '[' || raw[len(raw)-1] != ']' {
		return nil, fmt.Errorf("invalid vector value %q", raw)
	}
	body := strings.TrimSpace(raw[1 : len(raw)-1])
	if body == "" {
		return []float32{}, nil
	}
	parts := strings.Split(body, ",")
	out := make([]float32, 0, len(parts))
	for _, part := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(part), 32)
		if err != nil {
			return nil, fmt.Errorf("parse vector element %q: %w", part, err)
		}
		out = append(out, float32(f))
	}
	return out, nil
}

func normalizeMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return map[string]any{}
	}
	return metadata
}

func metadataJSON(metadata map[string]any) ([]byte, error) {
	return json.Marshal(normalizeMetadata(metadata))
}

func parseMetadata(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var out map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing metadata JSON")
	}
	if out == nil {
		return map[string]any{}, nil
	}
	return out, nil
}
