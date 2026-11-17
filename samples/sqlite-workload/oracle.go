package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"sort"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

type expectedHit struct {
	key      int64
	distance float64
}
type oracle struct {
	hits         []expectedHit
	limit, count int
}

// Retain only the largest requested page. This uses sorted insertion rather
// than the provider's heap/selector and never reads from either database.
func (o *oracle) add(key int64, distance float64) {
	o.count++
	index := sort.Search(len(o.hits), func(i int) bool {
		return o.hits[i].distance > distance || o.hits[i].distance == distance && o.hits[i].key > key
	})
	if index >= o.limit {
		return
	}
	if len(o.hits) < o.limit {
		o.hits = append(o.hits, expectedHit{})
	}
	copy(o.hits[index+1:], o.hits[index:len(o.hits)-1])
	o.hits[index] = expectedHit{key, distance}
}

func fixtureVector(key int64, dimensions, revision int, distribution string) []float32 {
	vector := make([]float32, dimensions)
	if distribution == "ties" && revision == 0 {
		vector[0] = 1
		return vector
	}
	state := uint64(key) ^ uint64(revision)*0xd1b54a32d192ed03 ^ 0x123456789abcdef
	for i := range vector {
		state += 0x9e3779b97f4a7c15
		x := state
		x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
		x = (x ^ (x >> 27)) * 0x94d049bb133111eb
		x ^= x >> 31
		vector[i] = float32(int32(x>>32)) / 2147483648
	}
	return vector
}

func referenceDistance(a, b []float32) float64 {
	var distance float64
	for i := range a {
		distance += math.Abs(float64(a[i]) - float64(b[i]))
	}
	return distance
}

func validatePage(hits []vectordata.ModelSearchResult, want []expectedHit, body string) (string, error) {
	if len(hits) != len(want) {
		return "", fmt.Errorf("page length %d, expected %d", len(hits), len(want))
	}
	digest := sha256.New()
	var token [24]byte
	for i, hit := range hits {
		key, ok := hit.Record.Key.(int64)
		if !ok || key != want[i].key || math.Float64bits(hit.Distance) != math.Float64bits(want[i].distance) || hit.Score != 1/(1+want[i].distance) || hit.ScoreKind != vectordata.ScoreSimilarity {
			return "", fmt.Errorf("rank %d: key=%v distance=%.17g score=%.17g, want key=%d distance=%.17g", i, hit.Record.Key, hit.Distance, hit.Score, want[i].key, want[i].distance)
		}
		if hit.Record.Data["Body"] != body || hit.Record.Data["Group"] != key%100 || hit.Record.Vectors != nil || hit.Record.BinaryVectors != nil || hit.Record.SparseVectors != nil {
			return "", fmt.Errorf("key %d: payload or projection differs", key)
		}
		binary.LittleEndian.PutUint64(token[:8], uint64(key))
		binary.LittleEndian.PutUint64(token[8:16], math.Float64bits(hit.Distance))
		binary.LittleEndian.PutUint64(token[16:], math.Float64bits(hit.Score))
		digest.Write(token[:])
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}
