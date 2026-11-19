// vectordata-gen generates typed property references for tagged Go models.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/gabisonia/go-vectorstore/internal/fieldgen"
)

func main() {
	model := flag.String("type", "", "Local tagged model type")
	output := flag.String("output", "", "Generated .go basename (default <type>_fields.gen.go)")
	name := flag.String("name", "", "Reference variable (default <type>Fields)")
	check := flag.Bool("check", false, "Check that generated output is current without writing")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := fieldgen.Run(ctx, fieldgen.Options{Model: *model, Output: *output, Name: *name, Check: *check}); err != nil {
		fmt.Fprintln(os.Stderr, "vectordata-gen:", err)
		os.Exit(1)
	}
}
