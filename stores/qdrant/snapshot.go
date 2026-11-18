package qdrant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

const maxDefinitionBytes = 64 << 10

var (
	ErrLimit          = fmt.Errorf("%w: Qdrant snapshot or retained receipt limit exhausted", vectordata.ErrInvalidRecord)
	ErrCorrupt        = fmt.Errorf("%w: corrupt Qdrant canonical state", vectordata.ErrInvalidRecord)
	ErrOutcomeUnknown = errors.New("Qdrant write outcome unknown")
	ErrConflict       = fmt.Errorf("%w: Qdrant revision conflict", vectordata.ErrInvalidRecord)
	errConflict       = ErrConflict
	ErrClosed         = errors.New("Qdrant store is closed")
	errRootAbsent     = fmt.Errorf("%w: Qdrant physical collection has no initialized root", vectordata.ErrCollectionNotFound)
)

// OutcomeUnknownError carries a non-secret receipt token for ConfirmWrite.
// Never retry a logical write solely because this error was returned.
type OutcomeUnknownError struct {
	Token string
	Err   error
}

func (e *OutcomeUnknownError) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%s (receipt %s)", ErrOutcomeUnknown, e.Token)
	}
	return fmt.Sprintf("%s (receipt %s): %v", ErrOutcomeUnknown, e.Token, e.Err)
}
func (e *OutcomeUnknownError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrOutcomeUnknown}
	}
	return []error{ErrOutcomeUnknown, e.Err}
}
func unknown(token string, err error) error { return &OutcomeUnknownError{Token: token, Err: err} }

type snapshot struct {
	Format                      int
	Revision, Epoch, Definition string
	Active                      bool
	Next                        int64
	Records                     map[string]json.RawMessage
	Receipts                    []string
}

func cloneSnapshot(m snapshot) snapshot {
	m.Receipts = slices.Clone(m.Receipts)
	records := make(map[string]json.RawMessage, len(m.Records))
	for key, raw := range m.Records {
		records[key] = raw
	}
	m.Records = records
	return m
}
func (s *Store) validate(m snapshot) error {
	if m.Format != 1 {
		return vectordata.ErrSchemaMismatch
	}
	if len(m.Revision) != 36 || m.Next < 0 || m.Records == nil || (m.Active && (len(m.Epoch) != 36 || m.Definition == "")) {
		return ErrCorrupt
	}
	if len(m.Definition) > maxDefinitionBytes || len(m.Records) > s.limits.MaxRecords || len(m.Receipts) > s.limits.MaxReceipts {
		return ErrLimit
	}
	if !m.Active && len(m.Records) != 0 {
		return ErrCorrupt
	}
	seen := make(map[string]bool, len(m.Receipts))
	for _, receipt := range m.Receipts {
		if len(receipt) != 36 || seen[receipt] {
			return ErrCorrupt
		}
		seen[receipt] = true
	}
	if !seen[m.Revision] {
		return ErrCorrupt
	}
	bytes := 0
	for key, raw := range m.Records {
		bytes += len(key) + len(raw)
		if bytes > s.limits.MaxCollectionBytes {
			return ErrLimit
		}
	}
	return nil
}
func (s *Store) payload(m snapshot) (map[string]any, error) {
	if err := s.validate(m); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return map[string]any{"canonical": string(raw), "revision": m.Revision, "epoch": m.Epoch}, nil
}

type physicalOwner struct {
	Format          int
	Namespace, Name string
	Limits          Limits
}
type collectionInfo struct {
	Result struct {
		Config struct {
			Params struct {
				Shards   int            `json:"shard_number"`
				Replicas int            `json:"replication_factor"`
				Sharding string         `json:"sharding_method"`
				Vectors  map[string]any `json:"vectors"`
			} `json:"params"`
			Metadata map[string]json.RawMessage `json:"metadata"`
		} `json:"config"`
	} `json:"result"`
}

func (s *Store) checkServer(ctx context.Context) error {
	var info struct {
		Version string `json:"version"`
	}
	if _, err := s.request(ctx, "GET", "/", nil, &info); err != nil {
		return err
	}
	if info.Version != "1.20.2" {
		return fmt.Errorf("%w: Qdrant foundation requires verified version 1.20.2", vectordata.ErrUnsupported)
	}
	var cluster response[struct {
		Status string `json:"status"`
	}]
	if _, err := s.request(ctx, "GET", "/cluster", nil, &cluster); err != nil {
		return err
	}
	if cluster.Result.Status != "disabled" {
		return fmt.Errorf("%w: Qdrant foundation requires standalone mode", vectordata.ErrUnsupported)
	}
	return nil
}
func (s *Store) checkPhysical(ctx context.Context, name string) error {
	if err := s.checkServer(ctx); err != nil {
		return err
	}
	var info collectionInfo
	if _, err := s.request(ctx, "GET", s.path(name), nil, &info); err != nil {
		if statusIs(err, http.StatusNotFound) {
			return vectordata.ErrCollectionNotFound
		}
		return err
	}
	config := info.Result.Config
	if config.Params.Shards != 1 || config.Params.Replicas != 1 || config.Params.Sharding != "auto" || len(config.Params.Vectors) != 0 {
		return fmt.Errorf("%w: require one auto shard, one replica, and no native vectors", vectordata.ErrUnsupported)
	}
	var owner physicalOwner
	if json.Unmarshal(config.Metadata["go_vectorstore"], &owner) != nil || owner != (physicalOwner{Format: 1, Namespace: s.namespace, Name: name, Limits: s.limits}) {
		return fmt.Errorf("%w: foreign ownership or different Qdrant limits", vectordata.ErrSchemaMismatch)
	}
	return nil
}
func (s *Store) ensurePhysical(ctx context.Context, name string) error {
	err := s.checkPhysical(ctx, name)
	if !errors.Is(err, vectordata.ErrCollectionNotFound) {
		return err
	}
	_, writeErr := s.request(ctx, "PUT", s.path(name), map[string]any{
		"vectors": map[string]any{}, "shard_number": 1, "replication_factor": 1, "sharding_method": "auto",
		"metadata": map[string]any{"go_vectorstore": physicalOwner{Format: 1, Namespace: s.namespace, Name: name, Limits: s.limits}},
	}, nil)
	// Concurrent creates and lost acknowledgements are adopted only after exact
	// ownership/configuration readback. No collection is overwritten or deleted.
	if err = s.checkPhysical(ctx, name); err == nil {
		return nil
	}
	return errors.Join(err, writeErr)
}
func (s *Store) readRoot(ctx context.Context, name string) (snapshot, error) {
	if err := s.checkPhysical(ctx, name); err != nil {
		return snapshot{}, err
	}
	return s.readPoint(ctx, name)
}
func (s *Store) readPoint(ctx context.Context, name string) (snapshot, error) {
	var out response[[]struct {
		ID      json.RawMessage            `json:"id"`
		Payload map[string]json.RawMessage `json:"payload"`
	}]
	_, err := s.request(ctx, "POST", s.path(name)+"/points?consistency=all", map[string]any{"ids": []int{0}, "with_payload": true, "with_vector": false}, &out)
	if err != nil {
		return snapshot{}, err
	}
	if len(out.Result) == 0 {
		return snapshot{}, errRootAbsent
	}
	if len(out.Result) != 1 || string(out.Result[0].ID) != "0" {
		return snapshot{}, ErrCorrupt
	}
	var raw, revision, epoch string
	for key, target := range map[string]*string{"canonical": &raw, "revision": &revision, "epoch": &epoch} {
		if json.Unmarshal(out.Result[0].Payload[key], target) != nil {
			return snapshot{}, ErrCorrupt
		}
	}
	var m snapshot
	// Canonical JSON written here has bounded escaping/punctuation overhead.
	// Reject oversized state before allocating an untrusted record/receipt map.
	canonicalLimit := 2*s.limits.MaxCollectionBytes + 4*s.limits.MaxRecords + 40*s.limits.MaxReceipts + 2*maxDefinitionBytes + 1024
	if len(raw) > canonicalLimit {
		return snapshot{}, ErrLimit
	}
	if json.Unmarshal([]byte(raw), &m) != nil || m.Revision != revision || m.Epoch != epoch {
		return snapshot{}, ErrCorrupt
	}
	if err := s.validate(m); err != nil {
		return snapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return snapshot{}, err
	}
	return m, nil
}
func (s *Store) initialize(ctx context.Context, name string) (snapshot, error) {
	m, err := s.readPoint(ctx, name)
	if !errors.Is(err, errRootAbsent) {
		return m, err
	}
	token, err := vectordata.NewUUIDKey()
	if err != nil {
		return snapshot{}, err
	}
	m = snapshot{Format: 1, Revision: token, Next: 1, Records: map[string]json.RawMessage{}, Receipts: []string{token}}
	payload, err := s.payload(m)
	if err != nil {
		return snapshot{}, err
	}
	_, writeErr := s.request(ctx, "PUT", s.path(name)+"/points?wait=true&ordering=strong", map[string]any{
		"points": []any{map[string]any{"id": 0, "vector": map[string]any{}, "payload": payload}}, "update_mode": "insert_only",
	}, nil)
	observed, readErr := s.readRoot(ctx, name)
	if readErr == nil {
		return observed, nil
	}
	return snapshot{}, unknown(token, errors.Join(writeErr, readErr))
}
func (s *Store) transition(ctx context.Context, name string, base, next snapshot) error {
	if len(base.Receipts) >= s.limits.MaxReceipts {
		return ErrLimit
	}
	token, err := vectordata.NewUUIDKey()
	if err != nil {
		return err
	}
	next.Revision, next.Receipts = token, append(slices.Clone(base.Receipts), token)
	payload, err := s.payload(next)
	if err != nil {
		return err
	}
	var out response[struct {
		Status string `json:"status"`
	}]
	status, writeErr := s.request(ctx, "PUT", s.path(name)+"/points?wait=true&ordering=strong", map[string]any{
		"points": []any{map[string]any{"id": 0, "vector": map[string]any{}, "payload": payload}}, "update_mode": "update_only",
		"update_filter": map[string]any{"must": []any{
			map[string]any{"key": "revision", "match": map[string]any{"value": base.Revision}},
			map[string]any{"key": "epoch", "match": map[string]any{"value": base.Epoch}},
		}},
	}, &out)
	if errors.Is(writeErr, errRequestLimit) {
		return writeErr
	}
	observed, readErr := s.readRoot(ctx, name)
	if readErr == nil && slices.Contains(observed.Receipts, token) {
		return nil
	}
	if writeErr != nil || status != http.StatusOK || out.Result.Status != "completed" || readErr != nil {
		return unknown(token, errors.Join(writeErr, readErr))
	}
	if observed.Revision != base.Revision {
		return errConflict
	}
	return unknown(token, nil)
}
