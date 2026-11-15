// Package vectordata defines portable vector-store contracts and typed model APIs.
//
// Start with EnsureTypedModelCollection and a tagged Go struct, or use ModelStore,
// CollectionDefinition and ModelRecord for dynamic schemas. Embedder supports
// automatic text embeddings; optional interfaces expose binary/sparse queries,
// hybrid search, capability discovery and migration planning/application.
// The original Record and Collection APIs remain supported by PostgreSQL.
//
// Model reads omit vectors by default. Select named vectors explicitly when a
// model has more than one, check iterator errors, and propagate request contexts.
// Provider capabilities differ; see the executable examples and usage guides.
//
// Usage guides: https://github.com/gabisonia/go-vectorstore/blob/master/docs/ai-guide.md
// and https://github.com/gabisonia/go-vectorstore/blob/master/docs/model-api.md.
package vectordata
