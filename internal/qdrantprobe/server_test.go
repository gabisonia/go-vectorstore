//go:build qdrantprobe

package qdrantprobe

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
	testcontainers "github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const version = "1.20.2"

var endpoint, apiKey string

func TestMain(m *testing.M) {
	endpoint = strings.TrimRight(os.Getenv("QDRANT_TEST_URL"), "/")
	apiKey = os.Getenv("QDRANT_TEST_API_KEY")
	var container testcontainers.Container
	if endpoint == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		var err error
		container, err = testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
			ContainerRequest: testcontainers.ContainerRequest{
				Image:        "qdrant/qdrant:v" + version,
				ExposedPorts: []string{"6333/tcp"},
				Env:          map[string]string{"QDRANT__TELEMETRY_DISABLED": "true"},
				WaitingFor:   wait.ForHTTP("/readyz").WithPort("6333/tcp").WithStartupTimeout(2 * time.Minute),
			}, Started: true,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "start Qdrant probe container: %v\n", err)
			os.Exit(1)
		}
		host, hostErr := container.Host(ctx)
		port, portErr := container.MappedPort(ctx, "6333/tcp")
		if hostErr != nil || portErr != nil {
			_ = container.Terminate(ctx)
			fmt.Fprintf(os.Stderr, "resolve Qdrant probe container: %v / %v\n", hostErr, portErr)
			os.Exit(1)
		}
		endpoint = "http://" + net.JoinHostPort(host, port.Port())
		// Credentials for an explicitly supplied server do not apply to our
		// freshly created, isolated container.
		apiKey = ""
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		fmt.Fprintln(os.Stderr, "QDRANT_TEST_URL must be an http(s) server URL without userinfo/query/fragment")
		os.Exit(1)
	}
	// A supplied CI service may still be starting. Poll only a side-effect-free
	// readiness read; never add retry behavior to point/collection writes.
	readyCtx, readyCancel := context.WithTimeout(context.Background(), 30*time.Second)
	ready := false
	for !ready {
		status, err := request(readyCtx, client(), "GET", "/readyz", nil, nil)
		ready = err == nil && status == http.StatusOK
		if ready {
			break
		}
		select {
		case <-readyCtx.Done():
			fmt.Fprintln(os.Stderr, "Qdrant probe server did not become ready")
		case <-time.After(100 * time.Millisecond):
			continue
		}
		break
	}
	readyCancel()
	code := 1
	if ready {
		code = m.Run()
	}
	if container != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := container.Terminate(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "terminate Qdrant probe container: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

func client() *http.Client {
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

// request never retries writes, including unknown outcomes. Response payloads
// stay out of error messages: canonical records can contain application data.
func request(ctx context.Context, c *http.Client, method, path string, body any, out any) (int, error) {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return 0, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint+path, bytes.NewReader(data))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("api-key", apiKey)
	}
	response, err := c.Do(req)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	const limit = 8 << 20
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return response.StatusCode, err
	}
	if len(raw) > limit {
		return response.StatusCode, fmt.Errorf("probe response exceeds %d bytes", limit)
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 && out != nil {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(out); err != nil {
			return response.StatusCode, err
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			return response.StatusCode, fmt.Errorf("unexpected trailing response")
		}
	}
	return response.StatusCode, nil
}

type response[T any] struct {
	Result T `json:"result"`
}

func call[T any](t *testing.T, method, path string, body any) T {
	t.Helper()
	var out response[T]
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	status, err := request(ctx, client(), method, path, body, &out)
	if err != nil || status < 200 || status >= 300 {
		t.Fatalf("%s %s: HTTP %d: %v", method, path, status, err)
	}
	return out.Result
}

func collection(t *testing.T, config map[string]any) string {
	t.Helper()
	id, err := vectordata.NewUUIDKey()
	if err != nil {
		t.Fatal(err)
	}
	name := "vectorstore_probe_" + strings.ReplaceAll(id, "-", "")
	path := "/collections/" + name
	t.Cleanup(func() {
		// Cleanup must outlive a canceled test context and touches only this
		// test's unpredictable, owned name, including on external servers.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		status, err := request(ctx, client(), "DELETE", path, nil, nil)
		if err != nil || status != 200 && status != 404 {
			t.Errorf("cleanup owned collection: HTTP %d: %v", status, err)
		}
	})
	if !call[bool](t, "PUT", path, config) {
		t.Fatal("collection creation returned false")
	}
	return path
}

func vectorConfig(distance string) map[string]any {
	return map[string]any{"vectors": map[string]any{"v": map[string]any{"size": 2, "distance": distance}}, "shard_number": 1, "replication_factor": 1}
}

type point struct {
	ID      json.RawMessage            `json:"id"`
	Payload map[string]json.RawMessage `json:"payload"`
	Vector  map[string][]float32       `json:"vector"`
	Score   float64                    `json:"score"`
}

func put(t *testing.T, path string, points []map[string]any, extra map[string]any) {
	t.Helper()
	body := map[string]any{"points": points}
	for key, value := range extra {
		body[key] = value
	}
	result := call[struct {
		Status string `json:"status"`
	}](t, "PUT", path+"/points?wait=true&ordering=strong", body)
	if result.Status != "completed" {
		t.Fatalf("write outcome %q", result.Status)
	}
}

func retrieve(t *testing.T, path string, ids ...any) []point {
	t.Helper()
	return call[[]point](t, "POST", path+"/points", map[string]any{"ids": ids, "with_payload": true, "with_vector": true})
}

func numericID(t *testing.T, p point) uint64 {
	t.Helper()
	var id uint64
	if err := json.Unmarshal(p.ID, &id); err != nil {
		t.Fatal(err)
	}
	return id
}

func payloadString(t *testing.T, p point, key string) string {
	t.Helper()
	var value string
	if err := json.Unmarshal(p.Payload[key], &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func query(t *testing.T, path string, body map[string]any) []point {
	t.Helper()
	return call[struct {
		Points []point `json:"points"`
	}](t, "POST", path+"/points/query", body).Points
}
