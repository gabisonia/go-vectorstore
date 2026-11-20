//go:build referenceparity

package redisparity

import (
	"encoding/binary"
	"encoding/json"
	"os/exec"
	"slices"
	"testing"

	"github.com/redis/go-redis/v9"
)

func TestNativeLayouts(t *testing.T) {
	ctx := t.Context()
	client := redis.NewClient(&redis.Options{Addr: "localhost:6379", Protocol: 2, MaxRetries: -1})
	defer client.Close()
	for _, layout := range []string{"JSON", "HASH"} {
		for _, creator := range []string{"go", "csharp"} {
			t.Run(layout+"_"+creator, func(t *testing.T) {
				name := "reference_" + layout + "_" + creator
				ref := func(phase string) {
					t.Helper()
					out, err := exec.CommandContext(ctx, "dotnet", "Reference.dll", phase, name, layout).CombinedOutput()
					if err != nil {
						t.Fatalf("C# %s: %s %v", phase, out, err)
					}
				}
				if creator == "go" {
					field := "Direction"
					if layout == "JSON" {
						field = "$.Direction"
					}
					err := client.Do(ctx, "FT.CREATE", name, "ON", layout, "PREFIX", 1, name+":", "SCHEMA", field, "AS", "Direction", "VECTOR", "FLAT", 6, "TYPE", "FLOAT32", "DIM", 2, "DISTANCE_METRIC", "COSINE").Err()
					if err != nil {
						t.Fatal(err)
					}
				}
				ref("seed")
				type document struct {
					Body      string
					Direction []float32
				}
				var seed document
				point := document{"from Go", []float32{1, 0}}
				packed, err := binary.Append(nil, binary.LittleEndian, point.Direction)
				if err != nil {
					t.Fatal(err)
				}
				if layout == "HASH" {
					values, err := client.HGetAll(ctx, name+":seed").Result()
					if err != nil {
						t.Fatal(err)
					}
					seed.Body = values["Body"]
					seed.Direction = make([]float32, 2)
					if _, err = binary.Decode([]byte(values["Direction"]), binary.LittleEndian, seed.Direction); err != nil {
						t.Fatal(err)
					}
					if err = client.HSet(ctx, name+":go", "Body", point.Body, "Direction", packed).Err(); err != nil {
						t.Fatal(err)
					}
				} else {
					raw, err := client.Do(ctx, "JSON.GET", name+":seed").Text()
					if err != nil {
						t.Fatal(err)
					}
					if err = json.Unmarshal([]byte(raw), &seed); err != nil {
						t.Fatal(err)
					}
					encoded, err := json.Marshal(point)
					if err != nil {
						t.Fatal(err)
					}
					if err = client.Do(ctx, "JSON.SET", name+":go", "$", encoded).Err(); err != nil {
						t.Fatal(err)
					}
				}
				if seed.Body != "reference" || !slices.Equal(seed.Direction, []float32{0, 1}) {
					t.Fatalf("reference record: %+v", seed)
				}
				hits, err := client.Do(ctx, "FT.SEARCH", name, "*=>[KNN 2 @Direction $v AS distance]", "PARAMS", 2, "v", packed, "SORTBY", "distance", "NOCONTENT", "DIALECT", 2).Slice()
				if err != nil || len(hits) != 3 || hits[1] != name+":go" || hits[2] != name+":seed" {
					t.Fatalf("Go search: %+v %v", hits, err)
				}
				ref("verify")
				exists, err := client.Exists(ctx, name+":go").Result()
				if err != nil || exists != 0 {
					t.Fatalf("read after C# deletion: %d %v", exists, err)
				}
			})
		}
	}
}
