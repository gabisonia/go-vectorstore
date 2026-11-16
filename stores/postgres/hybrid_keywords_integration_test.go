//go:build integration

package postgres

import (
	"context"
	"testing"
)

func TestIntegrationHybridKeywordTermParsing(t *testing.T) {
	ctx := context.Background()
	pool := integrationPool(t)
	for _, tt := range []struct {
		terms []string
		body  string
		match bool
	}{
		{[]string{"cloud costs", "cache"}, "costs of the cloud", true},
		{[]string{"cloud costs", "cache"}, "cloud", false},
		{[]string{"cloud costs", "cache"}, "CACHE", true},
		{[]string{"cat | dog"}, "cat", false},
		{[]string{"cat | dog"}, "dog and cat", true},
		{[]string{"cache", "cache"}, "cache", true},
		{[]string{"---", "cache"}, "cache", true},
		{[]string{"---", "!!!"}, "anything", false},
		{[]string{"école"}, "ÉCOLE", true},
		{[]string{"cache'); DROP TABLE data; --"}, "cache", false},
	} {
		query, arg, err := hybridKeywords("", tt.terms)
		if err != nil {
			t.Fatal(err)
		}
		var got bool
		if err := pool.QueryRow(ctx, "SELECT to_tsvector('simple'::regconfig,$1::text) @@ "+query, tt.body, arg).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != tt.match {
			t.Fatalf("terms %v / body %q: got %t, want %t", tt.terms, tt.body, got, tt.match)
		}
	}
}
