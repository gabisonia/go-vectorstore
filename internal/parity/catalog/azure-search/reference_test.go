//go:build referenceparity

package azuresearchparity

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

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
	httpClient := http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	post := func(path, body string, out any) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/indexes/"+index+"/docs/"+path+"?api-version=2025-09-01", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			t.Fatalf("%s returned %s", path, resp.Status)
		}
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatal(err)
		}
	}
	var write struct {
		Value []struct {
			Status bool
			Key    string
		}
	}
	post("index", `{"value":[{"@search.action":"upload","Id":"go","Body":"Go document","Direction":[1,0]}]}`, &write)
	if len(write.Value) != 1 || write.Value[0].Key != "go" || !write.Value[0].Status {
		t.Fatalf("upload: %+v", write)
	}
	for {
		var hits struct{ Value []struct{ Id, Body string } }
		post("search", `{"select":"Id,Body","top":2,"vectorQueries":[{"kind":"vector","vector":[1,0],"fields":"Direction","k":2}]}`, &hits)
		if len(hits.Value) == 2 {
			if hits.Value[0].Id != "go" || hits.Value[1].Id != "seed" || hits.Value[1].Body != "reference" {
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
	if err := ref(ctx, "verify"); err != nil {
		t.Fatal(err)
	}
}
