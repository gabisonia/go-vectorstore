package memory

import "github.com/gabisonia/go-vectorstore/vectordata"

var _ vectordata.StoreMetadataProvider = (*Store)(nil)
var _ vectordata.CollectionMetadataProvider = (*Collection)(nil)

func (*Store) StoreMetadata() vectordata.StoreMetadata {
	return vectordata.StoreMetadata{SystemName: "memory"}
}

func (c *Collection) CollectionMetadata() vectordata.CollectionMetadata {
	return vectordata.CollectionMetadata{StoreMetadata: c.store.StoreMetadata(), CollectionName: c.name}
}
