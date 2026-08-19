package beads

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
)

// ChildInfo holds the fields callers read from `bd show <id> --children --json`.
type ChildInfo struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Status string `json:"status"`
}

// ParseChildrenJSON parses the output of `bd show <id> --children --json`.
//
// bd returns a map keyed by parent ID plus envelope metadata:
// {"hq-wisp-abc": [{...}, ...], "schema_version": 1}. A bare array is also
// accepted for legacy compatibility.
//
// The envelope is the whole reason this is shared rather than reimplemented per
// caller. Decoding it as a bare array or as {"children": [...]} does not fail —
// it succeeds and yields nothing, so the caller reports a confident zero
// children for a molecule that has seven. That is the shape of both gt-fqd5's
// own near-miss and the gt doctor wisp check that reported OK on every rig
// forever.
func ParseChildrenJSON(raw string) ([]ChildInfo, error) {
	data := bytes.TrimSpace([]byte(raw))
	if len(data) == 0 {
		return nil, fmt.Errorf("empty children JSON")
	}

	var arr []ChildInfo
	if data[0] == '[' {
		if err := json.Unmarshal(data, &arr); err != nil {
			return nil, err
		}
		return arr, nil
	}

	if data[0] != '{' {
		return nil, fmt.Errorf("unrecognized JSON shape: %.200s", raw)
	}

	var wrapped map[string]json.RawMessage
	if err := json.Unmarshal(data, &wrapped); err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(wrapped))
	for key := range wrapped {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var children []ChildInfo
	sawChildArray := false
	for _, key := range keys {
		if key == "schema_version" {
			continue
		}

		value := bytes.TrimSpace(wrapped[key])
		if len(value) == 0 {
			return nil, fmt.Errorf("empty child payload for key %q", key)
		}
		if value[0] != '[' {
			return nil, fmt.Errorf("non-array child payload for key %q", key)
		}

		var group []ChildInfo
		if err := json.Unmarshal(value, &group); err != nil {
			return nil, fmt.Errorf("parse child array for key %q: %w", key, err)
		}
		children = append(children, group...)
		sawChildArray = true
	}

	if !sawChildArray {
		return nil, fmt.Errorf("children JSON object has no child arrays")
	}

	return children, nil
}
