package vectordata_test

import (
	"fmt"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

func ExampleWithProviderAnnotation() {
	a, err := vectordata.WithProviderAnnotation(vectordata.ProviderAnnotations(""), "vendor.limit", 100)
	if err != nil {
		panic(err)
	}
	p := vectordata.DataProperty{Name: "Body", Type: vectordata.TypeString, ProviderAnnotations: a}
	value, found, err := vectordata.GetProviderAnnotation[int](p.ProviderAnnotations, "vendor.limit")
	if err != nil {
		panic(err)
	}
	fmt.Println(value, found)
	changed, err := vectordata.WithProviderAnnotation(p.ProviderAnnotations, "vendor.limit", 200)
	if err != nil {
		panic(err)
	}
	fmt.Println("original unchanged:", p.ProviderAnnotations != changed)
	// Output:
	// 100 true
	// original unchanged: true
}
