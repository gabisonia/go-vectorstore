//go:build integration

package redis

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/gabisonia/go-vectorstore/internal/mixedworkload"
	"github.com/gabisonia/go-vectorstore/vectordata"
	goredis "github.com/redis/go-redis/v9"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
)

func TestIntegrationMixedVectorWorkload(t *testing.T) {
	ctx := t.Context()
	store := integrationStore(t, StoreOptions{StorageMode: StorageSnapshot})
	probe, err := New(StoreOptions{Addr: integrationAddr, Namespace: store.metadata.Namespace})
	if err != nil {
		t.Fatal(err)
	}
	defer probe.Close()
	report := mixedworkload.Run(t, os.Getenv("REDIS_MIXED_MEASURE") == "1", store, nil, func(base vectordata.ModelCollection, row vectordata.ModelRecord) error {
		c := base.(*Collection)
		handle, err := probe.GetModelCollection(c.name, c.definition, vectordata.ModelCollectionOptions{})
		if err != nil {
			return err
		}
		// Cancel after Redis serves the registration read, before publication.
		op, cancel := context.WithCancel(ctx)
		defer cancel()
		reached := false
		probe.client.AddHook(processHook{run: func(call context.Context, cmd goredis.Cmder, next goredis.ProcessHook) error {
			err := next(call, cmd)
			if call == op && cmd.Name() == "get" && err == nil {
				reached = true
				cancel()
			}
			return err
		}})
		row.Data = map[string]any{"Eligible": false, "Revision": int64(-1)}
		_, err = handle.Upsert(op, []vectordata.ModelRecord{row})
		if !reached || !errors.Is(err, context.Canceled) {
			return fmt.Errorf("cancel after snapshot read: reached=%t, err=%v", reached, err)
		}
		got, err := handle.Get(ctx, row.Key, vectordata.RetrievalOptions{})
		if err != nil {
			return err
		}
		if got.Data["Revision"] != int64(0) {
			return fmt.Errorf("canceled write changed revision: %v", got.Data)
		}
		row.Data["Revision"] = int64(1)
		if _, err = handle.Upsert(ctx, []vectordata.ModelRecord{row}); err != nil {
			return err
		}
		got, err = handle.Get(ctx, row.Key, vectordata.RetrievalOptions{})
		if err == nil && got.Data["Revision"] != int64(1) {
			return fmt.Errorf("fresh write missing: %v", got.Data)
		}
		return err
	})
	if report == nil {
		return
	}
	info, err := store.client.Info(ctx, "server").Result()
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(info) {
		if version, ok := strings.CutPrefix(line, "redis_version:"); ok {
			report["redis"] = strings.TrimSpace(version)
		}
	}
	if report["redis"] == nil {
		t.Fatal("missing Redis version")
	}
	config := map[string]string{}
	for _, key := range []string{"appendonly", "appendfsync", "save", "maxmemory-policy"} {
		value, err := store.client.ConfigGet(ctx, key).Result()
		if err != nil {
			t.Fatal(err)
		}
		v, ok := value[key]
		if !ok {
			t.Fatalf("missing Redis config %s", key)
		}
		config[key] = v
	}
	report["redis_config"] = config
	report["storage_layout"] = string(store.storageMode)
	report["native_indexes"] = store.nativeIndexes
	if ownedRedisContainer != nil {
		code, reader, err := ownedRedisContainer.Exec(ctx, []string{"cat", "/sys/fs/cgroup/memory.current"}, tcexec.Multiplexed())
		if err != nil || code != 0 {
			t.Fatalf("read container memory: exit %d, err %v", code, err)
		}
		raw, err := io.ReadAll(reader)
		if err != nil {
			t.Fatal(err)
		}
		bytes, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		report["database_cgroup_bytes"] = bytes
	}
	mixedworkload.Log(t, report)
}
