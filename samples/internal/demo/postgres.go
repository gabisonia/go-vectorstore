package demo

import (
	"os"
	"strings"
)

// PostgresDSN preserves explicit/manual DSNs and lets Aspire supply standard
// PG* variables. An empty connection string tells pgx to read those variables.
func PostgresDSN(fallback string) string {
	if dsn := strings.TrimSpace(os.Getenv("PG_DSN")); dsn != "" {
		return dsn
	}
	if strings.TrimSpace(os.Getenv("PGHOST")) != "" {
		return ""
	}
	return fallback
}
