package postgres

import (
	"encoding/json"
	"fmt"
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
	case vectordata.DistanceL2:
		return "<->", nil
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
	case vectordata.DistanceL2:
		return "vector_l2_ops", nil
	case vectordata.DistanceInnerProduct:
		return "vector_ip_ops", nil
	default:
		return "", fmt.Errorf("%w: unsupported distance metric %q", vectordata.ErrSchemaMismatch, metric)
	}
}

func vectorLiteral(v []float32) string {
	var b strings.Builder
	b.Grow(len(v) * 8)
	b.WriteByte('[')
	for i, n := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(n), 'f', -1, 32))
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
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if out == nil {
		return map[string]any{}, nil
	}
	return out, nil
}
