package exactsearch_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type writingOperand struct {
	write func() error
	calls *atomic.Int32
}

func (v writingOperand) MarshalJSON() ([]byte, error) {
	v.calls.Add(1)
	if err := v.write(); err != nil {
		return nil, err
	}
	return []byte("1"), nil
}

func TestFilterCodecsRunOutsideProviderResources(t *testing.T) {
	for name, store := range stores(t) {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			d := vectordata.CollectionDefinition{Key: vectordata.KeyProperty{Name: "ID"}, Data: []vectordata.DataProperty{{Name: "JSON", Type: vectordata.TypeJSON}}, Vectors: []vectordata.VectorProperty{{Name: "V", Dimensions: 1, Metric: vectordata.DistanceL2}}}
			c, err := store.EnsureModelCollection(ctx, "callbacks", d, vectordata.ModelCollectionOptions{})
			if err != nil {
				t.Fatal(err)
			}
			record := vectordata.ModelRecord{Key: "kept", Data: map[string]any{"JSON": map[string]any{"n": json.Number("1")}}, Vectors: map[string][]float32{"V": {1}}}
			records := []vectordata.ModelRecord{record}
			for i := 0; i < 10; i++ {
				other := record
				other.Key = fmt.Sprintf("other-%d", i)
				records = append(records, other)
			}
			if _, err := c.Insert(ctx, records); err != nil {
				t.Fatal(err)
			}
			calls := new(atomic.Int32)
			operand := writingOperand{calls: calls, write: func() error {
				// Bound a deadlock while allowing a real SQLite write on a busy race runner.
				deadline, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				done := make(chan error, 1)
				go func() { _, err := c.Upsert(deadline, []vectordata.ModelRecord{record}); done <- err }()
				select {
				case err := <-done:
					return err
				case <-deadline.Done():
					return fmt.Errorf("filter codec held a provider resource: %w", deadline.Err())
				}
			}}
			results, err := c.Search(ctx, []float32{1}, 1, vectordata.ModelSearchOptions{Filter: vectordata.Eq(vectordata.DataPath("JSON", "n"), operand)})
			if err != nil || len(results) != 1 {
				t.Fatalf("reentrant filter: %+v %v", results, err)
			}
			if calls.Load() == 0 || calls.Load() > 4 {
				t.Fatalf("filter callback was invoked per record: %d", calls.Load())
			}
			// Application iteration starts after the scan has released locks/connections.
			for _, err := range c.SearchIter(ctx, []float32{1}, 1, vectordata.ModelSearchOptions{}) {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := c.Upsert(ctx, []vectordata.ModelRecord{record}); err != nil {
					t.Fatal(err)
				}
				break
			}
		})
	}
}
