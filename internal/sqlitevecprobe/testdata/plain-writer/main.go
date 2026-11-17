// The compatibility probe runs this binary without importing sqlite/vec.
package main

import (
	"database/sql"
	"fmt"
	"os"
	"strings"

	_ "modernc.org/sqlite"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	db, err := sql.Open("sqlite", os.Args[1])
	if err != nil {
		return err
	}
	defer db.Close()
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
