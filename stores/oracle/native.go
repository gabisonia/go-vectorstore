// Package oracle accesses native Oracle AI Database vector tables through database/sql.
package oracle

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// NativeSchema names the three columns of an existing table owned by the session user.
// Identifiers are case-sensitive, including those originally created with double quotes.
type NativeSchema struct {
	Table, Key, Category, Vector string
	Dimensions                   int
}

// NativeCollection uses a caller-managed database/sql pool.
type NativeCollection struct {
	db                           *sql.DB
	table, key, category, vector string
	dimensions                   int
}

// NativePoint contains an explicit NUMBER(19) key, NUMBER(10) category and FLOAT32 vector.
type NativePoint struct {
	ID       int64
	Category *int32
	Vector   []float32
}

// NativeHit reports raw cosine distance, in ascending order.
type NativeHit struct {
	ID       int64
	Distance float64
}

// ErrNativeOutcomeUnknown indicates that a transaction commit was not acknowledged.
var ErrNativeOutcomeUnknown = errors.New("Oracle native commit outcome unknown")

func quote(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }

// AttachNative checks primary key, column types and physical vector dimensions.
func AttachNative(ctx context.Context, db *sql.DB, s NativeSchema) (*NativeCollection, error) {
	for _, name := range []string{s.Table, s.Key, s.Category, s.Vector} {
		if name == "" || strings.ContainsRune(name, 0) {
			return nil, vectordata.ErrSchemaMismatch
		}
	}
	if db == nil || s.Key == s.Category || s.Key == s.Vector || s.Category == s.Vector || s.Dimensions < 1 {
		return nil, vectordata.ErrSchemaMismatch
	}
	const metadata = `SELECT USER,c.column_name,c.data_type,NVL(c.data_precision,-1),NVL(c.data_scale,-1),
NVL(c.vector_info,'-'),c.identity_column,c.virtual_column,
CASE WHEN EXISTS (SELECT 1 FROM user_constraints p JOIN user_cons_columns k
ON k.constraint_name=p.constraint_name AND k.table_name=p.table_name
WHERE p.table_name=c.table_name AND p.constraint_type='P' AND p.status='ENABLED'
AND k.column_name=c.column_name AND NOT EXISTS (SELECT 1 FROM user_cons_columns k2
WHERE k2.constraint_name=p.constraint_name AND k2.table_name=p.table_name AND k2.position>1)) THEN 1 ELSE 0 END
FROM user_tab_cols c JOIN user_tables t ON t.table_name=c.table_name
WHERE c.table_name=:1 AND c.hidden_column='NO'`
	rows, err := db.QueryContext(ctx, metadata, s.Table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	count := 0
	var owner string
	for rows.Next() {
		var name, typ, info, identity, virtual string
		var precision, scale, primary int
		if err := rows.Scan(&owner, &name, &typ, &precision, &scale, &info, &identity, &virtual, &primary); err != nil {
			return nil, err
		}
		dense := fmt.Sprintf("VECTOR(%d,FLOAT32)", s.Dimensions)
		info = strings.ToUpper(strings.ReplaceAll(info, " ", ""))
		valid := name == s.Key && typ == "NUMBER" && precision == 19 && scale == 0 && primary == 1 || name == s.Category && typ == "NUMBER" && precision == 10 && scale == 0 || name == s.Vector && typ == "VECTOR" && (info == dense || info == strings.TrimSuffix(dense, ")")+",DENSE)")
		if !valid || identity == "YES" || virtual == "YES" {
			return nil, fmt.Errorf("%w: column %q (%s, precision %d, scale %d, %s)", vectordata.ErrSchemaMismatch, name, typ, precision, scale, info)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if count != 3 {
		return nil, vectordata.ErrSchemaMismatch
	}
	return &NativeCollection{db, quote(owner) + "." + quote(s.Table), quote(s.Key), quote(s.Category), quote(s.Vector), s.Dimensions}, nil
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

// Upsert merges one complete row. Oracle's primary key arbitrates concurrent inserts.
func (c *NativeCollection) Upsert(ctx context.Context, p NativePoint) error {
	raw, err := c.encode(p.Vector)
	if err != nil {
		return err
	}
	stmt := fmt.Sprintf(`MERGE INTO %s d USING (SELECT CAST(:1 AS NUMBER(19)) k, CAST(:2 AS NUMBER(10)) cat, TO_VECTOR(:3,%d,FLOAT32) v FROM dual) s
ON (d.%s=s.k) WHEN MATCHED THEN UPDATE SET d.%s=s.cat,d.%s=s.v
WHEN NOT MATCHED THEN INSERT (%s,%s,%s) VALUES (s.k,s.cat,s.v)`, c.table, c.dimensions, c.key, c.category, c.vector, c.key, c.category, c.vector)
	return c.mutate(ctx, stmt, p.ID, p.Category, raw)
}

// Get returns sql.ErrNoRows for an absent key.
func (c *NativeCollection) Get(ctx context.Context, id int64) (NativePoint, error) {
	p := NativePoint{ID: id}
	var raw string
	stmt := fmt.Sprintf("SELECT %s,VECTOR_SERIALIZE(%s RETURNING CLOB) FROM %s WHERE %s=:1", c.category, c.vector, c.table, c.key)
	if err := c.db.QueryRowContext(ctx, stmt, id).Scan(&p.Category, &raw); err != nil {
		return NativePoint{}, err
	}
	if err := json.Unmarshal([]byte(raw), &p.Vector); err != nil {
		return NativePoint{}, err
	}
	if _, err := vectordata.NormalizeModelVector(p.Vector, vectordata.VectorProperty{Dimensions: c.dimensions, Metric: vectordata.DistanceCosine}); err != nil {
		return NativePoint{}, err
	}
	return p, nil
}

// Delete removes one row. A missing key is a successful no-op.
func (c *NativeCollection) Delete(ctx context.Context, id int64) error {
	return c.mutate(ctx, "DELETE FROM "+c.table+" WHERE "+c.key+"=:1", id)
}

// Search ranks non-null vectors by exact cosine distance. An optional category is bound as a number.
func (c *NativeCollection) Search(ctx context.Context, v []float32, top int, category *int32) ([]NativeHit, error) {
	if top < 1 {
		return nil, vectordata.ErrInvalidRecord
	}
	raw, err := c.encode(v)
	if err != nil {
		return nil, err
	}
	stmt := fmt.Sprintf("SELECT %s,VECTOR_DISTANCE(%s,TO_VECTOR(:1,%d,FLOAT32),COSINE) FROM %s WHERE %s IS NOT NULL", c.key, c.vector, c.dimensions, c.table, c.vector)
	args := []any{raw}
	if category != nil {
		stmt += " AND " + c.category + "=:2"
		args = append(args, *category)
	}
	stmt += fmt.Sprintf(" ORDER BY 2,%s FETCH FIRST :%d ROWS ONLY", c.key, len(args)+1)
	rows, err := c.db.QueryContext(ctx, stmt, append(args, top)...)
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
