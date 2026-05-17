package reranker

// collectIDs maps a slice of selections to a slice of parsed int64 IDs,
// skipping items whose ID string fails the parser. The returned slice
// preserves the original order so it stays meaningful for "which one was
// hallucinated" comparisons.
//
// Used for the reranker.model_response span event: the model returns
// "Topic:42"-style strings, but TraceQL searches and Tempo attribute slices
// want plain ints.
func collectIDs[T any](items []T, getID func(T) string, parse func(string) (int64, error)) []int64 {
	if len(items) == 0 {
		return nil
	}
	out := make([]int64, 0, len(items))
	for _, item := range items {
		id, err := parse(getID(item))
		if err != nil {
			continue
		}
		out = append(out, id)
	}
	return out
}

// diffIDs returns the set difference raw \ kept (i.e. IDs the model returned
// that didn't survive candidate-pool validation — true hallucinations).
//
// Implemented with a small map; raw/kept lists are typically ≤10 each so the
// allocation is negligible. Returns nil rather than an empty slice when the
// diff is empty, so attribute.Int64Slice produces a clean omitted slot.
func diffIDs(raw, kept []int64) []int64 {
	if len(raw) == 0 {
		return nil
	}
	keptSet := make(map[int64]struct{}, len(kept))
	for _, id := range kept {
		keptSet[id] = struct{}{}
	}
	var out []int64
	for _, id := range raw {
		if _, ok := keptSet[id]; !ok {
			out = append(out, id)
		}
	}
	return out
}
