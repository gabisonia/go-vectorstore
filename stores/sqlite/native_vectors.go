package sqlite

import (
	"context"
	"fmt"
	"runtime"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// VectorExecution selects how ready dense projections supply exact candidates.
type VectorExecution string

const (
	// VectorExecutionGo retains existing Go execution (the default), reading
	// packed vectors when VectorProjections is enabled.
	VectorExecutionGo VectorExecution = ""
	// VectorExecutionSQLiteVec uses the verified sqlite-vec v0.1.9 scalar L1
	// kernel for unfiltered dense candidates, then ranks them exactly in Go.
	// Other metrics/platforms and filtered searches keep their existing paths.
	VectorExecutionSQLiteVec VectorExecution = "sqlite-vec"
)

func (s *Store) verifyVectorExtension(ctx context.Context) error {
	type build struct{ version, details string }
	info, err := retryBusy(ctx, func() (build, error) {
		var info build
		err := s.db.QueryRowContext(ctx, `SELECT vec_version(),vec_debug()`).Scan(&info.version, &info.details)
		return info, err
	})
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if strings.Contains(err.Error(), "no such function: vec_version") || strings.Contains(err.Error(), "no such function: vec_debug") {
			return fmt.Errorf("%w: sqlite-vec is unavailable; use stores/sqlitevec: %w", vectordata.ErrUnsupported, err)
		}
		return err
	}
	if !verifiedVectorExtension(info.version, info.details) {
		return fmt.Errorf("%w: unverified sqlite-vec build (want v0.1.9 without SIMD flags)", vectordata.ErrUnsupported)
	}
	return nil
}

// SIMD can change the reduction order even with double accumulators. Only the
// pinned sequential kernel has the exact-distance candidate proof below.
func verifiedVectorExtension(version, details string) bool {
	return version == "v0.1.9" && strings.HasPrefix(details, "Version: v0.1.9\n") &&
		strings.HasSuffix(strings.TrimSpace(details), "\nBuild flags:")
}

// Packed storage is little-endian. Restrict the first native kernel to the
// targets sharing the reviewed generated kernel. CI covers Linux/amd64,
// macOS/arm64 and Windows/amd64; every other architecture keeps Go ranking.
func nativeL1Platform() bool {
	return (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64") &&
		(runtime.GOOS == "linux" || runtime.GOOS == "darwin" || runtime.GOOS == "windows")
}

// The pinned scalar L1 kernel (NOT vec0 KNN) performs float64 subtraction,
// absolute value and sequential addition in the same order as DenseDistance.
// Therefore its distance is identical to canonical Go L1, including underflow
// and near ties. Keep ALL records at the (top+Skip)th distance. This is the
// conservative candidate bound with factor 1; a SQL key-token tie order or a
// fixed oversampling factor would not preserve native key ties. Go reapplies
// thresholds, scores and paging. See docs/sqlitevec.md for the proof and limits.
//
// Materialize only keys/distances, calculate distance once per vector, and fetch
// packed vectors only for candidates. Empty/short collections return every row.
// NULL/negative/infinite native distances are passed to Go validation too, so
// malformed projected values cannot silently disappear from an otherwise valid
// page. Finite float32 inputs and supported dimensions cannot overflow float64 L1.
const nativeL1CandidatesSQL = `WITH distances AS MATERIALIZED (
 SELECT key,vec_distance_L1(vector,?3) AS distance
 FROM __vectorstore_vector_values WHERE collection=?1 AND property=?2
), boundary AS (
 SELECT distance FROM distances WHERE distance>=0 AND distance<=1.7976931348623157e308
 ORDER BY distance LIMIT 1 OFFSET ?4
)
SELECT p.key,p.vector FROM distances d
JOIN __vectorstore_vector_values p ON p.collection=?1 AND p.property=?2 AND p.key=d.key
WHERE d.distance IS NULL OR d.distance<0 OR d.distance>1.7976931348623157e308
 OR d.distance<=coalesce((SELECT distance FROM boundary),1.7976931348623157e308)`
