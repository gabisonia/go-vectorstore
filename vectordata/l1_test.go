package vectordata_test

import (
	"testing"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func TestL1TaggedModel(t *testing.T) {
	type model struct {
		ID     string    `vectordata:"key"`
		Vector []float32 `vectordata:"vector,dimensions=3,metric=l1,precision=float16,index"`
	}
	d, err := vectordata.DefinitionFor[model]()
	if err != nil || d.Vectors[0].Metric != vectordata.DistanceL1 || d.Vectors[0].Index.Metric != vectordata.DistanceL1 {
		t.Fatalf("L1 tags: %+v, %v", d, err)
	}
	if err := vectordata.ValidateModelVector([]float32{0, 0, 0}, d.Vectors[0]); err != nil {
		t.Fatalf("zero L1 vector rejected: %v", err)
	}
}
