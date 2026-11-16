package postgres

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// Parse each group as plain text before combining server-produced tsquery text.
// Empty lexical groups (e.g. punctuation) contribute no OR operand. Only the
// normalized tsquery output is parsed again; caller terms remain bound data.
const hybridKeywordTermsSQL = `(SELECT COALESCE(string_agg(NULLIF(plainto_tsquery('simple'::regconfig,__vs_term)::text,''),' | '),'')::tsquery FROM unnest($2::text[]) AS __vs_terms(__vs_term))`

func hybridKeywords(keywords string, terms []string) (string, any, error) {
	if terms == nil {
		if strings.TrimSpace(keywords) == "" {
			return "", nil, fmt.Errorf("%w: keywords required", vectordata.ErrInvalidSearchOptions)
		}
		return "plainto_tsquery('simple'::regconfig,$2)", keywords, nil
	}
	if strings.TrimSpace(keywords) != "" || len(terms) == 0 {
		return "", nil, fmt.Errorf("%w: use a nonempty KeywordTerms list with blank positional keywords", vectordata.ErrInvalidSearchOptions)
	}
	for _, term := range terms {
		if strings.TrimSpace(term) == "" || !utf8.ValidString(term) || strings.ContainsRune(term, 0) {
			return "", nil, fmt.Errorf("%w: keyword terms must be nonblank UTF-8 without NUL", vectordata.ErrInvalidSearchOptions)
		}
	}
	return hybridKeywordTermsSQL, slices.Clone(terms), nil
}

func snapshotHybridOptions(opts vectordata.HybridSearchOptions) vectordata.HybridSearchOptions {
	opts.KeywordTerms = slices.Clone(opts.KeywordTerms)
	return opts
}
