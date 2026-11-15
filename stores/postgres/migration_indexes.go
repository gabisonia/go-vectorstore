package postgres

import (
	"fmt"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type modelIndexSpec struct {
	property, name, method, column, opClass, with string
	fullText                                      bool
}

func (x modelIndexSpec) createSQL(c *ModelCollection) string {
	expression := quoteIdent(x.column) + " " + x.opClass
	if x.fullText {
		expression = ftsExpr(quoteIdent(x.column))
	}
	return "CREATE INDEX " + quoteIdent(x.name) + " ON " + c.table() + " USING " + x.method + " (" + expression + ")" + x.with
}
func modelIndexSpecs(collection string, d vectordata.CollectionDefinition) ([]modelIndexSpec, error) {
	var indexes []modelIndexSpec
	for _, p := range d.Data {
		if p.IsIndexed {
			name, err := resolveIndexName("", fmt.Sprintf("idx_%s_%s_data", collection, p.StorageName))
			if err != nil {
				return nil, err
			}
			method := "btree"
			if _, array := p.Type.ArrayElementType(); array {
				method = "gin"
			}
			indexes = append(indexes, modelIndexSpec{property: p.Name, name: name, method: method, column: p.StorageName, opClass: dataOpClass(p.Type)})
		}
		if p.IsFullTextIndexed {
			name, err := resolveIndexName("", fmt.Sprintf("idx_%s_%s_fts", collection, p.StorageName))
			if err != nil {
				return nil, err
			}
			indexes = append(indexes, modelIndexSpec{property: p.Name, name: name, method: "gin", column: p.StorageName, opClass: "tsvector_ops", fullText: true})
		}
	}
	for _, p := range d.Vectors {
		if p.Index == nil {
			continue
		}
		method := defaultIndexMethod(p.Index.Method)
		op, err := vectorOpClass(p)
		if err != nil {
			return nil, err
		}
		with, err := buildVectorIndexWithClause(method, p.Index)
		if err != nil {
			return nil, err
		}
		name, err := resolveIndexName(p.Index.Name, fmt.Sprintf("idx_%s_%s_%s", collection, p.StorageName, method))
		if err != nil {
			return nil, err
		}
		indexes = append(indexes, modelIndexSpec{property: p.Name, name: name, method: string(method), column: p.StorageName, opClass: op, with: with})
	}
	seen := map[string]bool{}
	for _, index := range indexes {
		if seen[index.name] || strings.ContainsRune(index.name, 0) || index.name == collection || index.name == modelRegistry {
			return nil, fmt.Errorf("%w: duplicate/invalid index name %q", vectordata.ErrSchemaMismatch, index.name)
		}
		seen[index.name] = true
	}
	return indexes, nil
}
