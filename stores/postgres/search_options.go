package postgres

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gabisonia/go-vectorstore/vectordata"
	"github.com/jackc/pgx/v5"
)

type searchSetting struct{ name, value string }

func searchSettings(opts vectordata.SearchOptions) ([]searchSetting, bool, error) {
	var settings []searchSetting
	iterative := false
	add := func(name string, value, max int) error {
		if value < 0 || value > max {
			return fmt.Errorf("%w: %s must be between 0 and %d", vectordata.ErrInvalidSearchOptions, name, max)
		}
		if value != 0 {
			settings = append(settings, searchSetting{name, strconv.Itoa(value)})
		}
		return nil
	}
	if h := opts.HNSW; h != nil {
		iterative = h.MaxScanTuples != 0
		if err := add("hnsw.ef_search", h.EfSearch, 1000); err != nil {
			return nil, false, err
		}
		if err := add("hnsw.max_scan_tuples", h.MaxScanTuples, 2147483647); err != nil {
			return nil, false, err
		}
		if h.IterativeScan {
			settings = append(settings, searchSetting{"hnsw.iterative_scan", "strict_order"})
			iterative = true
		}
	}
	if ivf := opts.IVFFlat; ivf != nil {
		iterative = iterative || ivf.MaxProbes != 0
		if err := add("ivfflat.probes", ivf.Probes, 32768); err != nil {
			return nil, false, err
		}
		if err := add("ivfflat.max_probes", ivf.MaxProbes, 32768); err != nil {
			return nil, false, err
		}
		if ivf.MaxProbes != 0 && ivf.Probes > ivf.MaxProbes {
			return nil, false, fmt.Errorf("%w: IVFFlat max probes must be >= probes", vectordata.ErrInvalidSearchOptions)
		}
		if ivf.IterativeScan {
			settings = append(settings, searchSetting{"ivfflat.iterative_scan", "relaxed_order"})
			iterative = true
		}
	}
	return settings, iterative, nil
}

func supportsIterativeScans(version string) bool {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	minor, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	return major > 0 || major == 0 && minor >= 8
}

func rollbackTx(ctx context.Context, tx pgx.Tx) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(cleanupCtx)
}
