module github.com/gabisonia/go-vectorstore/internal/parity/catalog/sqlserver

go 1.25.0

require github.com/microsoft/go-mssqldb v1.12.0

require (
	github.com/gabisonia/go-vectorstore v0.0.0
	github.com/golang-sql/civil v0.0.0-20220223132316-b832511892a9 // indirect
	github.com/golang-sql/sqlexp v0.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/shopspring/decimal v1.4.0 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)

replace github.com/gabisonia/go-vectorstore => ../../../..
