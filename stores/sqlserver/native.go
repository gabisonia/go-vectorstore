// Package sqlserver accesses externally managed SQL Server 2025 vector tables.
package sqlserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// NativeSchema maps an explicit bigint primary key, nvarchar(max) text and
// a dense float32 vector. DatabaseSchema defaults to dbo.
type NativeSchema struct {
	DatabaseSchema, Table string
	Key, Text, Vector     string
	Dimensions            int
}

// NativeCollection operates on one existing table. Callers own the *sql.DB and schema.
type NativeCollection struct {
	db                             *sql.DB
	table, key, text, vector, cast string
	dimensions                     int
}

// NativePoint holds a complete row.
type NativePoint struct {
	ID     int64
	Text   string
	Vector []float32
}

// NativeHit carries SQL Server's raw cosine distance in ascending order.
type NativeHit struct {
	ID       int64
	Distance float64
}

// ErrNativeOutcomeUnknown means the transaction commit was not acknowledged.
var ErrNativeOutcomeUnknown = errors.New("SQL Server native commit outcome unknown")

func ident(name string) string { return "[" + strings.ReplaceAll(name, "]", "]]") + "]" }

// AttachNative checks the table's actual columns, primary key and vector type.
func AttachNative(ctx context.Context, db *sql.DB, s NativeSchema) (*NativeCollection, error) {
	if s.DatabaseSchema == "" {
		s.DatabaseSchema = "dbo"
	}
	for _, name := range []string{s.DatabaseSchema, s.Table, s.Key, s.Text, s.Vector} {
		if name == "" || strings.ContainsRune(name, 0) {
			return nil, vectordata.ErrSchemaMismatch
		}
	}
	if db == nil || s.Dimensions < 1 || s.Key == s.Text || s.Key == s.Vector || s.Text == s.Vector {
		return nil, vectordata.ErrSchemaMismatch
	}
	rows, err := db.QueryContext(ctx, `SELECT c.name,TYPE_NAME(c.user_type_id),c.max_length,COALESCE(c.vector_dimensions,0),
 COALESCE(c.vector_base_type,0),c.is_identity,c.is_computed,
 CASE WHEN EXISTS(SELECT 1 FROM sys.indexes i JOIN sys.index_columns k ON k.object_id=i.object_id AND k.index_id=i.index_id
 WHERE i.object_id=c.object_id AND i.is_primary_key=1 AND k.column_id=c.column_id AND k.key_ordinal=1
 AND NOT EXISTS(SELECT 1 FROM sys.index_columns other WHERE other.object_id=i.object_id AND other.index_id=i.index_id AND other.key_ordinal>1))
 THEN 1 ELSE 0 END FROM sys.columns c JOIN sys.tables t ON t.object_id=c.object_id JOIN sys.schemas sc ON sc.schema_id=t.schema_id
 WHERE sc.name=@p1 AND t.name=@p2`, s.DatabaseSchema, s.Table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var name, typ string
		var length, size, base int
		var identity, computed, primary bool
		if err := rows.Scan(&name, &typ, &length, &size, &base, &identity, &computed, &primary); err != nil {
			return nil, err
		}
		valid := name == s.Key && typ == "bigint" && primary || name == s.Text && typ == "nvarchar" && length == -1 || name == s.Vector && typ == "vector" && size == s.Dimensions && base == 0
		if !valid || identity || computed {
			return nil, fmt.Errorf("%w: incompatible column %q", vectordata.ErrSchemaMismatch, name)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if count != 3 {
		return nil, vectordata.ErrSchemaMismatch
	}
	return &NativeCollection{db, ident(s.DatabaseSchema) + "." + ident(s.Table), ident(s.Key), ident(s.Text), ident(s.Vector), fmt.Sprintf("vector(%d)", s.Dimensions), s.Dimensions}, nil
}

func (c *NativeCollection) encode(v []float32) (string, error) {
	if _, err := vectordata.NormalizeModelVector(v, vectordata.VectorProperty{Dimensions: c.dimensions, Metric: vectordata.DistanceCosine}); err != nil {
		return "", err
	}
	raw, err := json.Marshal(v)
	return string(raw), err
}

func (c *NativeCollection) mutate(ctx context.Context, stmt string, args ...any) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, stmt, args...); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return errors.Join(ErrNativeOutcomeUnknown, err)
	}
	return nil
}

// Upsert inserts or updates the complete row under a serializable key-range lock.
func (c *NativeCollection) Upsert(ctx context.Context, p NativePoint) error {
	raw, err := c.encode(p.Vector)
	if err != nil {
		return err
	}
	stmt := fmt.Sprintf(`UPDATE %s WITH (UPDLOCK,HOLDLOCK) SET %s=@p2,%s=CAST(@p3 AS %s) WHERE %s=@p1;
 IF @@ROWCOUNT=0 INSERT INTO %s (%s,%s,%s) VALUES(@p1,@p2,CAST(@p3 AS %s));`, c.table, c.text, c.vector, c.cast, c.key, c.table, c.key, c.text, c.vector, c.cast)
	return c.mutate(ctx, stmt, p.ID, p.Text, raw)
}

// Get retrieves the complete point. An absent key returns sql.ErrNoRows.
func (c *NativeCollection) Get(ctx context.Context, id int64) (NativePoint, error) {
	var p NativePoint
	var raw string
	err := c.db.QueryRowContext(ctx, "SELECT "+c.text+",CAST("+c.vector+" AS varchar(max)) FROM "+c.table+" WHERE "+c.key+"=@p1", id).Scan(&p.Text, &raw)
	if err != nil {
		return NativePoint{}, err
	}
	if err = json.Unmarshal([]byte(raw), &p.Vector); err != nil {
		return NativePoint{}, err
	}
	p.ID = id
	return p, nil
}

// Delete removes a point by primary key.
func (c *NativeCollection) Delete(ctx context.Context, id int64) error {
	return c.mutate(ctx, "DELETE FROM "+c.table+" WHERE "+c.key+"=@p1", id)
}

// Search ranks non-null vectors by cosine distance, breaking ties by primary key.
func (c *NativeCollection) Search(ctx context.Context, v []float32, top int) ([]NativeHit, error) {
	if top < 1 {
		return nil, vectordata.ErrInvalidRecord
	}
	raw, err := c.encode(v)
	if err != nil {
		return nil, err
	}
	stmt := fmt.Sprintf("SELECT TOP (@p1) %s,VECTOR_DISTANCE('cosine',%s,CAST(@p2 AS %s)) FROM %s WHERE %s IS NOT NULL ORDER BY 2,%s", c.key, c.vector, c.cast, c.table, c.vector, c.key)
	rows, err := c.db.QueryContext(ctx, stmt, top, raw)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []NativeHit
	for rows.Next() {
		var h NativeHit
		if err := rows.Scan(&h.ID, &h.Distance); err != nil {
			return nil, err
		}
		hits = append(hits, h)
	}
	return hits, rows.Err()
}
