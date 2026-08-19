package configreg

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"gopkg.in/yaml.v3"

	_ "github.com/go-sql-driver/mysql" // Dolt speaks the MySQL wire protocol.

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/formula"
)

// Options controls which layers Collect reads.
type Options struct {
	// TownRoot is the Gas Town workspace root. Required.
	TownRoot string
	// SkipDolt lists the config surface without contacting the Dolt server.
	// The Dolt layer is then reported absent rather than silently omitted.
	SkipDolt bool
	// Timeout bounds each external call (Dolt query, git config).
	// Zero means DefaultTimeout.
	Timeout time.Duration
	// EnvPrefixes selects which environment variables are inventoried.
	// Zero value means DefaultEnvPrefixes.
	EnvPrefixes []string
}

// DefaultTimeout bounds each external call made while collecting.
const DefaultTimeout = 5 * time.Second

// DefaultEnvPrefixes are the prefixes gt and beads read configuration from.
// Enumeration is by prefix, not by a list of known variable names, so a new
// GT_* variable appears without anyone updating this package.
var DefaultEnvPrefixes = []string{"GT_", "BEADS_", "BD_", "DOLT_"}

func (o Options) timeout() time.Duration {
	if o.Timeout > 0 {
		return o.Timeout
	}
	return DefaultTimeout
}

func (o Options) envPrefixes() []string {
	if len(o.EnvPrefixes) > 0 {
		return o.EnvPrefixes
	}
	return DefaultEnvPrefixes
}

// Collect reads every configuration layer and resolves precedence.
//
// It does not stop at the first unreadable layer: the caller gets everything
// that could be read plus a LayerStatus for each layer, so a short listing is
// always distinguishable from a broken one. Check Report.FailureError.
func Collect(opts Options) (*Report, error) {
	if opts.TownRoot == "" {
		return nil, fmt.Errorf("configreg: TownRoot is required")
	}
	b := newBuilder()

	collectTownSettings(b, opts.TownRoot)
	collectDaemonJSON(b, opts.TownRoot)
	rigs := collectRigSettings(b, opts.TownRoot)
	collectBeadsNamespaces(b, opts, rigs)
	collectFormulaVars(b, opts.TownRoot)
	collectEnv(b, opts)

	return b.resolve(opts.TownRoot), nil
}

// --- struct-backed file layers ---

// collectTownSettings reads <town>/settings/config.json. Keys and defaults come
// from reflecting TownSettings; whether a key is *set* comes from the raw file,
// because a struct field cannot tell an absent key from an explicit false.
func collectTownSettings(b *builder, townRoot string) {
	path := config.TownSettingsPath(townRoot)
	const scope = "town/settings"

	loaded := &config.TownSettings{}
	doc, status := readJSONDoc(path, LayerTownSettings, loaded)
	leaves, err := WalkStruct(loaded, config.NewTownSettings())
	if err != nil {
		status.Status = StatusError
		status.Error = err.Error()
		b.layer(status)
		return
	}
	status.Keys = declareAndObserve(b, scope, LayerTownSettings, path, leaves, doc)
	b.layer(status)
}

// collectDaemonJSON reads <town>/mayor/daemon.json.
//
// Defaults for this layer are the ones gt init provisions
// (daemon.DefaultLifecycleConfig). Individual patrols carry their own
// compiled-in fallbacks for a genuinely absent file, and those are not always
// the same number — see gt-il30 notes.
func collectDaemonJSON(b *builder, townRoot string) {
	path := daemon.PatrolConfigFile(townRoot)
	const scope = "town/daemon"

	loaded := &daemon.DaemonPatrolConfig{}
	doc, status := readJSONDoc(path, LayerDaemonJSON, loaded)
	leaves, err := WalkStruct(loaded, daemon.DefaultLifecycleConfig())
	if err != nil {
		status.Status = StatusError
		status.Error = err.Error()
		b.layer(status)
		return
	}
	status.Keys = declareAndObserve(b, scope, LayerDaemonJSON, path, leaves, doc)
	b.layer(status)
}

// collectRigSettings reads each rig's settings/config.json and returns the
// discovered rig directories for the beads-namespace sweep.
func collectRigSettings(b *builder, townRoot string) map[string]string {
	rigsPath := filepath.Join(townRoot, "mayor", "rigs.json")
	rigsCfg, err := config.LoadRigsConfig(rigsPath)
	if err != nil {
		// A town with no registry has no rigs to configure; an unparseable one
		// hides rigs that do exist. Those are different answers.
		status := StatusError
		if errors.Is(err, config.ErrNotFound) {
			status = StatusAbsent
		}
		b.layer(LayerStatus{Layer: LayerRigSettings, Path: rigsPath, Status: status, Error: err.Error()})
		return nil
	}

	rigs := make(map[string]string, len(rigsCfg.Rigs))
	names := make([]string, 0, len(rigsCfg.Rigs))
	for name := range rigsCfg.Rigs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		rigPath := filepath.Join(townRoot, name)
		rigs[name] = rigPath

		path := config.RigSettingsPath(rigPath)
		loaded := &config.RigSettings{}
		doc, status := readJSONDoc(path, LayerRigSettings, loaded)
		leaves, err := WalkStruct(loaded, config.NewRigSettings())
		if err != nil {
			status.Status = StatusError
			status.Error = err.Error()
			b.layer(status)
			continue
		}
		status.Keys = declareAndObserve(b, "rig/"+name, LayerRigSettings, path, leaves, doc)
		b.layer(status)
	}
	return rigs
}

// readJSONDoc loads a JSON config file into out and also returns it decoded as
// a generic document, used to tell "key absent" from "key explicitly zero".
func readJSONDoc(path, layer string, out any) (map[string]any, LayerStatus) {
	status := LayerStatus{Layer: layer, Path: path}
	data, err := os.ReadFile(path) //nolint:gosec // G304: path is derived from the town root
	if err != nil {
		if os.IsNotExist(err) {
			status.Status = StatusAbsent
			return nil, status
		}
		status.Status = StatusError
		status.Error = err.Error()
		return nil, status
	}
	if err := json.Unmarshal(data, out); err != nil {
		status.Status = StatusError
		status.Error = fmt.Sprintf("parsing %s: %v", filepath.Base(path), err)
		return nil, status
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		status.Status = StatusError
		status.Error = fmt.Sprintf("parsing %s: %v", filepath.Base(path), err)
		return nil, status
	}
	status.Status = StatusOK
	return doc, status
}

// declareAndObserve declares every reflected key and records an occurrence for
// the ones the raw document actually contains. Returns the number set.
func declareAndObserve(b *builder, scope, layer, path string, leaves []Leaf, doc map[string]any) int {
	set := 0
	for _, l := range leaves {
		b.declare(scope, l.Key, l.Type, l.Default, "")
		if raw, ok := lookupPath(doc, l.Key); ok {
			b.observe(scope, l.Key, layer, path, renderAny(raw, l.Value))
			set++
		}
	}
	return set
}

// lookupPath walks a dotted key through a decoded JSON/YAML document.
func lookupPath(doc map[string]any, key string) (any, bool) {
	var cur any = doc
	for _, seg := range strings.Split(key, ".") {
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

// renderAny renders a value decoded from a config file, falling back to the
// value the struct walk computed when the raw form is not a simple scalar.
func renderAny(v any, fallback string) string {
	switch t := v.(type) {
	case nil:
		return fallback
	case string:
		return t
	case bool:
		return fmt.Sprintf("%t", t)
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
		return fmt.Sprintf("%g", t)
	case int:
		return fmt.Sprintf("%d", t)
	}
	if b, err := json.Marshal(v); err == nil {
		return string(b)
	}
	return fallback
}

// --- beads namespaces: config.yaml, git config, and the Dolt config table ---

// beadsNamespace is one beads data store and the places that configure it.
type beadsNamespace struct {
	// Name is the scope suffix: the Dolt database name when known, else the dir.
	Name string
	// Dir is the resolved .beads directory.
	Dir string
	// Database is the Dolt database from metadata.json, empty when unknown.
	Database string
}

// collectBeadsNamespaces sweeps the town root and every rig for beads
// namespaces, then reads all three layers that configure each one. These share
// a scope on purpose: config.yaml, git config and the Dolt config table hold
// the same key names, and which one wins is exactly what was invisible.
func collectBeadsNamespaces(b *builder, opts Options, rigs map[string]string) {
	roots := []string{opts.TownRoot}
	names := make([]string, 0, len(rigs))
	for name := range rigs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		roots = append(roots, rigs[name])
	}

	seen := map[string]bool{}
	var namespaces []beadsNamespace
	for _, root := range roots {
		if _, err := os.Stat(filepath.Join(root, ".beads")); err != nil {
			continue
		}
		dir := beads.ResolveBeadsDir(root)
		if seen[dir] {
			continue
		}
		seen[dir] = true
		db := beads.DatabaseNameFromMetadata(dir)
		name := db
		if name == "" {
			name = filepath.Base(filepath.Dir(dir))
		}
		namespaces = append(namespaces, beadsNamespace{Name: name, Dir: dir, Database: db})
	}

	for _, ns := range namespaces {
		scope := "beads/" + ns.Name
		collectBeadsYAML(b, scope, ns)
		collectGitConfig(b, scope, ns, opts.timeout())
	}
	collectDoltConfig(b, opts, namespaces)
}

// collectBeadsYAML reads <namespace>/.beads/config.yaml. Keys come from the file
// itself — beads owns this schema, gt has no struct for it — so every key an
// operator has written is listed whether or not gt knows what it means.
func collectBeadsYAML(b *builder, scope string, ns beadsNamespace) {
	path := filepath.Join(ns.Dir, "config.yaml")
	status := LayerStatus{Layer: LayerBeadsYAML, Path: path}

	data, err := os.ReadFile(path) //nolint:gosec // G304: path is derived from the town root
	if err != nil {
		if os.IsNotExist(err) {
			status.Status = StatusAbsent
		} else {
			status.Status = StatusError
			status.Error = err.Error()
		}
		b.layer(status)
		return
	}

	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		status.Status = StatusError
		status.Error = fmt.Sprintf("parsing config.yaml: %v", err)
		b.layer(status)
		return
	}

	for _, kv := range flatten("", doc) {
		b.observe(scope, kv.key, LayerBeadsYAML, path, kv.value)
		status.Keys++
	}
	status.Status = StatusOK
	b.layer(status)
}

// collectGitConfig reads beads.* from the git repo that owns the namespace.
// beads.role decides write routing, and it is set per repo where nothing else
// in the town can see it.
func collectGitConfig(b *builder, scope string, ns beadsNamespace, timeout time.Duration) {
	repo := filepath.Dir(ns.Dir)
	status := LayerStatus{Layer: LayerGitConfig, Path: "git:" + repo}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "git", "-C", repo, "config", "--get-regexp", `^beads\.`).Output()
	if err != nil {
		// git config exits 1 when nothing matches; that is "absent", not broken.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			status.Status = StatusAbsent
			b.layer(status)
			return
		}
		status.Status = StatusError
		status.Error = err.Error()
		b.layer(status)
		return
	}

	for line := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		key, value, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		b.observe(scope, key, LayerGitConfig, "git:"+repo, value)
		status.Keys++
	}
	status.Status = StatusOK
	b.layer(status)
}

// collectDoltConfig reads the config table of each namespace's Dolt database.
// Every row is listed: this table is the layer that silently won over
// config.yaml, and a curated subset would hide the next such key.
func collectDoltConfig(b *builder, opts Options, namespaces []beadsNamespace) {
	host := config.ResolveDoltHost(opts.TownRoot)
	if host == "" {
		// ResolveDoltHost leaves the default to the caller.
		host = "127.0.0.1"
	}
	port := config.ResolveDoltPort(opts.TownRoot)
	endpoint := fmt.Sprintf("%s:%d", host, port)

	for _, ns := range namespaces {
		if ns.Database == "" {
			continue
		}
		status := LayerStatus{Layer: LayerDoltConfig, Path: fmt.Sprintf("dolt:%s/%s", endpoint, ns.Database)}
		if opts.SkipDolt {
			status.Status = StatusAbsent
			status.Error = "skipped (--no-dolt)"
			b.layer(status)
			continue
		}

		rows, err := queryDoltConfig(host, port, ns.Database, opts.timeout())
		if err != nil {
			status.Status = StatusError
			status.Error = err.Error()
			b.layer(status)
			continue
		}
		scope := "beads/" + ns.Name
		for _, kv := range rows {
			b.observe(scope, kv.key, LayerDoltConfig, status.Path, kv.value)
			status.Keys++
		}
		status.Status = StatusOK
		b.layer(status)
	}
}

type kv struct{ key, value string }

func queryDoltConfig(host string, port int, database string, timeout time.Duration) ([]kv, error) {
	if strings.ContainsAny(database, "`'\"\\/ ") {
		return nil, fmt.Errorf("invalid database name %q", database)
	}
	dsn := fmt.Sprintf("root@tcp(%s:%d)/%s?parseTime=true&timeout=%s&readTimeout=%s",
		host, port, database, timeout, timeout)
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	rows, err := db.QueryContext(ctx, "SELECT `key`, `value` FROM config")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []kv
	for rows.Next() {
		var k, v sql.NullString
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out = append(out, kv{key: k.String, value: v.String})
	}
	return out, rows.Err()
}

// --- formula vars ---

// collectFormulaVars enumerates the vars every installed formula declares.
// These are not town settings: the acting value is whatever the wisp was
// materialised with. What is listed here is the formula's declared default,
// which is the only part an operator can change ahead of time.
func collectFormulaVars(b *builder, townRoot string) {
	dir := filepath.Join(townRoot, ".beads", "formulas")
	status := LayerStatus{Layer: LayerFormulaVar, Path: dir}

	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			status.Status = StatusAbsent
		} else {
			status.Status = StatusError
			status.Error = err.Error()
		}
		b.layer(status)
		return
	}

	var failed []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		// Decode rather than formula.ParseFile: a formula that fails semantic
		// validation still declares vars, and its vars are configuration an
		// operator needs to see. Only unreadable TOML is a gap in the listing.
		data, err := os.ReadFile(path) //nolint:gosec // G304: path is derived from the town root
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", e.Name(), err))
			continue
		}
		var f formula.Formula
		if _, err := toml.Decode(string(data), &f); err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", e.Name(), err))
			continue
		}
		name := f.Name
		if name == "" {
			name = strings.TrimSuffix(e.Name(), ".formula.toml")
		}
		scope := "formula/" + name
		for varName, v := range f.Vars {
			b.declare(scope, varName, "string", v.Default, v.Description)
			if v.Default != "" {
				b.observe(scope, varName, LayerFormulaVar, path, v.Default)
				status.Keys++
			}
		}
	}

	if len(failed) > 0 {
		status.Status = StatusError
		status.Error = fmt.Sprintf("%d formula(s) unreadable: %s", len(failed), strings.Join(failed, "; "))
	} else {
		status.Status = StatusOK
	}
	b.layer(status)
}

// --- environment ---

// collectEnv inventories the process environment by prefix. An exported GT_*
// variable overrides everything below it and leaves no trace in any file, so
// it belongs in the listing even though no struct declares it.
func collectEnv(b *builder, opts Options) {
	status := LayerStatus{Layer: LayerEnv, Path: "process environment", Status: StatusOK}
	prefixes := opts.envPrefixes()

	for _, kv := range os.Environ() {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		matched := false
		for _, p := range prefixes {
			if strings.HasPrefix(name, p) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		b.observe("env", name, LayerEnv, "process environment", value)
		status.Keys++
	}
	b.layer(status)
}

// --- helpers ---

// flatten turns a decoded YAML document into dotted key/value pairs. Nested
// maps become dotted keys; lists and anything else are rendered as JSON.
func flatten(prefix string, doc map[string]any) []kv {
	var out []kv
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, k := range keys {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		switch v := doc[k].(type) {
		case map[string]any:
			out = append(out, flatten(key, v)...)
		default:
			out = append(out, kv{key: key, value: renderAny(doc[k], "")})
		}
	}
	return out
}
