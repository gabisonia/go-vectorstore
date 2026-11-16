package postgres

import (
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestSquaredL2RecordPlanKeepsANNOrdering(t *testing.T) {
	c := testCollection()
	c.metric = vectordata.DistanceL2Squared
	threshold := 25.0
	plan, err := c.buildSearchPlan([]float32{0, 0, 0}, 2, vectordata.SearchOptions{Threshold: &threshold, IVFFlat: &vectordata.IVFFlatSearchOptions{IterativeScan: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.query, `power(("vector" <-> $1::vector), 2) AS distance`) || !strings.Contains(plan.query, `ORDER BY ("vector" <-> $1::vector) ASC`) || !strings.Contains(plan.query, `ORDER BY distance + 0 ASC`) {
		t.Fatalf("squared projection/native ordering missing: %s", plan.query)
	}
}
