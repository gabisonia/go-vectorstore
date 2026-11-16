package vectordata

// StoreMetadata identifies the configured store. Discovery performs no I/O and
// does not establish existence or health. Empty fields mean unavailable.
// Returned values are caller-owned snapshots.
type StoreMetadata struct {
	// SystemName identifies the database system: memory, sqlite or postgresql
	// for built-ins. It need not equal ProviderCapabilities.Backend.
	SystemName string
	// DatabaseName is the configured PostgreSQL database, absolute SQLite filename,
	// or :memory: for private SQLite storage. Memory leaves it empty.
	DatabaseName string
	// Namespace is the configured PostgreSQL schema; other built-ins leave it empty.
	Namespace string
}

// CollectionMetadata identifies a collection within its configured store.
type CollectionMetadata struct {
	StoreMetadata
	CollectionName string
}

// StoreMetadataProvider is optional; existing store interfaces remain unchanged.
// Implementations return configuration snapshots without I/O or live validation.
type StoreMetadataProvider interface {
	StoreMetadata() StoreMetadata
}

// CollectionMetadataProvider is optional for model and original Record collections.
// Discovery must not create storage or depend on its existence or connection state.
type CollectionMetadataProvider interface {
	CollectionMetadata() CollectionMetadata
}

// GetStoreMetadata returns provider metadata, or an empty value for providers
// without discovery. Nil (including typed-nil) values also return an empty value.
func GetStoreMetadata(store any) StoreMetadata {
	if !isNilValue(store) {
		if provider, ok := store.(StoreMetadataProvider); ok {
			return provider.StoreMetadata()
		}
	}
	return StoreMetadata{}
}

// GetCollectionMetadata uses optional discovery, falling back to Name for older
// providers. Both model and original Record collections and typed wrappers are
// accepted. Nil values return an empty value.
func GetCollectionMetadata(collection interface{ Name() string }) CollectionMetadata {
	if isNilValue(collection) {
		return CollectionMetadata{}
	}
	if provider, ok := collection.(CollectionMetadataProvider); ok {
		return provider.CollectionMetadata()
	}
	return CollectionMetadata{CollectionName: collection.Name()}
}

// CollectionMetadata forwards discovery without invoking the application codec.
func (c *TypedModelCollection[K, T]) CollectionMetadata() CollectionMetadata {
	return GetCollectionMetadata(c.base)
}

// CollectionMetadata forwards discovery for the original typed Record API.
func (c *TypedCollection[T]) CollectionMetadata() CollectionMetadata {
	return GetCollectionMetadata(c.base)
}
