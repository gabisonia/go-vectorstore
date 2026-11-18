package demo

import "testing"

func TestPostgresDSNManualAndAspireConfiguration(t *testing.T) {
	t.Setenv("PG_DSN", "")
	t.Setenv("PGHOST", "")
	if got := PostgresDSN("manual-default"); got != "manual-default" {
		t.Fatal(got)
	}
	t.Setenv("PGHOST", "localhost")
	if got := PostgresDSN("manual-default"); got != "" {
		t.Fatal("Aspire PG variables ignored:", got)
	}
	t.Setenv("PG_DSN", " explicit-dsn ")
	if got := PostgresDSN("manual-default"); got != "explicit-dsn" {
		t.Fatal("explicit DSN did not override discovery:", got)
	}
}
