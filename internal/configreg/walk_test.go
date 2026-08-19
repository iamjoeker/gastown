package configreg

import (
	"testing"
	"time"
)

type innerCfg struct {
	Timeout string `json:"timeout,omitempty"`
	Retries *int   `json:"retries,omitempty"`
	Silent  bool   `json:"silent"`
	Hidden  string `json:"-"`
	NoTag   string
}

// TimeoutD is the accessor production code would call: it encodes the
// compiled-in default so the walker can read it back.
func (c *innerCfg) TimeoutD() time.Duration {
	if c != nil && c.Timeout != "" {
		if d, err := time.ParseDuration(c.Timeout); err == nil {
			return d
		}
	}
	return 90 * time.Second
}

// GetRetries mirrors the other accessor convention Gas Town uses.
func (c *innerCfg) GetRetries() int {
	if c != nil && c.Retries != nil {
		return *c.Retries
	}
	return 3
}

type outerCfg struct {
	Name    string            `json:"name,omitempty"`
	Inner   *innerCfg         `json:"inner,omitempty"`
	Nested  innerCfg          `json:"nested"`
	Tags    []string          `json:"tags,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
	Elapsed time.Duration     `json:"elapsed,omitempty"`
}

func leafMap(t *testing.T, leaves []Leaf) map[string]Leaf {
	t.Helper()
	m := make(map[string]Leaf, len(leaves))
	for _, l := range leaves {
		if _, dup := m[l.Key]; dup {
			t.Fatalf("duplicate key %q", l.Key)
		}
		m[l.Key] = l
	}
	return m
}

func TestWalkStructEnumeratesNestedKeys(t *testing.T) {
	leaves, err := WalkStruct(&outerCfg{}, &outerCfg{})
	if err != nil {
		t.Fatalf("WalkStruct: %v", err)
	}
	got := leafMap(t, leaves)

	want := []string{
		"name", "tags", "labels", "elapsed",
		"inner.timeout", "inner.retries", "inner.silent",
		"nested.timeout", "nested.retries", "nested.silent",
	}
	for _, key := range want {
		if _, ok := got[key]; !ok {
			t.Errorf("key %q missing from walk", key)
		}
	}
	// json:"-" is never written to a config file, so no operator can set it.
	if _, ok := got["inner.hidden"]; ok {
		t.Error(`field tagged json:"-" should not be listed`)
	}
	// A field with no json tag is likewise unreachable from a config file.
	for key := range got {
		if key == "inner.NoTag" || key == "inner.notag" {
			t.Errorf("untagged field listed as %q", key)
		}
	}
}

func TestWalkStructReachesKeysUnderNilSection(t *testing.T) {
	// Inner is nil: the whole section is unconfigured, which is precisely the
	// case that used to be invisible.
	leaves, err := WalkStruct(&outerCfg{}, &outerCfg{})
	if err != nil {
		t.Fatalf("WalkStruct: %v", err)
	}
	got := leafMap(t, leaves)

	if l := got["inner.timeout"]; l.Default != "1m30s" || l.Value != "1m30s" {
		t.Errorf("inner.timeout = %+v, want default and value 1m30s from the accessor", l)
	}
	if l := got["inner.retries"]; l.Default != "3" || l.Value != "3" {
		t.Errorf("inner.retries = %+v, want default and value 3 from the accessor", l)
	}
}

func TestWalkStructPrefersAccessorOverRawField(t *testing.T) {
	cur := &outerCfg{Inner: &innerCfg{Timeout: "5s"}}
	leaves, err := WalkStruct(cur, &outerCfg{})
	if err != nil {
		t.Fatalf("WalkStruct: %v", err)
	}
	got := leafMap(t, leaves)

	l := got["inner.timeout"]
	if l.Value != "5s" {
		t.Errorf("inner.timeout value = %q, want 5s", l.Value)
	}
	if l.Default != "1m30s" {
		t.Errorf("inner.timeout default = %q, want the accessor default 1m30s", l.Default)
	}
}

func TestWalkStructDefaultTreeSuppliesDefaults(t *testing.T) {
	// Fields without an accessor take their default from the default tree,
	// the way daemon.DefaultLifecycleConfig supplies patrol defaults.
	def := &outerCfg{Name: "gastown", Nested: innerCfg{Silent: true}}
	leaves, err := WalkStruct(&outerCfg{}, def)
	if err != nil {
		t.Fatalf("WalkStruct: %v", err)
	}
	got := leafMap(t, leaves)

	if l := got["name"]; l.Default != "gastown" || l.Value != "gastown" {
		t.Errorf("name = %+v, want default gastown", l)
	}
	if l := got["nested.silent"]; l.Default != "true" || l.Value != "true" {
		t.Errorf("nested.silent = %+v, want default true", l)
	}
}

func TestWalkStructRendersTypes(t *testing.T) {
	n := 7
	cur := &outerCfg{
		Tags:    []string{"a", "b"},
		Labels:  map[string]string{"k": "v"},
		Elapsed: 90 * time.Second,
		Inner:   &innerCfg{Retries: &n},
	}
	leaves, err := WalkStruct(cur, &outerCfg{})
	if err != nil {
		t.Fatalf("WalkStruct: %v", err)
	}
	got := leafMap(t, leaves)

	for _, tc := range []struct{ key, typ, value string }{
		{"tags", "list", `["a","b"]`},
		{"labels", "map", `{"k":"v"}`},
		{"elapsed", "duration", "1m30s"},
		{"inner.retries", "int", "7"},
		{"inner.silent", "bool", "false"},
		{"name", "string", ""},
	} {
		l := got[tc.key]
		if l.Type != tc.typ {
			t.Errorf("%s type = %q, want %q", tc.key, l.Type, tc.typ)
		}
		if l.Value != tc.value {
			t.Errorf("%s value = %q, want %q", tc.key, l.Value, tc.value)
		}
	}
}

func TestWalkStructNilCurrentIsAllDefaults(t *testing.T) {
	leaves, err := WalkStruct((*outerCfg)(nil), &outerCfg{Name: "gastown"})
	if err != nil {
		t.Fatalf("WalkStruct: %v", err)
	}
	got := leafMap(t, leaves)
	if l := got["name"]; l.Value != "gastown" {
		t.Errorf("name value = %q, want the default gastown", l.Value)
	}
}

func TestWalkStructRejectsMismatchedTypes(t *testing.T) {
	if _, err := WalkStruct(&innerCfg{}, &outerCfg{}); err == nil {
		t.Error("want an error when cur and def are different types")
	}
	if _, err := WalkStruct(&outerCfg{}, nil); err == nil {
		t.Error("want an error when def is nil")
	}
	notAStruct := 3
	if _, err := WalkStruct(&notAStruct, &notAStruct); err == nil {
		t.Error("want an error when def does not point to a struct")
	}
}

type panickyCfg struct {
	Value string `json:"value,omitempty"`
}

// ValueD panics the way a buggy accessor would.
func (c *panickyCfg) ValueD() string {
	panic("boom")
}

func TestWalkStructSurvivesPanickingAccessor(t *testing.T) {
	leaves, err := WalkStruct(&panickyCfg{Value: "x"}, &panickyCfg{})
	if err != nil {
		t.Fatalf("WalkStruct: %v", err)
	}
	got := leafMap(t, leaves)
	if l := got["value"]; l.Value != "x" {
		t.Errorf("value = %q, want the raw field x after the accessor panicked", l.Value)
	}
}

type selfRef struct {
	Next *selfRef `json:"next,omitempty"`
	Name string   `json:"name,omitempty"`
}

func TestWalkStructBoundsRecursion(t *testing.T) {
	// A self-referential config type must not hang the listing.
	done := make(chan []Leaf, 1)
	go func() {
		leaves, _ := WalkStruct(&selfRef{}, &selfRef{})
		done <- leaves
	}()
	select {
	case leaves := <-done:
		if len(leaves) == 0 {
			t.Error("want at least the top-level name key")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("WalkStruct did not terminate on a self-referential type")
	}
}
