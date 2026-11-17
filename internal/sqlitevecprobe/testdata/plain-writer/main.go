// The compatibility probe runs this binary without importing sqlite/vec.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/gabisonia/go-vectorstore/stores/sqlite"
	"github.com/gabisonia/go-vectorstore/vectordata"
	_ "modernc.org/sqlite"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	// A binary that imports only the base provider must reject native execution
	// before creating provider/projection tables; extension availability is explicit.
	store, err := sqlite.Open(context.Background(), os.Args[1], sqlite.StoreOptions{VectorExecution: sqlite.VectorExecutionSQLiteVec})
	if store != nil {
		store.Close()
		return fmt.Errorf("base provider unexpectedly linked sqlite-vec")
	}
	if !errors.Is(err, vectordata.ErrUnsupported) {
		return fmt.Errorf("missing extension: %v", err)
	}
	db, err := sql.Open("sqlite", os.Args[1])
	if err != nil {
		return err
	}
	defer db.Close()
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name LIKE '__vectorstore_%'`).Scan(&tables); err != nil || tables != 0 {
		return fmt.Errorf("failed native open created provider schema: count=%d error=%v", tables, err)
	}
	var version string
	if err := db.QueryRow(`SELECT vec_version()`).Scan(&version); err == nil || !strings.Contains(err.Error(), "no such function") {
		return fmt.Errorf("expected no extension, got %q: %v", version, err)
	}
	_, err = db.Exec(`INSERT INTO records(embedding) VALUES ('[1,2]')`)
	if os.Args[2] == "failure" {
		if err == nil || (!strings.Contains(err.Error(), "no such function") && !strings.Contains(err.Error(), "no such module")) {
			return fmt.Errorf("expected unavailable extension error, got %v", err)
		}
		return nil
	}
	return err
}
