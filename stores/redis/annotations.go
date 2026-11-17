package redis

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

const TextNoStemAnnotation = "redis.text.nostem"

// TextIndexOptions configures native Redis TEXT indexing. NoStem disables index
// stemming; tokenization, case folding and default stopwords still apply.
type TextIndexOptions struct {
	NoStem bool
}

// WithTextIndexOptions returns a property copy with Redis-owned annotations.
// Default options remove the hint, retaining old registrations byte-for-byte.
func WithTextIndexOptions(p vectordata.DataProperty, options TextIndexOptions) (vectordata.DataProperty, error) {
	if !nativeText(p) {
		return p, fmt.Errorf("%w: Redis TEXT options require a full-text string property", vectordata.ErrSchemaMismatch)
	}
	var err error
	if options.NoStem {
		p.ProviderAnnotations, err = vectordata.WithProviderAnnotation(p.ProviderAnnotations, TextNoStemAnnotation, true)
	} else {
		p.ProviderAnnotations, err = vectordata.WithoutProviderAnnotation(p.ProviderAnnotations, TextNoStemAnnotation)
	}
	return p, err
}

// GetTextIndexOptions reads the typed hint. Other providers may preserve it
// without applying it; Redis uses it only when NativeIndexes builds TEXT fields.
func GetTextIndexOptions(p vectordata.DataProperty) (TextIndexOptions, error) {
	value, found, err := vectordata.GetProviderAnnotation[*bool](p.ProviderAnnotations, TextNoStemAnnotation)
	if err != nil {
		return TextIndexOptions{}, err
	}
	if found && (value == nil || !nativeText(p)) {
		return TextIndexOptions{}, fmt.Errorf("%w: Redis NOSTEM requires a boolean on a full-text string property", vectordata.ErrSchemaMismatch)
	}
	return TextIndexOptions{NoStem: value != nil && *value}, nil
}

func validateRedisAnnotations(d vectordata.CollectionDefinition) error {
	check := func(a vectordata.ProviderAnnotations, text bool) error {
		if a == "" {
			return nil
		}
		raw, err := json.Marshal(a)
		if err != nil {
			return err
		}
		var entries map[string]json.RawMessage
		if err := json.Unmarshal(raw, &entries); err != nil {
			return err
		}
		for name := range entries {
			if !strings.HasPrefix(name, "redis.") {
				continue
			}
			if name != TextNoStemAnnotation {
				return fmt.Errorf("%w: unknown Redis annotation %q", vectordata.ErrUnsupported, name)
			}
			if !text {
				return fmt.Errorf("%w: misplaced Redis TEXT annotation", vectordata.ErrSchemaMismatch)
			}
		}
		return nil
	}
	if err := check(d.Key.ProviderAnnotations, false); err != nil {
		return err
	}
	for _, p := range d.Data {
		if err := check(p.ProviderAnnotations, nativeText(p)); err != nil {
			return err
		}
		if _, err := GetTextIndexOptions(p); err != nil {
			return err
		}
	}
	for _, p := range d.Vectors {
		if err := check(p.ProviderAnnotations, false); err != nil {
			return err
		}
	}
	return nil
}
