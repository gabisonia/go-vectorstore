package sqlite

import (
	"github.com/gabisonia/go-vectorstore/internal/modelcodec"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

// Keep SQLite format 1 byte-compatible while sharing canonical value encoding.
func encodeRecord(r vectordata.ModelRecord) ([]byte, error) { return modelcodec.Encode(r) }
func decodeRecord(payload []byte, d vectordata.CollectionDefinition) (vectordata.ModelRecord, error) {
	return modelcodec.Decode(payload, d)
}
func validText(value any) bool { return modelcodec.ValidText(value) }
