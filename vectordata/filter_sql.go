package vectordata

import (
	"github.com/gabisonia/go-vectorstore/stores/postgres/filtersql"
	"github.com/gabisonia/go-vectorstore/vectordata/filterast"
)

// FilterSQLConfig is retained for source compatibility.
// Deprecated: use stores/postgres/filtersql.FilterSQLConfig for PostgreSQL SQL.
type FilterSQLConfig = filtersql.FilterSQLConfig

// CompileFilterSQL is retained for source compatibility.
// Deprecated: use stores/postgres/filtersql.CompileFilterSQL.
func CompileFilterSQL(filter Filter, cfg FilterSQLConfig, startArg int) (string, []any, int, error) {
	return filtersql.CompileFilterSQL(filter, cfg, startArg)
}

const numericTextPattern = filterast.NumericTextPattern

var numericLiteral = filterast.NumericLiteral
var isNilValue = filterast.IsNilValue
