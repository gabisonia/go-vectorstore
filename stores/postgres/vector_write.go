package postgres

import (
	"context"
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"math"
	"slices"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

// writeVector borrows a validated vector until the synchronous write completes.
// Binary encoding appends directly to pgx's buffer, avoiding a text intermediary.
type writeVector []float32

// Local transport benchmarks favor binary above roughly 1,024 float32 values.
// Keep tiny writes on text so codec/OID setup does not outweigh the savings.
const minBinaryVectorValues = 1024

func useBinaryVectors(rows, dimensions int) bool {
	return rows > 0 && dimensions > 0 && (dimensions >= minBinaryVectorValues || rows > (minBinaryVectorValues-1)/dimensions)
}

type writeVectorCodec struct{}

func (writeVectorCodec) FormatSupported(format int16) bool {
	return format == pgtype.BinaryFormatCode || format == pgtype.TextFormatCode
}
func (writeVectorCodec) PreferredFormat() int16 { return pgtype.BinaryFormatCode }
func (writeVectorCodec) PlanEncode(_ *pgtype.Map, _ uint32, format int16, value any) pgtype.EncodePlan {
	if _, ok := value.(writeVector); !ok {
		return nil
	}
	return vectorEncodePlan{format: format}
}

// This codec is scoped to ingestion, whose RETURNING clauses contain keys only.
func (writeVectorCodec) PlanScan(*pgtype.Map, uint32, int16, any) pgtype.ScanPlan { return nil }
func (writeVectorCodec) DecodeValue(*pgtype.Map, uint32, int16, []byte) (any, error) {
	return nil, fmt.Errorf("write-only vector codec")
}
func (c writeVectorCodec) DecodeDatabaseSQLValue(m *pgtype.Map, oid uint32, format int16, src []byte) (driver.Value, error) {
	return c.DecodeValue(m, oid, format, src)
}

type vectorEncodePlan struct{ format int16 }

func (p vectorEncodePlan) Encode(value any, buf []byte) ([]byte, error) {
	vector := value.(writeVector)
	if p.format == pgtype.TextFormatCode {
		return append(buf, vectorLiteral(vector)...), nil
	}
	// The pgvector wire format uses a signed int16 dimension, zero reserved bits,
	// and network-order float32 elements. Reject overflow rather than truncating it.
	if len(vector) == 0 || len(vector) > math.MaxInt16 {
		return nil, fmt.Errorf("%w: binary vector dimensions out of range", vectordata.ErrInvalidVector)
	}
	buf = slices.Grow(buf, 4+4*len(vector))
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(vector)))
	buf = binary.BigEndian.AppendUint16(buf, 0)
	for _, v := range vector {
		buf = binary.BigEndian.AppendUint32(buf, math.Float32bits(v))
	}
	return buf, nil
}
func vectorWriteArg(vector []float32, useBinary bool) any {
	if useBinary {
		return writeVector(vector)
	}
	return vectorLiteral(vector)
}

// beginVectorWrite owns the pool lease until rollback/commit and codec restoration
// have finished. Using pool.Begin would release the lease inside Commit, too soon
// to restore caller-owned type mappings before another goroutine acquires it.
func beginVectorWrite(ctx context.Context, pool *pgxpool.Pool, useBinary bool) (pgx.Tx, func(), error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, nil, err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		return nil, nil, err
	}
	release := conn.Release
	if useBinary {
		var oid uint32
		if err := tx.QueryRow(ctx, "SELECT 'vector'::regtype::oid").Scan(&oid); err != nil {
			rollbackTx(ctx, tx)
			conn.Release()
			return nil, nil, err
		}
		m := conn.Conn().TypeMap()
		original := *m
		scoped := m.Copy()
		scoped.TryWrapEncodePlanFuncs = slices.Clone(m.TryWrapEncodePlanFuncs)
		scoped.TryWrapScanPlanFuncs = slices.Clone(m.TryWrapScanPlanFuncs)
		scoped.RegisterType(&pgtype.Type{Name: "vector", OID: oid, Codec: writeVectorCodec{}})
		scoped.RegisterDefaultPgType(writeVector(nil), "vector")
		*m = *scoped
		release = func() { *m = original; conn.Release() }
	}
	return tx, release, nil
}
