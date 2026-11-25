package qdrant

import (
	"encoding/json"
	"fmt"

	"github.com/gabisonia/go-vectorstore/internal/modelcodec"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

// NewNativeModelSearcher maps logical model fields to attached native points.
// A nil definition infers struct tags. Searches on a multi-vector model must
// select a logical vector property. Unmapped payload fields are ignored.
func NewNativeModelSearcher[T any, K NativeKey](points *NativePoints[K], definition *vectordata.CollectionDefinition) (vectordata.VectorSearcher[T], error) {
	codec, err := vectordata.NewStructCodec[T](definition)
	if err != nil {
		return nil, err
	}
	d := codec.Definition()
	var key K
	_, unsigned := any(key).(uint64)
	if points == nil || len(d.Vectors) == 0 || unsigned && d.Key.Type != vectordata.KeyUint64 || !unsigned && d.Key.Type != vectordata.KeyUUID {
		return nil, fmt.Errorf("%w: expected matching point key and named vectors", vectordata.ErrSchemaMismatch)
	}
	for _, p := range d.Vectors {
		if p.Kind != "" || p.Precision != "" || p.Metric != vectordata.DistanceCosine || points.dimensions[p.StorageName] != p.Dimensions {
			return nil, fmt.Errorf("%w: incompatible native vector %q", vectordata.ErrSchemaMismatch, p.StorageName)
		}
	}
	decode := func(point NamedPoint[K], includeVectors bool) (T, error) {
		var zero T
		key, err := d.NormalizeKey(point.ID)
		if err != nil {
			return zero, err
		}
		record := vectordata.ModelRecord{Key: key, Data: make(map[string]any), Vectors: make(map[string][]float32)}
		for _, p := range d.Data {
			value, exists := point.Payload[p.StorageName]
			if value != nil || exists && p.Type == vectordata.TypeJSON {
				raw, err := json.Marshal(value)
				if err != nil {
					return zero, err
				}
				value, err = modelcodec.DecodeProperty(raw, p)
				if err != nil {
					return zero, fmt.Errorf("payload field %q: %w", p.StorageName, err)
				}
			}
			record.Data[p.Name], err = vectordata.NormalizeModelData(value, p)
			if err != nil {
				return zero, err
			}
		}
		if includeVectors {
			for _, p := range d.Vectors {
				v := point.Vectors[p.StorageName]
				if v == nil && p.Optional {
					continue
				}
				if err := vectordata.ValidateModelVector(v, p); err != nil {
					return zero, err
				}
				record.Vectors[p.Name] = v
			}
		}
		return codec.Decode(record)
	}
	return &nativeSearcher[K, T]{points: points, definition: &d, decode: decode}, nil
}
