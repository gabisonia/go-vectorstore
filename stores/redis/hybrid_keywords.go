package redis

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/gabisonia/go-vectorstore/vectordata"
)

// Each group is plain text: terms within it are ANDed, groups are ORed. Only
// lexical tokens reach bound TEXT operands; caller query syntax is never parsed.
// Nil retains the original positional string and its AND semantics.
func hybridKeywords(keywords string, terms []string) (string, []any, error) {
	grouped := terms != nil
	if grouped {
		if strings.TrimSpace(keywords) != "" || len(terms) == 0 || len(terms) > 128 {
			return "", nil, fmt.Errorf("%w: use 1..128 KeywordTerms groups with blank positional keywords", vectordata.ErrInvalidSearchOptions)
		}
	} else {
		terms = []string{keywords}
	}
	var groups []string
	var params []any
	bytes := 0
	for _, term := range terms {
		if strings.TrimSpace(term) == "" || !utf8.ValidString(term) || strings.ContainsRune(term, 0) || len(term) > 16384 {
			return "", nil, fmt.Errorf("%w: keywords must be nonblank UTF-8 without NUL, at most 16384 bytes", vectordata.ErrInvalidSearchOptions)
		}
		bytes += len(term)
		if bytes > 16384 {
			return "", nil, fmt.Errorf("%w: keywords exceed 16384 aggregate bytes", vectordata.ErrInvalidSearchOptions)
		}
		words := strings.FieldsFunc(term, func(r rune) bool {
			return r != '_' && !unicode.IsLetter(r) && !unicode.IsNumber(r) && !unicode.IsMark(r)
		})
		if len(params)/2+len(words) > 128 {
			return "", nil, fmt.Errorf("%w: at most 128 aggregate keyword tokens", vectordata.ErrInvalidSearchOptions)
		}
		if len(words) == 0 {
			continue
		}
		bound := make([]string, 0, len(words))
		for _, word := range words {
			name := "__kw" + strconv.Itoa(len(params)/2)
			bound = append(bound, "$"+name)
			params = append(params, name, word)
		}
		query := strings.Join(bound, " ")
		if grouped {
			query = "(" + query + ")"
		}
		groups = append(groups, query)
	}
	if len(groups) == 0 {
		return nativeNone, nil, nil
	}
	return strings.Join(groups, " | "), params, nil
}
