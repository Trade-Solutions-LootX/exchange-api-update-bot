package sources

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// extractArray navigates a decoded-JSON tree to the array of item objects.
//
// Path segments are dot-separated. A segment ending in "[]" means "this key is
// an array; descend into every element". This handles both the common flat
// case ("data.list") and nested-category feeds such as Binance
// ("data.catalogs[].articles[]").
func extractArray(node any, path string) []any {
	if path == "" {
		return asArray(node)
	}
	return walkArray(node, strings.Split(path, "."))
}

func walkArray(node any, segs []string) []any {
	if len(segs) == 0 {
		return asArray(node)
	}
	seg := segs[0]
	flatten := strings.HasSuffix(seg, "[]")
	key := strings.TrimSuffix(seg, "[]")

	m, ok := node.(map[string]any)
	if !ok {
		return nil
	}
	child, ok := m[key]
	if !ok {
		return nil
	}
	if flatten {
		arr, ok := child.([]any)
		if !ok {
			return nil
		}
		// Non-nil so a present-but-empty array (e.g. a quiet-period feed) is
		// reported as "0 items", NOT confused with a missing/changed path (nil).
		out := []any{}
		for _, e := range arr {
			out = append(out, walkArray(e, segs[1:])...)
		}
		return out
	}
	return walkArray(child, segs[1:])
}

func asArray(node any) []any {
	switch v := node.(type) {
	case []any:
		return v
	case nil:
		return nil
	default:
		return []any{v}
	}
}

// getField walks a dot path within a single item and returns the leaf value.
func getField(item any, path string) (any, bool) {
	if path == "" {
		return nil, false
	}
	cur := item
	for _, seg := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[seg]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// getString returns a stringified field value ("" if absent). JSON numbers are
// rendered without scientific notation so IDs remain stable.
func getString(item any, path string) string {
	v, ok := getField(item, path)
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case json.Number:
		// The decoder uses UseNumber(), so numeric IDs arrive here as their
		// exact source text — no float64 rounding, so 19-digit ids stay distinct.
		return string(t)
	case float64:
		// Defensive: values not routed through UseNumber. Preserve integer IDs.
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// parseTime converts a field value to a time using the given layout hint.
// Supported hints: "unix_ms", "unix_s", "unix_ns", "rfc3339", "rfc1123",
// "rfc1123z", or any Go reference layout. Returns zero time on failure.
func parseTime(v any, layout string) time.Time {
	if v == nil {
		return time.Time{}
	}
	switch layout {
	case "unix_ms":
		if n, ok := toInt64(v); ok {
			return time.UnixMilli(n).UTC()
		}
	case "unix_s":
		if n, ok := toInt64(v); ok {
			return time.Unix(n, 0).UTC()
		}
	case "unix_ns":
		if n, ok := toInt64(v); ok {
			return time.Unix(0, n).UTC()
		}
	default:
		s, ok := v.(string)
		if !ok {
			// Some feeds put epoch millis in a string; try that too below.
			if n, ok2 := toInt64(v); ok2 {
				return heuristicEpoch(n)
			}
			return time.Time{}
		}
		s = strings.TrimSpace(s)
		if s == "" {
			return time.Time{}
		}
		layouts := candidateLayouts(layout)
		for _, l := range layouts {
			if t, err := time.Parse(l, s); err == nil {
				return t.UTC()
			}
		}
		// Last resort: a numeric string epoch.
		if n, err := strconv.ParseInt(s, 10, 64); err == nil {
			return heuristicEpoch(n)
		}
	}
	return time.Time{}
}

func candidateLayouts(hint string) []string {
	switch hint {
	case "", "rfc3339":
		return []string{time.RFC3339, time.RFC3339Nano, "2006-01-02T15:04:05Z0700", "2006-01-02 15:04:05"}
	case "rfc1123":
		return []string{time.RFC1123, time.RFC1123Z}
	case "rfc1123z":
		return []string{time.RFC1123Z, time.RFC1123}
	default:
		return []string{hint}
	}
}

// heuristicEpoch decides whether an integer is seconds, millis, or micros by
// magnitude. Robust against feeds that are inconsistent about precision.
func heuristicEpoch(n int64) time.Time {
	switch {
	case n > 1e17: // nanoseconds
		return time.Unix(0, n).UTC()
	case n > 1e14: // microseconds
		return time.UnixMicro(n).UTC()
	case n > 1e11: // milliseconds
		return time.UnixMilli(n).UTC()
	case n > 0: // seconds
		return time.Unix(n, 0).UTC()
	}
	return time.Time{}
}

func toInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case json.Number:
		if n, err := t.Int64(); err == nil {
			return n, true
		}
		// A fractional/oversized numeric string: fall back to parsing the text.
		n, err := strconv.ParseInt(strings.TrimSpace(string(t)), 10, 64)
		return n, err == nil
	case float64:
		return int64(t), true
	case int64:
		return t, true
	case int:
		return int64(t), true
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n, err == nil
	}
	return 0, false
}
