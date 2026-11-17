// sqlite-workload measures owned, disposable SQLite fixtures sequentially.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"
)

type config struct {
	Records      []int  `json:"records"`
	Dimensions   []int  `json:"dimensions"`
	Tops         []int  `json:"tops"`
	Samples      int    `json:"warm_samples"`
	Batch        int    `json:"batch_records"`
	Payload      int    `json:"payload_bytes"`
	Distribution string `json:"distribution"`
	Machine      string `json:"machine_label"`
	Parent       string `json:"-"`
	Quiet        bool   `json:"-"`
}

func main() {
	if err := mainRun(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func mainRun() (err error) {
	records := flag.String("records", "1000", "Comma-separated fixture record counts")
	dimensions := flag.String("dimensions", "32", "Comma-separated vector dimensions")
	tops := flag.String("tops", "10,100", "Comma-separated result limits")
	c := config{}
	flag.IntVar(&c.Samples, "samples", 3, "Warm query samples; reopened queries have one sample")
	flag.IntVar(&c.Batch, "batch", 256, "Records per durable insert/upsert transaction")
	flag.IntVar(&c.Payload, "payload-bytes", 4096, "Bytes in each record's Body string")
	flag.StringVar(&c.Distribution, "distribution", "random", "Vector distribution: random or ties")
	flag.StringVar(&c.Machine, "machine", "", "Optional hardware label for the report")
	flag.StringVar(&c.Parent, "workdir", "", "Existing parent directory for owned temporary fixtures")
	flag.BoolVar(&c.Quiet, "quiet", false, "Suppress progress on stderr")
	output := flag.String("output", "", "New JSONL report filename; empty writes stdout")
	timeout := flag.Duration("timeout", 45*time.Minute, "Overall measurement deadline")
	flag.Parse()
	if c.Records, err = parseInts(*records); err != nil {
		return err
	}
	if c.Dimensions, err = parseInts(*dimensions); err != nil {
		return err
	}
	if c.Tops, err = parseInts(*tops); err != nil {
		return err
	}
	if err := c.validate(); err != nil {
		return err
	}
	if *timeout <= 0 {
		return fmt.Errorf("timeout must be positive")
	}
	var out io.Writer = os.Stdout
	if *output != "" {
		file, openErr := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if openErr != nil {
			return openErr
		}
		defer func() { err = errors.Join(err, file.Close()) }()
		out = file
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	return run(ctx, c, out, os.Stderr)
}

func parseInts(text string) ([]int, error) {
	var values []int
	seen := map[int]bool{}
	for _, part := range strings.Split(text, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("expected positive integer list, got %q", text)
		}
		if seen[n] {
			return nil, fmt.Errorf("duplicate value %d", n)
		}
		seen[n] = true
		values = append(values, n)
	}
	return values, nil
}

func (c config) validate() error {
	if len(c.Records) == 0 || len(c.Dimensions) == 0 || len(c.Tops) == 0 || c.Samples < 1 || c.Batch < 1 || c.Payload < 0 {
		return fmt.Errorf("records, dimensions, tops, samples and batch must be positive; payload must be nonnegative")
	}
	for _, list := range [][]int{c.Records, c.Dimensions, c.Tops} {
		for _, n := range list {
			if n <= 0 {
				return fmt.Errorf("fixture sizes and limits must be positive")
			}
		}
	}
	if c.Distribution != "random" && c.Distribution != "ties" {
		return fmt.Errorf("unknown distribution %q", c.Distribution)
	}
	for _, dimensions := range c.Dimensions {
		if _, err := definition(dimensions).Normalize(); err != nil {
			return err
		}
	}
	return nil
}

type environment struct {
	Type         string            `json:"type"`
	Schema       int               `json:"schema"`
	Started      string            `json:"started_utc"`
	Go           string            `json:"go"`
	OS           string            `json:"os"`
	Arch         string            `json:"arch"`
	CPUs         int               `json:"logical_cpus"`
	Procs        int               `json:"gomaxprocs"`
	Revision     string            `json:"vcs_revision"`
	Modified     bool              `json:"vcs_modified"`
	Dependencies map[string]string `json:"dependencies"`
	Config       config            `json:"config"`
	Notes        []string          `json:"notes"`
}

func run(ctx context.Context, c config, out, progress io.Writer) error {
	if err := c.validate(); err != nil {
		return err
	}
	env := environment{Type: "environment", Schema: 1, Started: time.Now().UTC().Format(time.RFC3339Nano), Go: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, CPUs: runtime.NumCPU(), Procs: runtime.GOMAXPROCS(0), Config: c, Dependencies: map[string]string{}, Notes: []string{
		"Sequential fixtures; deterministic input generation and validation are outside API timers.",
		"Canonical uses an unprojected file; packed_go and sqlitevec share a second projected file with identical canonical records.",
		"Warm queries have an untimed warm-up; reopened queries use a fresh provider connection. OS caches are not flushed: neither condition measures cold disk I/O.",
		"GC is requested before timed queries, outside duration/allocation measurements. Go allocations exclude SQLite temporary-storage requirements.",
		"1% and 10% eligibility use data filters; every search path currently scans canonical payloads for these queries.",
		"Writes alternate baseline/projected order per batch. Projected Go and native handles use the same write implementation.",
		"Only a final complete event makes the JSONL run complete; failed/canceled fixtures are not published.",
	}}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "modernc.org/sqlite" || dep.Path == "modernc.org/libc" {
				version := dep.Version
				if dep.Replace != nil {
					version += " (replaced by " + dep.Replace.Path + " " + dep.Replace.Version + ")"
				}
				env.Dependencies[dep.Path] = version
			}
		}
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				env.Revision = setting.Value
			}
			if setting.Key == "vcs.modified" {
				env.Modified = setting.Value == "true"
			}
		}
	}
	encoder := json.NewEncoder(out)
	if err := encoder.Encode(env); err != nil {
		return err
	}
	completed := 0
	for _, records := range c.Records {
		for _, dimensions := range c.Dimensions {
			logf := func(format string, args ...any) {
				if !c.Quiet {
					fmt.Fprintf(progress, format+"\n", args...)
				}
			}
			fixture, err := runFixture(ctx, c, records, dimensions, logf)
			if err != nil {
				return fmt.Errorf("records=%d dimensions=%d: %w", records, dimensions, err)
			}
			if err := encoder.Encode(fixture); err != nil {
				return err
			}
			completed++
		}
	}
	return encoder.Encode(struct {
		Type     string `json:"type"`
		Fixtures int    `json:"fixtures"`
		Finished string `json:"finished_utc"`
	}{"complete", completed, time.Now().UTC().Format(time.RFC3339Nano)})
}
