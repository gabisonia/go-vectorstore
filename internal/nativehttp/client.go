// Package nativehttp runs JSON requests for native REST collection adapters.
package nativehttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client never follows redirects or replays requests at the application layer.
type Client struct {
	endpoint     string
	httpClient   *http.Client
	statusError  func(int) error
	unknownWrite error
}

// New accepts a service origin and a fixed API prefix.
func New(endpoint, prefix string, t http.RoundTripper, statusError func(int) error, unknown error) (*Client, error) {
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Trim(u.Path, "/") != "" {
		return nil, errors.New("invalid native service origin")
	}
	return &Client{u.Scheme + "://" + u.Host + prefix, &http.Client{Transport: t, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, statusError, unknown}, nil
}
func (c *Client) Request(ctx context.Context, method, path string, body, out any, write bool) error {
	var data []byte
	var err error
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	resp, err := c.httpClient.Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			err = c.statusError(resp.StatusCode)
			if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != 408 {
				return err
			}
		} else if out != nil {
			decoder := json.NewDecoder(io.LimitReader(resp.Body, 32<<20))
			decoder.UseNumber()
			err = decoder.Decode(out)
		}
	}
	if write && err != nil {
		return errors.Join(c.unknownWrite, err)
	}
	return err
}
