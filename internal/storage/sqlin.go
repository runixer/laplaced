package storage

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// ExpandIn rewrites a SQL query by expanding a single `?` placeholder whose
// matching argument is a slice into `?,?,?,...` (one `?` per slice element),
// and returns the rewritten query plus a flat argument list.
//
// Rules:
//   - Scalar args are passed through unchanged.
//   - Exactly one slice argument is supported. Zero slices is fine (the query
//     is returned unchanged with a copied args slice). Two or more slices
//     return an error.
//   - An empty or nil slice returns an error — `IN ()` is not valid SQL, and
//     callers should guard the no-IDs case themselves (the early-return is
//     usually what they want anyway).
//   - Placeholder counting is naive: `?` inside SQL string literals is not
//     skipped. SQLite does not treat `?` inside `'...'` specially, but if a
//     query contains a literal `?` inside a string, behaviour is undefined.
//
// Usage:
//
//	q, args, err := ExpandIn(
//	    "SELECT ... FROM t WHERE user_id = ? AND id IN (?)",
//	    userID, ids,
//	)
//	if err != nil {
//	    return nil, err
//	}
//	rows, err := db.Query(q, args...)
func ExpandIn(query string, args ...any) (string, []any, error) {
	sliceIdx := -1
	var sliceVal reflect.Value

	for i, a := range args {
		v := reflect.ValueOf(a)
		if !v.IsValid() || v.Kind() != reflect.Slice {
			continue
		}
		if sliceIdx >= 0 {
			return "", nil, errors.New("ExpandIn: multiple slice args not supported")
		}
		sliceIdx = i
		sliceVal = v
	}

	if sliceIdx < 0 {
		out := make([]any, len(args))
		copy(out, args)
		return query, out, nil
	}

	sliceLen := sliceVal.Len()
	if sliceLen == 0 {
		return "", nil, errors.New("ExpandIn: slice arg is empty")
	}

	out := make([]any, 0, len(args)-1+sliceLen)
	for i, a := range args {
		if i == sliceIdx {
			for j := 0; j < sliceLen; j++ {
				out = append(out, sliceVal.Index(j).Interface())
			}
			continue
		}
		out = append(out, a)
	}

	expanded := "?" + strings.Repeat(",?", sliceLen-1)

	var sb strings.Builder
	sb.Grow(len(query) + len(expanded) - 1)
	count := 0
	replaced := false
	for i := 0; i < len(query); i++ {
		c := query[i]
		if c == '?' {
			if count == sliceIdx {
				sb.WriteString(expanded)
				replaced = true
			} else {
				sb.WriteByte('?')
			}
			count++
			continue
		}
		sb.WriteByte(c)
	}

	if !replaced {
		return "", nil, fmt.Errorf("ExpandIn: query has %d placeholders, slice at arg index %d not reached", count, sliceIdx)
	}

	return sb.String(), out, nil
}
