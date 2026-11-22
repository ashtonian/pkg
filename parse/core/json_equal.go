package core

// JSONEqual reports whether two documents, as NormalizeJSON returns them,
// are equal as PostgreSQL's jsonb compares them: objects by key set and
// member values, arrays element by element in order, numbers by exact value
// (1 equals 1.0), other scalars by value.
func JSONEqual(a, b any) bool {
	switch x := a.(type) {
	case map[string]any:
		y, ok := b.(map[string]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for k, xv := range x {
			yv, ok := y[k]
			if !ok || !JSONEqual(xv, yv) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !JSONEqual(x[i], y[i]) {
				return false
			}
		}
		return true
	case string, bool, nil:
		return a == b
	}
	c, ok := CompareNumbers(a, b)
	return ok && c == 0
}
