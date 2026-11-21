package documentdbrecovery

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	_ "modernc.org/sqlite"
)

var errRevisionConflict = errors.New("conflicting payload at same revision")

type item struct {
	ID       string `bson:"_id"`
	Revision int64  `bson:"revision"`
	Payload  string `bson:"payload"`
}

// reconcile only inserts missing records. Existing values, including newer
// revisions, are never replaced. A conflicting same-revision payload is an error.
func reconcile(ctx context.Context, log *sql.DB, target *mongo.Collection) (int, error) {
	rows, err := log.QueryContext(ctx, `SELECT id, revision, payload FROM intents ORDER BY id`)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	restored := 0
	for rows.Next() {
		var want item
		if err := rows.Scan(&want.ID, &want.Revision, &want.Payload); err != nil {
			return restored, err
		}
		var got item
		err := target.FindOne(ctx, bson.M{"_id": want.ID}).Decode(&got)
		if errors.Is(err, mongo.ErrNoDocuments) {
			_, err = target.InsertOne(ctx, want)
			if err == nil {
				restored++
				continue
			}
			// A concurrent insert may have won; inspect its value, not the error alone.
			if !mongo.IsDuplicateKeyError(err) {
				return restored, err
			}
			err = target.FindOne(ctx, bson.M{"_id": want.ID}).Decode(&got)
		}
		if err != nil {
			return restored, err
		}
		if got.Revision < want.Revision {
			return restored, fmt.Errorf("%s: target revision %d is older than intent %d", want.ID, got.Revision, want.Revision)
		}
		if got.Revision == want.Revision && got.Payload != want.Payload {
			return restored, fmt.Errorf("%w: %s at revision %d", errRevisionConflict, want.ID, want.Revision)
		}
	}
	return restored, rows.Err()
}

func TestDocumentDBRecovery(t *testing.T) {
	uri := os.Getenv("DOCUMENTDB_RECOVERY_URI")
	if uri == "" {
		t.Skip("set DOCUMENTDB_RECOVERY_URI to a disposable DocumentDB instance")
	}
	ctx := t.Context()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(context.Background())
	db := client.Database("recovery" + rand.Text())
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if err := db.Drop(c); err != nil {
			t.Error(err)
		}
	}()
	target := db.Collection("items")
	path := filepath.Join(t.TempDir(), "independent-intents.db")
	log, err := sql.Open("sqlite", path+"?_pragma=synchronous(FULL)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.ExecContext(ctx, `CREATE TABLE intents (id TEXT PRIMARY KEY, revision INTEGER NOT NULL, payload TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	seed := []item{{"missing", 1, "lost"}, {"current", 1, "current"}, {"newer", 1, "old"}}
	for _, v := range seed {
		// Commit intent before the remote write and before reporting success.
		if _, err := log.ExecContext(ctx, `INSERT INTO intents VALUES (?, ?, ?)`, v.ID, v.Revision, v.Payload); err != nil {
			t.Fatal(err)
		}
		if _, err := target.InsertOne(ctx, v); err != nil {
			t.Fatal(err)
		}
	}
	// Reopen the independent log to ensure recovery does not depend on process memory.
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	log, err = sql.Open("sqlite", path+"?_pragma=synchronous(FULL)")
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	if _, err := target.DeleteOne(ctx, bson.M{"_id": "missing"}); err != nil {
		t.Fatal(err)
	}
	if _, err := target.ReplaceOne(ctx, bson.M{"_id": "newer"}, item{"newer", 2, "new"}); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for pass := 1; pass <= 2; pass++ {
		n, err := reconcile(ctx, log, target)
		if err != nil {
			t.Fatal(err)
		}
		if want := 2 - pass; n != want {
			t.Fatalf("pass %d restored %d, want %d", pass, n, want)
		}
		for _, want := range []item{{"missing", 1, "lost"}, {"current", 1, "current"}, {"newer", 2, "new"}} {
			var got item
			if err := target.FindOne(ctx, bson.M{"_id": want.ID}).Decode(&got); err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Fatalf("pass %d: got %+v, want %+v", pass, got, want)
			}
		}
		if pass == 1 {
			t.Logf("verified recovery: %s; fixture: %d intents; restored: %d", time.Since(start), len(seed), n)
		}
	}
	// A divergent value at the same revision must not be silently accepted.
	if _, err := target.ReplaceOne(ctx, bson.M{"_id": "current"}, item{"current", 1, "conflict"}); err != nil {
		t.Fatal(err)
	}
	if _, err := reconcile(ctx, log, target); !errors.Is(err, errRevisionConflict) {
		t.Fatalf("expected a same-revision conflict, got %v", err)
	}
}
