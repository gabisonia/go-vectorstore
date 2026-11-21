//go:build referenceparity

package azuresearchparity

import (
	"bytes"
	"context"
	"errors"
	"net/http"

	"fmt"
	"io"

	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/connectors/azuresearch"
	"github.com/google/uuid"
)

func TestLocalSearch(t *testing.T) {
	if os.Getenv("CI") == "true" {
		t.Skip("live Azure tests run locally")
	}
	name := os.Getenv("AZURE_SEARCH_SERVICE")
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,58}[a-z0-9]$`).MatchString(name) {
		t.Fatal("expected Azure Search service name")
	}
	token, err := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
	if err != nil || len(bytes.TrimSpace(token)) == 0 {
		t.Fatal("expected Azure CLI search token on stdin")
	}
	endpoint := "https://" + name + ".search.windows.net"
	index := "go-vectordata-parity-" + uuid.NewString()
	t.Logf("temporary index %s", index)
	ref := func(ctx context.Context, phase string) error {
		cmd := exec.CommandContext(ctx, "dotnet", "Reference.dll", phase, endpoint, index)
		cmd.Stdin = bytes.NewReader(token)
		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s: %w: %s", phase, err, output)
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	if err := ref(ctx, "create"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := ref(ctx, "cleanup"); err != nil {
			t.Errorf("cleanup %s: %v", index, err)
		}
	})
	if err := ref(ctx, "seed"); err != nil {
		t.Fatal(err)
	}
	collection, err := azuresearch.AttachNative(ctx, azuresearch.Options{Endpoint: endpoint, Token: func(context.Context) (string, error) { return strings.TrimSpace(string(token)), nil }}, index, azuresearch.NativeSchema{Key: "Id", Text: "Body", Vector: "Direction", Dimensions: 2})
	if err != nil {
		t.Fatal(err)
	}
	if err := collection.Upsert(ctx, azuresearch.NativeDocument{ID: "go", Text: "Go document", Vector: []float32{1, 0}}); err != nil {
		t.Fatal(err)
	}
	for {
		hits, err := collection.Search(ctx, []float32{1, 0}, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(hits) == 2 {
			if hits[0].ID != "go" || hits[1].ID != "seed" || hits[1].Text != "reference" || hits[0].Score <= hits[1].Score {
				t.Fatalf("Go ranking: %+v", hits)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	seedText, err := collection.GetText(ctx, "seed")
	if err != nil || seedText != "reference" {
		t.Fatalf("C# document read: %q %v", seedText, err)
	}
	if err := ref(ctx, "verify"); err != nil {
		t.Fatal(err)
	}
	if err := collection.Delete(ctx, "go"); err != nil {
		t.Fatal(err)
	}
	for {
		_, err := collection.GetText(ctx, "go")
		var status *azuresearch.ResponseError
		if errors.As(err, &status) && status.StatusCode == http.StatusNotFound {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	if err := ref(ctx, "verify-delete"); err != nil {
		t.Fatal(err)
	}
}
