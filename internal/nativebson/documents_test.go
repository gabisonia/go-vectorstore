package nativebson

import (
	"context"
	"errors"
	"testing"

	"go.mongodb.org/mongo-driver/v2/mongo"
)

func TestWriteOutcomes(t *testing.T) {
	unknown := errors.New("unknown")
	rejected := mongo.WriteException{WriteErrors: mongo.WriteErrors{{Code: 121, Message: "document validation failed"}}}
	if errors.Is(writeError(unknown, rejected), unknown) {
		t.Fatal("single-row validation rejection marked uncertain")
	}
	uncertain := writeError(unknown, context.DeadlineExceeded)
	if !errors.Is(uncertain, unknown) || !errors.Is(uncertain, context.DeadlineExceeded) {
		t.Fatal(uncertain)
	}
}
