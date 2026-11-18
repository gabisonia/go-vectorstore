package qdrant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const maxHTTPBytes = 64 << 20

// Only this error proves rejection before any request was sent. A response-size
// failure can happen after a committed write and must remain reconcilable.
var errRequestLimit = fmt.Errorf("%w: encoded request exceeds HTTP limit", ErrLimit)

type response[T any] struct {
	Result T `json:"result"`
}

// HTTPError preserves the status without exposing response/document bodies.
type HTTPError struct{ StatusCode int }

func (e *HTTPError) Error() string { return fmt.Sprintf("Qdrant HTTP %d", e.StatusCode) }
func statusIs(err error, code int) bool {
	var status *HTTPError
	return errors.As(err, &status) && status.StatusCode == code
}
func newHTTPClient(configured *http.Client) (*http.Client, *http.Transport) {
	c := &http.Client{Timeout: 30 * time.Second}
	var owned *http.Transport
	if configured != nil {
		*c = *configured
	}
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	if c.Transport == nil {
		if transport, ok := http.DefaultTransport.(*http.Transport); ok {
			owned = transport.Clone()
			c.Transport = owned
		} else {
			c.Transport = http.DefaultTransport
		}
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return c, owned
}

// request bounds both directions, rejects redirects, and never retries writes.
// Status errors intentionally omit response bodies and canonical application data.
func (s *Store) request(ctx context.Context, method, path string, body, out any) (int, error) {
	if s.closed.Load() {
		return 0, ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var raw []byte
	var err error
	if body != nil {
		raw, err = json.Marshal(body)
		if err != nil {
			return 0, err
		}
		if len(raw) > maxHTTPBytes {
			return 0, errRequestLimit
		}
	}
	// Deliberately hide GetBody, so net/http cannot replay PUT after a reused
	// connection failure. Conditional updates still have uncertain outcomes.
	req, err := http.NewRequestWithContext(ctx, method, s.url+path, io.NopCloser(bytes.NewReader(raw)))
	if err != nil {
		return 0, err
	}
	req.ContentLength = int64(len(raw))
	req.Header.Set("Content-Type", "application/json")
	if s.apiKey != "" {
		req.Header.Set("api-key", s.apiKey)
	}
	r, err := s.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer r.Body.Close()
	if r.StatusCode < 200 || r.StatusCode >= 300 {
		return r.StatusCode, &HTTPError{StatusCode: r.StatusCode}
	}
	raw, err = io.ReadAll(io.LimitReader(r.Body, maxHTTPBytes+1))
	if err != nil {
		return r.StatusCode, err
	}
	if len(raw) > maxHTTPBytes {
		return r.StatusCode, ErrLimit
	}
	if out != nil {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err = decoder.Decode(out); err != nil {
			return r.StatusCode, ErrCorrupt
		}
		if err = decoder.Decode(new(any)); err != io.EOF {
			return r.StatusCode, ErrCorrupt
		}
	}
	return r.StatusCode, nil
}
