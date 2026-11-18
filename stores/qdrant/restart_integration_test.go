//go:build integration

package qdrant

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/testcontainers/testcontainers-go"
)

func killAndRestart(t *testing.T, s *Store) {
	t.Helper()
	// Only TestMain's owned fixture can be killed. Never infer Docker ownership
	// from QDRANT_TEST_URL, and never restart a supplied development/CI service.
	if ownedContainer == nil {
		t.Fatal("restart requires the owned test fixture")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cli, err := testcontainers.NewDockerClientWithOpts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	if err := cli.ContainerKill(ctx, ownedContainer.GetContainerID(), "SIGKILL"); err != nil {
		t.Fatal(err)
	}
	done, failed := cli.ContainerWait(ctx, ownedContainer.GetContainerID(), "not-running")
	select {
	case result := <-done:
		if result.StatusCode != 137 {
			t.Fatalf("expected SIGKILL exit, got %d", result.StatusCode)
		}
	case err := <-failed:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := ownedContainer.Start(ctx); err != nil {
		t.Fatal(err)
	}
	host, err := ownedContainer.Host(ctx)
	if err != nil {
		t.Fatal(err)
	}
	port, err := ownedContainer.MappedPort(ctx, "6333/tcp")
	if err != nil {
		t.Fatal(err)
	}
	integrationURL = "http://" + net.JoinHostPort(host, port.Port())
	s.url = integrationURL
	if s.transport != nil {
		s.transport.CloseIdleConnections()
	}
	if _, err := s.request(ctx, "GET", "/readyz", nil, nil); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationRestartCommittedSnapshotsAndSequence(t *testing.T) {
	if ownedContainer == nil {
		t.Skip("restart tests require an owned container; unset QDRANT_TEST_URL")
	}
	s := integrationStore(t, StoreOptions{})
	c := generated(t, s)
	if _, err := c.Insert(t.Context(), body("committed")); err != nil {
		t.Fatal(err)
	}
	before, err := s.readRoot(t.Context(), c.name)
	if err != nil {
		t.Fatal(err)
	}
	killAndRestart(t, s)
	// A fresh client and handle must discover persistent ownership, registration,
	// records and sequence; the original client's memory is not recovery state.
	reopened, err := New(StoreOptions{URL: integrationURL, Namespace: s.namespace})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	h, err := reopened.GetModelCollection(c.name, c.definition, vectordata.ModelCollectionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	row, err := h.Get(t.Context(), int64(1), vectordata.RetrievalOptions{})
	if err != nil || row.Data["Body"] != "committed" {
		t.Fatalf("lost acknowledged record: %+v %v", row, err)
	}
	if err := reopened.ConfirmWrite(t.Context(), c.name, before.Revision); err != nil {
		t.Fatal(err)
	}
	keys, err := h.Insert(t.Context(), body("after restart"))
	if err != nil || keys[0] != int64(2) {
		t.Fatalf("lost sequence: %v %v", keys, err)
	}
}

func TestIntegrationRestartUnknownPublicationAndTombstone(t *testing.T) {
	if ownedContainer == nil {
		t.Skip("restart tests require an owned container; unset QDRANT_TEST_URL")
	}
	s := integrationStore(t, StoreOptions{})
	c := generated(t, s)
	transport := s.http.Transport
	var wrote atomic.Bool
	lost := errors.New("lost publication acknowledgement before process restart")
	s.http.Transport = roundTripper(func(r *http.Request) (*http.Response, error) {
		if r.Method == "PUT" && strings.HasSuffix(r.URL.Path, "/points") {
			response, err := transport.RoundTrip(r)
			if err != nil {
				return nil, err
			}
			response.Body.Close()
			wrote.Store(true)
			return nil, lost
		}
		if wrote.Load() {
			return nil, lost
		}
		return transport.RoundTrip(r)
	})
	_, writeErr := c.Insert(t.Context(), body("unknown"))
	s.http.Transport = transport
	var outcome *OutcomeUnknownError
	if !errors.As(writeErr, &outcome) || !errors.Is(writeErr, lost) {
		t.Fatal(writeErr)
	}
	killAndRestart(t, s)
	if err := s.ConfirmWrite(t.Context(), c.name, outcome.Token); err != nil {
		t.Fatal("restart lost receipt:", err)
	}
	old, err := s.readRoot(t.Context(), c.name)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteCollection(t.Context(), c.name); err != nil {
		t.Fatal(err)
	}
	killAndRestart(t, s)
	if exists, err := s.CollectionExists(t.Context(), c.name); err != nil || exists {
		t.Fatalf("tombstone lost: %t %v", exists, err)
	}
	if err := c.EnsureExists(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.transition(t.Context(), c.name, old, cloneSnapshot(old)); !errors.Is(err, errConflict) {
		t.Fatalf("stale operation changed new epoch: %v", err)
	}
	if err := s.ConfirmWrite(t.Context(), c.name, outcome.Token); err != nil {
		t.Fatal("recreation evicted receipt:", err)
	}
	keys, err := c.Insert(t.Context(), body("new incarnation"))
	if err != nil || keys[0] != int64(1) {
		t.Fatalf("old sequence crossed epoch: %v %v", keys, err)
	}
}
