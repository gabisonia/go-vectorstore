package sqlite

import (
	"context"
	"database/sql"
	"iter"

	"github.com/gabisonia/go-vectorstore/internal/exactsearch"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

func (c *Collection) searchRecords(ctx context.Context, p vectordata.VectorProperty, top int, opts vectordata.ModelSearchOptions, measure exactsearch.Measure, denseQuery []float32) ([]vectordata.ModelSearchResult, error) {
	return retryBusy(ctx, func() ([]vectordata.ModelSearchResult, error) {
		if hits, used, err := c.searchProjectedVectors(ctx, p, top, opts, measure, denseQuery); used || err != nil {
			return hits, err
		}
		return exactsearch.Run(ctx, c.definition, p, top, opts, c.searchScan(ctx), measure)
	})
}

// Stream canonical records from one WAL snapshot; do not reconstruct a memory
// collection. The cursor/transaction close before Run exposes the final page.
func (c *Collection) searchScan(ctx context.Context) iter.Seq2[vectordata.ModelRecord, error] {
	return func(yield func(vectordata.ModelRecord, error) bool) {
		tx, err := c.store.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
		if err != nil {
			yield(vectordata.ModelRecord{}, err)
			return
		}
		defer tx.Rollback()
		if _, err := c.metadata(ctx, tx); err != nil {
			yield(vectordata.ModelRecord{}, err)
			return
		}
		rows, err := tx.QueryContext(ctx, `SELECT payload FROM __vectorstore_records WHERE collection=?`, c.name)
		if err != nil {
			yield(vectordata.ModelRecord{}, err)
			return
		}
		defer rows.Close()
		definition := c.readDefinition()
		for rows.Next() {
			if err := ctx.Err(); err != nil {
				yield(vectordata.ModelRecord{}, err)
				return
			}
			var payload []byte
			if err := rows.Scan(&payload); err != nil {
				yield(vectordata.ModelRecord{}, err)
				return
			}
			record, err := decodeRecord(payload, c.definition)
			if err != nil {
				yield(vectordata.ModelRecord{}, err)
				return
			}
			prepared, err := vectordata.PrepareModelRecords(ctx, definition, vectordata.ModelCollectionOptions{}, []vectordata.ModelRecord{record})
			if err != nil {
				yield(vectordata.ModelRecord{}, err)
				return
			}
			if !yield(prepared[0], nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(vectordata.ModelRecord{}, err)
		}
	}
}
