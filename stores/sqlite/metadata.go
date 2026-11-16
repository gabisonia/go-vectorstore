package sqlite

import "github.com/gabisonia/go-vectorstore/vectordata"

var _ vectordata.StoreMetadataProvider = (*Store)(nil)
var _ vectordata.CollectionMetadataProvider = (*Collection)(nil)

func (s *Store) StoreMetadata() vectordata.StoreMetadata {
	return vectordata.StoreMetadata{SystemName: "sqlite", DatabaseName: s.databaseName}
}

func (c *Collection) CollectionMetadata() vectordata.CollectionMetadata {
	return vectordata.CollectionMetadata{StoreMetadata: c.store.StoreMetadata(), CollectionName: c.name}
}
