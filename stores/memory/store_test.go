package memory_test

import (
	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/gabisonia/go-vectorstore/vectordata/conformance"
	"testing"
)

func TestModelContract(t *testing.T) {
	conformance.Run(t, func(t *testing.T) vectordata.ModelStore { return memory.New() })
}
