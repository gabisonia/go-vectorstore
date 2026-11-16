// Demonstrates typed integer keys and generated UUID keys without credentials.
package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/gabisonia/go-vectorstore/stores/memory"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

type Event struct {
	ID   uint64 `vectordata:"key"`
	Body string `vectordata:"data"`
}

type Sensor struct {
	ID int32 `vectordata:"key"`
}

type Document struct {
	ID   string `vectordata:"key,type=uuid,generated"`
	Body string `vectordata:"data"`
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	store := memory.New()
	sensors, err := vectordata.EnsureTypedModelCollection[int32, Sensor](ctx, store, "sensors", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	sensorKeys, err := sensors.Insert(ctx, []Sensor{{ID: math.MaxInt32}})
	if err != nil {
		return err
	}
	fmt.Printf("Typed int32 key: %d\n", sensorKeys[0])
	events, err := vectordata.EnsureTypedModelCollection[uint64, Event](ctx, store, "events", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	if _, err := events.Upsert(ctx, []Event{{ID: math.MaxUint64, Body: "largest unsigned key"}}); err != nil {
		return err
	}
	event, err := events.Get(ctx, math.MaxUint64, vectordata.RetrievalOptions{})
	if err != nil {
		return err
	}
	fmt.Printf("Unsigned key: %d\n", event.ID)
	documents, err := vectordata.EnsureTypedModelCollection[string, Document](ctx, store, "documents", nil, vectordata.ModelCollectionOptions{})
	if err != nil {
		return err
	}
	input := []Document{{Body: "first version"}}
	keys, err := documents.Insert(ctx, input)
	if err != nil {
		return err
	}
	fmt.Printf("Generated UUID: %t; caller ID unchanged: %t\n", len(keys[0]) == 36, input[0].ID == "")
	// Reuse the returned key to update instead of inserting a second document.
	input[0].ID, input[0].Body = keys[0], "updated version"
	if _, err := documents.Upsert(ctx, input); err != nil {
		return err
	}
	document, err := documents.Get(ctx, keys[0], vectordata.RetrievalOptions{})
	if err != nil {
		return err
	}
	fmt.Printf("Retrieved: %s\n", document.Body)
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
