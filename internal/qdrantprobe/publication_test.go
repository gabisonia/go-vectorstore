//go:build qdrantprobe

package qdrantprobe

// This is an executable protocol experiment, not a provider or supported
// storage format. Only cooperating clients on the validated topology may write.

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"

	"github.com/gabisonia/go-vectorstore/internal/modelcodec"
	"github.com/gabisonia/go-vectorstore/vectordata"
)

const (
	proofSlots           = 16
	proofRecords         = 32
	proofRecordBytes     = 8 << 10
	proofGenerationBytes = 64 << 10
	proofDefinitionBytes = 8 << 10
	proofReceipts        = 128
	proofReaderPins      = 16
)

var (
	errProofConflict = errors.New("publication conflict")
	errProofUnknown  = errors.New("publication outcome unknown")
	errProofLimit    = errors.New("publication proof resource limit")
	errProofStale    = errors.New("collection incarnation is stale or absent")
	errProofCorrupt  = errors.New("invalid publication proof state")
)

type generation struct {
	Index                  int
	Token, Epoch, BaseHead string
	Count, Bytes           int
	Digest                 string
	Next                   int64
}

type readerPin struct {
	Token, Generation string
}

type manifest struct {
	Format                            int
	Revision, Epoch, Definition, Head string
	Active                            bool
	Next                              int64
	Slots                             []generation
	// Stable generation tokens permit read-only reconciliation by a new client.
	Published []string
	// Experimental pins are bounded and retained across logical epochs.
	Pins []readerPin
	// Every confirmed transition retains its unique receipt for the lifetime
	// of this root. Stop at the bound instead of evicting evidence of success.
	Receipts []string
}

type publicationProof struct {
	path string
	http *http.Client
}

// Propagate randomness errors; clocks/counters can repeat across processes.
func newProofToken() (string, error) { return vectordata.NewUUIDKey() }

func proofPayload(m manifest) (map[string]any, error) {
	if err := validateManifest(m); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return map[string]any{"revision": m.Revision, "epoch": m.Epoch, "canonical": string(raw)}, nil
}

func validateManifest(m manifest) error {
	if m.Format != 1 || m.Revision == "" || len(m.Slots) > proofSlots || len(m.Published) > proofSlots || len(m.Pins) > proofReaderPins || len(m.Receipts) > proofReceipts || len(m.Definition) > proofDefinitionBytes || m.Next < 1 {
		return errProofCorrupt
	}
	if m.Active && (m.Epoch == "" || m.Definition == "") {
		return errProofCorrupt
	}
	seen := map[string]bool{}
	for _, token := range m.Receipts {
		if token == "" || seen[token] {
			return errProofCorrupt
		}
		seen[token] = true
	}
	if len(m.Receipts) > 0 && !seen[m.Revision] {
		return errProofCorrupt
	}
	head := m.Head == ""
	for i, slot := range m.Slots {
		if slot.Index != i || seen["slot:"+slot.Token] || validateGeneration(slot) != nil {
			return errProofCorrupt
		}
		seen["slot:"+slot.Token] = true
		if slot.Token == m.Head {
			head = slot.Epoch == m.Epoch && slot.Next == m.Next && slices.Contains(m.Published, slot.Token)
		}
	}
	for _, token := range m.Published {
		if !seen["slot:"+token] || seen["published:"+token] {
			return errProofCorrupt
		}
		seen["published:"+token] = true
	}
	for _, pin := range m.Pins {
		if pin.Token == "" || seen["pin:"+pin.Token] || !seen["published:"+pin.Generation] {
			return errProofCorrupt
		}
		seen["pin:"+pin.Token] = true
	}
	if !head {
		return errProofCorrupt
	}
	return nil
}

func validateGeneration(g generation) error {
	if g.Index < 0 || g.Index >= proofSlots || g.Token == "" || g.Epoch == "" || g.Count < 0 || g.Count > proofRecords || g.Bytes < 0 || g.Bytes > proofGenerationBytes || g.Next < 1 || len(g.Digest) != 64 {
		return errProofCorrupt
	}
	if _, err := hex.DecodeString(g.Digest); err != nil {
		return errProofCorrupt
	}
	return nil
}

func cloneManifest(m manifest) manifest {
	m.Slots = slices.Clone(m.Slots)
	m.Published = slices.Clone(m.Published)
	m.Pins = slices.Clone(m.Pins)
	m.Receipts = slices.Clone(m.Receipts)
	return m
}

func (p publicationProof) read(ctx context.Context) (manifest, error) {
	var out response[[]point]
	status, err := request(ctx, p.http, "POST", p.path+"/points?consistency=all", map[string]any{"ids": []int{0}, "with_payload": true, "with_vector": false}, &out)
	if err != nil {
		return manifest{}, err
	}
	if status != 200 || len(out.Result) != 1 || string(out.Result[0].ID) != "0" {
		return manifest{}, errProofCorrupt
	}
	var canonical, revision, epoch string
	for key, target := range map[string]*string{"canonical": &canonical, "revision": &revision, "epoch": &epoch} {
		if err := json.Unmarshal(out.Result[0].Payload[key], target); err != nil {
			return manifest{}, errProofCorrupt
		}
	}
	// Bound before decoding an untrusted canonical manifest. The server helper
	// also bounds the entire HTTP response independently.
	if len(canonical) > 128<<10 {
		return manifest{}, errProofLimit
	}
	var m manifest
	if err := json.Unmarshal([]byte(canonical), &m); err != nil {
		return m, errProofCorrupt
	}
	if m.Revision != revision || m.Epoch != epoch {
		return m, errProofCorrupt
	}
	return m, validateManifest(m)
}

func (p publicationProof) checkTopology(ctx context.Context) error {
	var out response[struct {
		Config struct {
			Params struct {
				Shards   int            `json:"shard_number"`
				Replicas int            `json:"replication_factor"`
				Sharding string         `json:"sharding_method"`
				Vectors  map[string]any `json:"vectors"`
			} `json:"params"`
			Metadata map[string]json.RawMessage `json:"metadata"`
		} `json:"config"`
	}]
	status, err := request(ctx, p.http, "GET", p.path, nil, &out)
	if err != nil {
		return err
	}
	c := out.Result.Config
	if status != 200 || c.Params.Shards != 1 || c.Params.Replicas != 1 || c.Params.Sharding != "auto" || len(c.Params.Vectors) != 0 || string(c.Metadata["publication_proof"]) != "1" {
		return fmt.Errorf("%w: require dedicated single-shard, single-replica auto collection (shards=%d replicas=%d sharding=%q)", vectordata.ErrUnsupported, c.Params.Shards, c.Params.Replicas, c.Params.Sharding)
	}
	return nil
}

// initialize never overwrites point 0, including a tombstoned root. All clients
// adopt the winning root after readback. Physical collection removal is outside
// this experiment's contract; logical deletion preserves epochs and receipts.
func (p publicationProof) initialize(ctx context.Context) (manifest, error) {
	if err := p.checkTopology(ctx); err != nil {
		return manifest{}, err
	}
	token, err := newProofToken()
	if err != nil {
		return manifest{}, err
	}
	m := manifest{Format: 1, Revision: token, Next: 1}
	payload, err := proofPayload(m)
	if err != nil {
		return m, err
	}
	var out response[struct {
		Status string `json:"status"`
	}]
	status, writeErr := request(ctx, p.http, "PUT", p.path+"/points?wait=true&ordering=strong", map[string]any{"points": []any{map[string]any{"id": 0, "vector": map[string]any{}, "payload": payload}}, "update_mode": "insert_only"}, &out)
	if writeErr != nil || status != 200 || out.Result.Status != "completed" {
		return m, errors.Join(errProofUnknown, writeErr)
	}
	return p.read(ctx)
}

// transition makes exactly one write attempt. Receipts survive subsequent
// transitions, so a confirmed earlier publication is not mistaken for a
// conflict just because a later writer already advanced the head.
func (p publicationProof) transition(ctx context.Context, base, next manifest) error {
	if len(base.Receipts) >= proofReceipts {
		return errProofLimit
	}
	if len(next.Slots) < len(base.Slots) || !slices.Equal(base.Slots, next.Slots[:len(base.Slots)]) {
		return errProofCorrupt
	}
	if len(next.Published) < len(base.Published) || !slices.Equal(base.Published, next.Published[:len(base.Published)]) {
		return errProofCorrupt
	}
	token, err := newProofToken()
	if err != nil {
		return err
	}
	next.Revision = token
	next.Receipts = append(slices.Clone(base.Receipts), token)
	payload, err := proofPayload(next)
	if err != nil {
		return err
	}
	var out response[struct {
		Status string `json:"status"`
	}]
	status, writeErr := request(ctx, p.http, "PUT", p.path+"/points?wait=true&ordering=strong", map[string]any{
		"points":      []any{map[string]any{"id": 0, "vector": map[string]any{}, "payload": payload}},
		"update_mode": "update_only",
		"update_filter": map[string]any{"must": []any{
			map[string]any{"key": "revision", "match": map[string]any{"value": base.Revision}},
			map[string]any{"key": "epoch", "match": map[string]any{"value": base.Epoch}},
		}},
	}, &out)
	observed, readErr := p.read(ctx)
	if readErr == nil && slices.Contains(observed.Receipts, token) {
		return nil
	}
	// A read without our receipt cannot settle a request that may still be
	// in flight. Preserve the transport/context cause and leave data retained.
	if writeErr != nil || status != 200 || out.Result.Status != "completed" || readErr != nil {
		return errors.Join(errProofUnknown, writeErr, readErr)
	}
	if observed.Revision != base.Revision {
		return errProofConflict
	}
	return errProofUnknown
}

func (p publicationProof) create(ctx context.Context, definition string) (manifest, error) {
	if len(definition) == 0 || len(definition) > proofDefinitionBytes {
		return manifest{}, errProofLimit
	}
	base, err := p.read(ctx)
	if err != nil {
		return base, err
	}
	if base.Active {
		if base.Definition != definition {
			return base, vectordata.ErrSchemaMismatch
		}
		return base, nil
	}
	next := cloneManifest(base)
	next.Epoch, err = newProofToken()
	if err != nil {
		return base, err
	}
	next.Active, next.Definition, next.Head, next.Next = true, definition, "", 1
	if err := p.transition(ctx, base, next); err != nil {
		return base, err
	}
	observed, err := p.read(ctx)
	if err == nil && (!observed.Active || observed.Epoch != next.Epoch || observed.Definition != definition) {
		return observed, errProofStale
	}
	return observed, err
}

func (p publicationProof) remove(ctx context.Context, epoch string) error {
	base, err := p.read(ctx)
	if err != nil {
		return err
	}
	if !base.Active || base.Epoch != epoch {
		return errProofStale
	}
	next := cloneManifest(base)
	next.Active = false
	return p.transition(ctx, base, next)
}

func generationDigest(records []string) (string, int) {
	h := sha256.New()
	total := 0
	for _, record := range records {
		var length [8]byte
		binary.LittleEndian.PutUint64(length[:], uint64(len(record)))
		h.Write(length[:])
		h.Write([]byte(record))
		total += len(record)
	}
	return hex.EncodeToString(h.Sum(nil)), total
}

// reserve consumes one lifetime slot before data writes, preventing crashed or
// uncertain staging from accumulating unbounded orphan generations. No slot is
// ever reused or reclaimed online, even after logical delete/recreate.
func (p publicationProof) reserve(ctx context.Context, base manifest, records []string, nextKey int64) (generation, error) {
	if !base.Active {
		return generation{}, errProofStale
	}
	if len(records) > proofRecords || len(base.Slots) >= proofSlots || nextKey < base.Next {
		return generation{}, errProofLimit
	}
	for _, raw := range records {
		if len(raw) > proofRecordBytes {
			return generation{}, errProofLimit
		}
	}
	digest, size := generationDigest(records)
	if size > proofGenerationBytes {
		return generation{}, errProofLimit
	}
	// Reconstruct and validate the whole explicit canonical set before any
	// remote reservation. Persisted definitions carry no embedding callbacks.
	var definition vectordata.CollectionDefinition
	if err := json.Unmarshal([]byte(base.Definition), &definition); err != nil {
		return generation{}, errProofCorrupt
	}
	definition, err := definition.Normalize()
	if err != nil {
		return generation{}, err
	}
	decoded := make([]vectordata.ModelRecord, len(records))
	for i, raw := range records {
		decoded[i], err = modelcodec.Decode([]byte(raw), definition)
		if err != nil {
			return generation{}, err
		}
	}
	validated, err := canonicalProofRecords(ctx, definition, decoded)
	if err != nil {
		return generation{}, err
	}
	if !slices.Equal(records, validated) {
		return generation{}, errProofCorrupt
	}
	owner, err := newProofToken()
	if err != nil {
		return generation{}, err
	}
	g := generation{Index: len(base.Slots), Token: owner, Epoch: base.Epoch, BaseHead: base.Head, Count: len(records), Bytes: size, Digest: digest, Next: nextKey}
	next := cloneManifest(base)
	next.Slots = append(next.Slots, g)
	return g, p.transition(ctx, base, next)
}

func (g generation) id(ordinal int) int { return 1 + g.Index*proofRecords + ordinal }

func (p publicationProof) stage(ctx context.Context, g generation, records []string) error {
	root, err := p.read(ctx)
	if err != nil {
		return err
	}
	if validateGeneration(g) != nil || g.Index >= len(root.Slots) || root.Slots[g.Index] != g {
		return errProofCorrupt
	}
	if slices.Contains(root.Published, g.Token) {
		return errProofConflict
	}
	digest, size := generationDigest(records)
	if len(records) != g.Count || digest != g.Digest || size != g.Bytes {
		return errProofCorrupt
	}
	// Proof deliberately uses small batches to exercise partially completed
	// staging. Only its owner writes these disjoint, never-reused IDs.
	for start := 0; start < len(records); start += 2 {
		points := []any{}
		for i := start; i < min(start+2, len(records)); i++ {
			points = append(points, map[string]any{"id": g.id(i), "vector": map[string]any{}, "payload": map[string]any{"owner": g.Token, "epoch": g.Epoch, "canonical": records[i]}})
		}
		var out response[struct {
			Status string `json:"status"`
		}]
		status, err := request(ctx, p.http, "PUT", p.path+"/points?wait=true&ordering=strong", map[string]any{"points": points, "update_mode": "insert_only"}, &out)
		if err != nil || status != 200 || out.Result.Status != "completed" {
			return errors.Join(errProofUnknown, err)
		}
	}
	// insert_only silently skips an existing ID. Verify complete canonical
	// contents/ownership before permitting any manifest to reference this slot.
	_, err = p.readGeneration(ctx, g)
	return err
}

func (p publicationProof) readGeneration(ctx context.Context, g generation) ([]string, error) {
	if err := validateGeneration(g); err != nil {
		return nil, err
	}
	records := make([]string, 0, g.Count)
	for start := 0; start < g.Count; start += 2 {
		ids := []int{}
		for i := start; i < min(start+2, g.Count); i++ {
			ids = append(ids, g.id(i))
		}
		var out response[[]point]
		status, err := request(ctx, p.http, "POST", p.path+"/points?consistency=all", map[string]any{"ids": ids, "with_payload": true, "with_vector": false}, &out)
		if err != nil {
			return nil, err
		}
		if status != 200 || len(out.Result) != len(ids) {
			return nil, errProofCorrupt
		}
		byID := map[int]point{}
		for _, point := range out.Result {
			var id int
			if json.Unmarshal(point.ID, &id) != nil {
				return nil, errProofCorrupt
			}
			if _, exists := byID[id]; exists || !slices.Contains(ids, id) {
				return nil, errProofCorrupt
			}
			byID[id] = point
		}
		for _, id := range ids {
			point, exists := byID[id]
			if !exists {
				return nil, errProofCorrupt
			}
			var owner, epoch, raw string
			if json.Unmarshal(point.Payload["owner"], &owner) != nil || json.Unmarshal(point.Payload["epoch"], &epoch) != nil || json.Unmarshal(point.Payload["canonical"], &raw) != nil || owner != g.Token || epoch != g.Epoch || len(raw) > proofRecordBytes {
				return nil, errProofCorrupt
			}
			records = append(records, raw)
		}
	}
	digest, size := generationDigest(records)
	if digest != g.Digest || size != g.Bytes {
		return nil, errProofCorrupt
	}
	return records, nil
}

func (p publicationProof) publish(ctx context.Context, g generation) error {
	base, err := p.read(ctx)
	if err != nil {
		return err
	}
	if g.Index < 0 || g.Index >= len(base.Slots) || base.Slots[g.Index] != g {
		return errProofCorrupt
	}
	if slices.Contains(base.Published, g.Token) {
		return nil
	}
	if !base.Active || base.Epoch != g.Epoch {
		return errProofStale
	}
	if base.Head != g.BaseHead {
		return errProofConflict
	}
	if _, err := p.readGeneration(ctx, g); err != nil {
		return err
	}
	next := cloneManifest(base)
	next.Head, next.Next = g.Token, g.Next
	next.Published = append(next.Published, g.Token)
	return p.transition(ctx, base, next)
}

// An absent receipt cannot settle a write that may still be in flight. This
// operation performs no writes, including when used by another process/client.
func (p publicationProof) confirm(ctx context.Context, g generation) error {
	m, err := p.read(ctx)
	if err != nil {
		return errors.Join(errProofUnknown, err)
	}
	if g.Index < 0 || g.Index >= len(m.Slots) || m.Slots[g.Index] != g {
		return errProofUnknown
	}
	if slices.Contains(m.Published, g.Token) {
		return nil
	}
	return errProofUnknown
}

// pinHead registers the observed head using the same revision fence as writers.
// On an unknown outcome the token is retained by the caller, but the reader
// must not access the generation until positive registration is established.
func (p publicationProof) pinHead(ctx context.Context, base manifest) (readerPin, error) {
	if !base.Active || base.Head == "" {
		return readerPin{}, errProofStale
	}
	if len(base.Pins) >= proofReaderPins {
		return readerPin{}, errProofLimit
	}
	token, err := newProofToken()
	if err != nil {
		return readerPin{}, err
	}
	pin := readerPin{Token: token, Generation: base.Head}
	next := cloneManifest(base)
	next.Pins = append(next.Pins, pin)
	return pin, p.transition(ctx, base, next)
}

// releasePin is called only after the reader has permanently stopped using the
// generation. An uncertain release cannot justify another read by that reader.
func (p publicationProof) releasePin(ctx context.Context, pin readerPin) error {
	base, err := p.read(ctx)
	if err != nil {
		return err
	}
	index := slices.Index(base.Pins, pin)
	if index < 0 {
		return errProofStale
	}
	next := cloneManifest(base)
	next.Pins = slices.Delete(next.Pins, index, index+1)
	return p.transition(ctx, base, next)
}

func (p publicationProof) snapshot(ctx context.Context, epoch string) (manifest, []string, error) {
	m, err := p.read(ctx)
	if err != nil {
		return m, nil, err
	}
	if !m.Active || m.Epoch != epoch {
		return m, nil, errProofStale
	}
	if m.Head == "" {
		return m, []string{}, nil
	}
	for _, g := range m.Slots {
		if g.Token == m.Head {
			pin, err := p.pinHead(ctx, m)
			if err != nil {
				return m, nil, err
			}
			records, readErr := p.readGeneration(ctx, g)
			if err := errors.Join(readErr, p.releasePin(ctx, pin)); err != nil {
				return m, nil, err
			}
			return m, records, nil
		}
	}
	return m, nil, errProofCorrupt
}

// reclaim deletes only the payload of an already superseded publication.
// Published tokens can never become head again. A new pin requires a CAS on
// the current head, so the absence of pins here remains true during deletion.
func (p publicationProof) reclaim(ctx context.Context, g generation) error {
	m, err := p.read(ctx)
	if err != nil {
		return err
	}
	if g.Index < 0 || g.Index >= len(m.Slots) || m.Slots[g.Index] != g {
		return errProofCorrupt
	}
	if g.Token == m.Head || !slices.Contains(m.Published, g.Token) {
		return errProofConflict
	}
	for _, pin := range m.Pins {
		if pin.Generation == g.Token {
			return errProofConflict
		}
	}
	if g.Count == 0 {
		return nil
	}
	ids := make([]int, g.Count)
	for i := range ids {
		ids[i] = g.id(i)
	}
	var out responseDelete
	status, err := request(ctx, p.http, "POST", p.path+"/points/delete?wait=true&ordering=strong", map[string]any{"points": ids}, &out)
	if err != nil || status != 200 || out.Result.Status != "completed" {
		return errors.Join(errProofUnknown, err)
	}
	return nil
}

// Untargeted pruning stays disabled. It must not infer ownership
// or quiescence from the currently visible head: old readers and pending/unknown
// publications may still need every reserved slot. Exhaustion fails closed.
func (publicationProof) prune(context.Context) error { return vectordata.ErrUnsupported }

// Canonical preparation belongs before reserve/stage and never re-runs caller
// embedders on a metadata conflict. This proof takes already explicit records.
func canonicalProofRecords(ctx context.Context, d vectordata.CollectionDefinition, records []vectordata.ModelRecord) ([]string, error) {
	prepared, err := vectordata.PrepareModelRecords(ctx, d, vectordata.ModelCollectionOptions{}, records)
	if err != nil {
		return nil, err
	}
	out := make([]string, len(prepared))
	seen := map[any]bool{}
	for i, record := range prepared {
		if record.Key == nil || seen[record.Key] {
			return nil, vectordata.ErrInvalidRecord
		}
		seen[record.Key] = true
		raw, err := modelcodec.Encode(record)
		if err != nil {
			return nil, err
		}
		out[i] = string(raw)
	}
	return out, nil
}
