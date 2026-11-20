package sqlitevec

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// NativeSchema identifies the physical columns of an integer-keyed SQLite
// collection with one text property and a named dense cosine-distance vector.
type NativeSchema struct {
	DataTable, VectorTable string
	Key, Text, Vector      string
	Dimensions             int
}

// NativeCollection accesses a pair of existing data and vec0 tables. The caller
// owns the database, its lifetime and schema migrations.
type NativeCollection struct {
	db     *sql.DB
	schema NativeSchema
}

var identifier = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)

func quote(name string) string { return `"` + name + `"` }

// AttachNative validates both main-database tables. VectorTable defaults to the
// C# connector's vec_ prefix. Attach never creates or migrates tables.
func AttachNative(ctx context.Context, db *sql.DB, s NativeSchema) (*NativeCollection, error) {
	if s.VectorTable == "" {
		s.VectorTable = "vec_" + s.DataTable
	}
	for _, name := range []string{s.DataTable, s.VectorTable, s.Key, s.Text, s.Vector} {
		if !identifier.MatchString(name) {
			return nil, vectordata.ErrSchemaMismatch
		}
	}
	if db == nil || s.Dimensions < 1 || strings.EqualFold(s.Key, s.Text) || strings.EqualFold(s.Key, s.Vector) || strings.EqualFold(s.VectorTable, s.DataTable) {
		return nil, vectordata.ErrSchemaMismatch
	}
	// table_xinfo includes hidden/generated columns, which replacement writes cannot preserve.
	var columns, keys, texts int
	err := db.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE name=? AND upper(type)='INTEGER' AND pk=1 AND hidden=0),
 count(*) FILTER (WHERE name=? AND upper(type)='TEXT' AND pk=0 AND hidden=0)
 FROM pragma_table_xinfo(?, 'main')`, s.Key, s.Text, s.DataTable).Scan(&columns, &keys, &texts)
	if err != nil {
		return nil, err
	}
	var tableType string
	if err := db.QueryRowContext(ctx, "SELECT type FROM pragma_table_list WHERE schema='main' AND name=?", s.DataTable).Scan(&tableType); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if tableType != "table" || columns != 2 || keys != 1 || texts != 1 {
		return nil, fmt.Errorf("%w: incompatible data columns", vectordata.ErrSchemaMismatch)
	}
	var ddl string
	if err := db.QueryRowContext(ctx, `SELECT sql FROM main.sqlite_schema WHERE type='table' AND name=?`, s.VectorTable).Scan(&ddl); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, vectordata.ErrSchemaMismatch
		}
		return nil, err
	}
	// Match the narrow, published vec0 grammar emitted by the reference connector.
	// SQLite normalizes the CREATE prefix and drops IF NOT EXISTS in sqlite_schema.
	pattern := fmt.Sprintf(`(?i)^CREATE\s+VIRTUAL\s+TABLE\s+"?%s"?\s+USING\s+vec0\s*\(\s*%s\s+INTEGER\s+PRIMARY\s+KEY\s*,\s*%s\s+FLOAT\[%d\]\s+distance_metric=cosine\s*\)\s*$`, s.VectorTable, s.Key, s.Vector, s.Dimensions)
	if !regexp.MustCompile(pattern).MatchString(ddl) {
		return nil, fmt.Errorf("%w: incompatible vec0 definition", vectordata.ErrSchemaMismatch)
	}
	return &NativeCollection{db, s}, nil
}

// NativePoint stores one complete record in the two native tables.
type NativePoint struct {
	ID     int64
	Text   string
	Vector []float32
}

// NativeHit reports vec0's cosine distance. Smaller distances are closer.
type NativeHit struct {
	ID       int64
	Distance float64
}

// ErrNativeOutcomeUnknown indicates that a native transaction's commit failed
// to acknowledge success. Mutations never retry an uncertain commit.
var ErrNativeOutcomeUnknown = errors.New("SQLite native commit outcome unknown")

func (c *NativeCollection) mutate(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return errors.Join(ErrNativeOutcomeUnknown, err)
	}
	return nil
}

func (c *NativeCollection) vectorJSON(vector []float32) (string, error) {
	_, err := vectordata.NormalizeModelVector(vector, vectordata.VectorProperty{Dimensions: c.schema.Dimensions, Metric: vectordata.DistanceCosine})
	if err != nil {
		return "", err
	}
	data, err := json.Marshal(vector)
	return string(data), err
}

// Upsert replaces a complete point in one transaction.
func (c *NativeCollection) Upsert(ctx context.Context, p NativePoint) error {
	vector, err := c.vectorJSON(p.Vector)
	if err != nil {
		return err
	}
	s := c.schema
	return c.mutate(ctx, func(tx *sql.Tx) error {
		key, text := quote(s.Key), quote(s.Text)
		query := fmt.Sprintf("INSERT INTO main.%s(%s,%s) VALUES(?,?) ON CONFLICT(%s) DO UPDATE SET %s=excluded.%s", quote(s.DataTable), key, text, key, text, text)
		if _, err := tx.ExecContext(ctx, query, p.ID, p.Text); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM main."+quote(s.VectorTable)+" WHERE "+key+"=?", p.ID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO main."+quote(s.VectorTable)+"("+key+","+quote(s.Vector)+") VALUES(?,vec_f32(?))", p.ID, vector)
		return err
	})
}

// Delete removes both rows atomically.
func (c *NativeCollection) Delete(ctx context.Context, id int64) error {
	s := c.schema
	return c.mutate(ctx, func(tx *sql.Tx) error {
		for _, table := range []string{s.VectorTable, s.DataTable} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM main."+quote(table)+" WHERE "+quote(s.Key)+"=?", id); err != nil {
				return err
			}
		}
		return nil
	})
}

// Get reads a single point. A missing ID returns sql.ErrNoRows.
func (c *NativeCollection) Get(ctx context.Context, id int64) (NativePoint, error) {
	s := c.schema
	query := fmt.Sprintf("SELECT d.%s,v.%s FROM main.%s d JOIN main.%s v USING(%s) WHERE d.%s=?", quote(s.Text), quote(s.Vector), quote(s.DataTable), quote(s.VectorTable), quote(s.Key), quote(s.Key))
	p := NativePoint{ID: id}
	var data []byte
	if err := c.db.QueryRowContext(ctx, query, id).Scan(&p.Text, &data); err != nil {
		return NativePoint{}, err
	}
	if len(data)%4 != 0 || len(data)/4 != s.Dimensions {
		return NativePoint{}, vectordata.ErrInvalidRecord
	}
	p.Vector = make([]float32, s.Dimensions)
	_, err := binary.Decode(data, binary.LittleEndian, p.Vector)
	return p, err
}

// Search ranks native vec0 candidates by ascending cosine distance.
func (c *NativeCollection) Search(ctx context.Context, vector []float32, top int) ([]NativeHit, error) {
	if top < 1 {
		return nil, vectordata.ErrInvalidRecord
	}
	data, err := c.vectorJSON(vector)
	if err != nil {
		return nil, err
	}
	s := c.schema
	query := fmt.Sprintf("SELECT %s,distance FROM main.%s WHERE %s MATCH vec_f32(?) AND k=? ORDER BY distance", quote(s.Key), quote(s.VectorTable), quote(s.Vector))
	rows, err := c.db.QueryContext(ctx, query, data, top)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hits []NativeHit
	for rows.Next() {
		var hit NativeHit
		if err := rows.Scan(&hit.ID, &hit.Distance); err != nil {
			return nil, err
		}
		hits = append(hits, hit)
	}
	return hits, rows.Err()
}
