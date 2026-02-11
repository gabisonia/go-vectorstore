package mssql

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

const (
	idColumn            = "id"
	vectorColumn        = "vector"
	metadataColumn      = "metadata"
	contentColumn       = "content"
	collectionMetaTable = "__vector_collections"
	maxRowsPerStatement = 500
)

func quoteIdent(ident string) string {
	return "[" + strings.ReplaceAll(ident, "]", "]]") + "]"
}

func qualifiedTable(schema, table string) string {
	return quoteIdent(schema) + "." + quoteIdent(table)
}

func objectIDName(schema, table string) string {
	return quoteIdent(schema) + "." + quoteIdent(table)
}

func escapeSQLString(value string) string {
	return strings.ReplaceAll(value, "'", "''")
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

func vectorJSON(vector []float32) (string, error) {
	payload, err := json.Marshal(vector)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func parseVectorJSON(raw string) ([]float32, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	var values []float64
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}

	out := make([]float32, 0, len(values))
	for _, value := range values {
		out = append(out, float32(value))
	}
	return out, nil
}

func normalizeMetadata(metadata map[string]any) map[string]any {
	if metadata == nil {
		return map[string]any{}
	}
	return metadata
}

func metadataJSON(metadata map[string]any) (string, error) {
	payload, err := json.Marshal(normalizeMetadata(metadata))
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func parseMetadataJSON(raw string) (map[string]any, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}, nil
	}

	var metadata map[string]any
	if err := json.Unmarshal([]byte(raw), &metadata); err != nil {
		return nil, err
	}
	if metadata == nil {
		return map[string]any{}, nil
	}
	return metadata, nil
}

func resolveProjection(projection *vectordata.Projection) vectordata.Projection {
	if projection == nil {
		return vectordata.DefaultProjection()
	}
	return *projection
}

func distanceBetween(metric vectordata.DistanceMetric, query, candidate []float32) (float64, error) {
	if len(query) != len(candidate) {
		return 0, fmt.Errorf("%w: expected %d, got %d", vectordata.ErrDimensionMismatch, len(query), len(candidate))
	}

	switch metric {
	case vectordata.DistanceCosine:
		return cosineDistance(query, candidate), nil
	case vectordata.DistanceL2:
		return l2Distance(query, candidate), nil
	case vectordata.DistanceInnerProduct:
		return -dot(query, candidate), nil
	default:
		return 0, fmt.Errorf("%w: unsupported distance metric %q", vectordata.ErrSchemaMismatch, metric)
	}
}

func cosineDistance(left, right []float32) float64 {
	leftNorm := norm(left)
	rightNorm := norm(right)
	if leftNorm == 0 || rightNorm == 0 {
		return 1
	}
	similarity := dot(left, right) / (leftNorm * rightNorm)
	return 1 - similarity
}

func l2Distance(left, right []float32) float64 {
	sum := 0.0
	for i := range left {
		delta := float64(left[i] - right[i])
		sum += delta * delta
	}
	return math.Sqrt(sum)
}

func dot(left, right []float32) float64 {
	sum := 0.0
	for i := range left {
		sum += float64(left[i] * right[i])
	}
	return sum
}

func norm(vector []float32) float64 {
	sum := 0.0
	for _, value := range vector {
		f := float64(value)
		sum += f * f
	}
	return math.Sqrt(sum)
}

func isStringType(dataType string) bool {
	switch strings.ToLower(strings.TrimSpace(dataType)) {
	case "varchar", "nvarchar", "char", "nchar", "text", "ntext":
		return true
	default:
		return false
	}
}

func toFloat64(value any) (float64, bool) {
	switch n := value.(type) {
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case float32:
		return float64(n), true
	case float64:
		return n, true
	case json.Number:
		parsed, err := strconv.ParseFloat(string(n), 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}
