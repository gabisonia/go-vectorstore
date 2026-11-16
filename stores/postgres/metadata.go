package postgres

import "github.com/gabisonia/go-vectorstore/vectordata"

var _ vectordata.StoreMetadataProvider = (*PostgresVectorStore)(nil)
var _ vectordata.CollectionMetadataProvider = (*ModelCollection)(nil)
var _ vectordata.CollectionMetadataProvider = (*PostgresCollection)(nil)

func (s *PostgresVectorStore) StoreMetadata() vectordata.StoreMetadata {
	return vectordata.StoreMetadata{SystemName: "postgresql", DatabaseName: s.databaseName, Namespace: s.opts.Schema}
}

func (c *ModelCollection) CollectionMetadata() vectordata.CollectionMetadata {
	return vectordata.CollectionMetadata{StoreMetadata: c.store.StoreMetadata(), CollectionName: c.name}
}

func (c *PostgresCollection) CollectionMetadata() vectordata.CollectionMetadata {
	return vectordata.CollectionMetadata{StoreMetadata: c.store.StoreMetadata(), CollectionName: c.name}
}
