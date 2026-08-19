// Package configreg enumerates the whole Gas Town configuration surface: every
// key, its compiled-in default, its acting value, and which layer supplied it.
//
// Gas Town reads configuration from several independent places — compiled-in
// defaults, town settings, mayor/daemon.json, per-rig settings, .beads/config.yaml,
// the Dolt config table, git config, formula vars, and the process environment.
// Nothing enumerated them, so an unset key that changes the town's operating
// mode looked exactly like a key that does not exist (gt-il30).
//
// The key list is derived, never curated. Struct layers are reflected over their
// json tags, and file/table layers are read key-by-key from the source, so a
// field added to a config struct shows up here without anyone editing a list.
package configreg

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxWalkDepth bounds struct recursion so a self-referential config type
// cannot hang the listing.
const maxWalkDepth = 12

// Leaf is one configuration key discovered by reflecting over a config struct.
type Leaf struct {
	// Key is the dotted path built from json tags, e.g. "scheduler.max_polecats".
	Key string
	// Type is a human-readable type name: string, bool, int, duration, list, map.
	Type string
	// Default is the compiled-in value the code falls back to when unset.
	Default string
	// Value is the effective value: what the code gets when it reads this key.
	Value string
}

var (
	durationType = reflect.TypeOf(time.Duration(0))
	timeType     = reflect.TypeOf(time.Time{})
)

// WalkStruct reflects over cur (the loaded config) alongside def (the tree of
// compiled-in defaults) and returns one Leaf per non-struct field, keyed by its
// json tag path. Both must be pointers to the same struct type; a nil cur means
// "nothing configured" and yields every key at its default.
//
// Defaults are read through the same accessor method production code uses
// (FooD, FooV, GetFoo) whenever one exists, falling back to the def tree. That
// is what keeps this from drifting away from what the code actually reads.
func WalkStruct(cur, def any) ([]Leaf, error) {
	dv := reflect.ValueOf(def)
	if !dv.IsValid() || dv.Kind() != reflect.Pointer || dv.IsNil() {
		return nil, fmt.Errorf("configreg: def must be a non-nil pointer to a struct")
	}
	if dv.Type().Elem().Kind() != reflect.Struct {
		return nil, fmt.Errorf("configreg: def must point to a struct, got %s", dv.Type().Elem().Kind())
	}

	cv := reflect.ValueOf(cur)
	switch {
	case !cv.IsValid() || cv.Kind() != reflect.Pointer || cv.IsNil():
		cv = reflect.New(dv.Type().Elem())
	case cv.Type() != dv.Type():
		return nil, fmt.Errorf("configreg: cur is %s but def is %s", cv.Type(), dv.Type())
	}

	var out []Leaf
	walk("", cv, dv, 0, &out)
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// walk descends cur and def in lockstep. Both are non-nil pointers to the same
// struct type.
func walk(prefix string, cur, def reflect.Value, depth int, out *[]Leaf) {
	if depth > maxWalkDepth {
		return
	}
	curElem, defElem := cur.Elem(), def.Elem()
	t := defElem.Type()

	for i := range t.NumField() {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // unexported
		}
		name, ok := jsonName(f)
		if !ok {
			continue
		}
		key := name
		if prefix != "" {
			key = prefix + "." + name
		}

		dfv, cfv := defElem.Field(i), curElem.Field(i)
		if subCur, subDef, nested := structChild(cfv, dfv); nested {
			walk(key, subCur, subDef, depth+1, out)
			continue
		}
		*out = append(*out, leafFor(key, f, cur, def, cfv, dfv))
	}
}

// structChild reports whether a field is a nested config object and, if so,
// returns non-nil pointers to the current and default sub-structs. A nil
// pointer becomes a pointer to a fresh zero value, so the walk still reaches
// every key underneath an unconfigured section.
func structChild(cfv, dfv reflect.Value) (curPtr, defPtr reflect.Value, ok bool) {
	t := dfv.Type()
	switch {
	case t.Kind() == reflect.Pointer && t.Elem().Kind() == reflect.Struct && t.Elem() != timeType:
		return ptrOrZero(cfv, t.Elem()), ptrOrZero(dfv, t.Elem()), true
	case t.Kind() == reflect.Struct && t != timeType:
		return addrOf(cfv), addrOf(dfv), true
	}
	return reflect.Value{}, reflect.Value{}, false
}

func ptrOrZero(v reflect.Value, structType reflect.Type) reflect.Value {
	if v.IsValid() && !v.IsNil() {
		return v
	}
	return reflect.New(structType)
}

// addrOf returns an addressable copy so pointer-receiver accessors resolve.
func addrOf(v reflect.Value) reflect.Value {
	p := reflect.New(v.Type())
	p.Elem().Set(v)
	return p
}

func leafFor(key string, f reflect.StructField, curPtr, defPtr, cfv, dfv reflect.Value) Leaf {
	l := Leaf{Key: key, Type: typeName(f.Type)}

	if v, ok := accessor(defPtr, f.Name); ok {
		l.Default = v
	} else {
		l.Default = render(dfv)
	}

	switch v, ok := accessor(curPtr, f.Name); {
	case ok:
		l.Value = v
	case !cfv.IsZero():
		l.Value = render(cfv)
	default:
		l.Value = l.Default
	}
	return l
}

// accessor calls the method the rest of the codebase uses to read this field,
// so the reported value is the one production code actually gets. Gas Town
// names these FooD (duration), FooV (value), or GetFoo.
func accessor(ptr reflect.Value, field string) (string, bool) {
	for _, name := range []string{field + "D", field + "V", "Get" + field} {
		m := ptr.MethodByName(name)
		if !m.IsValid() {
			continue
		}
		mt := m.Type()
		if mt.NumIn() != 0 || mt.NumOut() != 1 {
			continue
		}
		// Accessors returning a nested config struct belong to the walk, not here.
		if out := mt.Out(0); out.Kind() == reflect.Pointer && out.Elem().Kind() == reflect.Struct {
			continue
		}
		v, ok := callSafe(m)
		if !ok {
			continue
		}
		return render(v), true
	}
	return "", false
}

// callSafe invokes an accessor, recovering from a panic so one bad method
// cannot take down the whole listing.
func callSafe(m reflect.Value) (v reflect.Value, ok bool) {
	defer func() {
		if recover() != nil {
			v, ok = reflect.Value{}, false
		}
	}()
	out := m.Call(nil)
	return out[0], true
}

func render(v reflect.Value) string {
	if !v.IsValid() {
		return ""
	}
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface:
		if v.IsNil() {
			return ""
		}
		return render(v.Elem())
	case reflect.String:
		return v.String()
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if v.Type() == durationType {
			return time.Duration(v.Int()).String()
		}
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64)
	case reflect.Slice, reflect.Array, reflect.Map:
		if v.Len() == 0 {
			return ""
		}
	case reflect.Struct:
		if v.Type() == timeType {
			t, _ := v.Interface().(time.Time)
			if t.IsZero() {
				return ""
			}
			return t.Format(time.RFC3339)
		}
	}
	if b, err := json.Marshal(v.Interface()); err == nil {
		return string(b)
	}
	return fmt.Sprint(v.Interface())
}

// jsonName returns the config key a field is serialized under. Fields without a
// json tag are skipped: they are never written to a config file, so no operator
// can set them.
func jsonName(f reflect.StructField) (string, bool) {
	tag, ok := f.Tag.Lookup("json")
	if !ok {
		return "", false
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "-" {
		return "", false
	}
	if name == "" {
		name = strings.ToLower(f.Name)
	}
	return name, true
}

func typeName(t reflect.Type) string {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t {
	case durationType:
		return "duration"
	case timeType:
		return "time"
	}
	switch t.Kind() {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "int"
	case reflect.Float32, reflect.Float64:
		return "float"
	case reflect.Slice, reflect.Array:
		return "list"
	case reflect.Map:
		return "map"
	case reflect.Struct:
		return "object"
	}
	return t.Kind().String()
}
