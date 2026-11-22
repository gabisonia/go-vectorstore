package sqlite

import (
	"fmt"

	"github.com/gabisonia/go-vectorstore/internal/modelcodec"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

// Keep SQLite format 1 byte-compatible while sharing canonical value encoding.
func encodeRecord(r vectordata.ModelRecord) ([]byte, error) { return modelcodec.Encode(r) }
func decodeRecord(payload []byte, d vectordata.CollectionDefinition) (vectordata.ModelRecord, error) {
	return modelcodec.Decode(payload, d)
}

// decodeStoredRecord checks the payload against the indexed row before a read
// or rebuild consumes it. Format 1 stores JSON tokens of normalized native keys.
func decodeStoredRecord(payload []byte, storedKey string, d vectordata.CollectionDefinition) (vectordata.ModelRecord, error) {
	record, err := decodeRecord(payload, d)
	if err != nil {
		return vectordata.ModelRecord{}, err
	}
	key, err := d.NormalizeKey(record.Key)
	if err != nil {
		return vectordata.ModelRecord{}, err
	}
	if keyToken(key) != storedKey {
		return vectordata.ModelRecord{}, fmt.Errorf("%w: canonical key differs from stored key", vectordata.ErrInvalidRecord)
	}
	record.Key = key
	return record, nil
}

func validText(value any) bool { return modelcodec.ValidText(value) }
