package formula

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/BurntSushi/toml"
)

// Formulas live in internal/formula/formulas/ (source of truth).
// They are embedded into the binary and provisioned to .beads/formulas/ at install time.

//go:embed formulas/*.formula.toml
var formulasFS embed.FS

// InstalledRecord tracks which formulas were installed and their checksums.
// Stored in .beads/formulas/.installed.json
type InstalledRecord struct {
	Formulas map[string]string `json:"formulas"` // filename -> sha256 at install time
}

// FormulaStatus represents the status of a single formula during health check.
type FormulaStatus struct {
	Name          string
	Status        string // "ok", "outdated", "modified", "missing", "new", "untracked"
	EmbeddedHash  string // hash computed from embedded content
	InstalledHash string // hash we installed (from .installed.json)
	CurrentHash   string // hash of current file on disk
	// EmbeddedChanged is set on a "modified" formula whose embedded copy has
	// ALSO moved since install. UpdateFormulas skips modified files by design,
	// so such a formula is pinned to its local edit forever — a fix shipped in
	// the embedded corpus never reaches the town, and without this flag nothing
	// says so. See gt-0sq.
	EmbeddedChanged bool
}

// HealthReport contains the results of checking formula health.
type HealthReport struct {
	Formulas []FormulaStatus
	// Counts
	OK        int
	Outdated  int // embedded changed, user hasn't modified
	Modified  int // user modified the file (tracked in .installed.json)
	Missing   int // file was deleted
	New       int // new formula not yet installed
	Untracked int // file exists but not in .installed.json (safe to update)
	Error     int // file could not be read (e.g. permission denied)
	// ModifiedDrift counts the subset of Modified whose embedded copy has also
	// changed since install: local edits shadowing a newer shipped default.
	// Not auto-fixable — overwriting would discard the local edit — so it is
	// reported for manual reconciliation rather than repaired.
	ModifiedDrift int
}

// ResolveFormulaContent resolves formula content using the three-tier precedence
// defined in docs/design/formula-resolution.md: rig > town > embedded.
//
// Tier 1 (rig): townRoot/rigName/.beads/formulas/<name>.formula.toml
// Tier 2 (town): townRoot/.beads/formulas/<name>.formula.toml
// Tier 3 (embedded): compiled into the binary
//
// Either townRoot or rigName may be empty; those tiers are skipped.
//
// Consequence worth stating explicitly, because it has been rediscovered the
// hard way more than once: because tiers 1 and 2 are read from disk on every
// call, editing a provisioned formula file takes effect on the next gt prime
// with no rebuild. The converse also holds — rebuilding gt only changes tier 3,
// which an edited disk copy goes on shadowing, so shipping a fix in the
// embedded corpus does not reach a town that has a disk copy. See
// docs/design/directives-and-overlays.md ("Editing a Formula File Directly")
// for which edits survive UpdateFormulas and which get silently overwritten.
// Callers that need to know WHICH tier won, or whether the winning copy is
// shadowing a newer shipped default, should use ResolveFormula instead — this
// function is a thin wrapper over it and the two can never disagree about which
// copy executes.
func ResolveFormulaContent(name, townRoot, rigName string) ([]byte, error) {
	resolved, err := ResolveFormula(name, townRoot, rigName)
	if err != nil {
		return nil, err
	}
	return resolved.Content, nil
}

// Pours reports whether a formula asks for its steps to be materialized as
// child wisps (pour = true) rather than read inline from the formula itself.
//
// ref is either a formula name — resolved through the same rig > town >
// embedded tiers as ResolveFormulaContent, so the answer describes the copy
// that actually executes — or a path to a formula file, which is what sling
// passes when it has extracted the embedded formula to a temp file for bd.
//
// pour is read from the formula's own declaration and is deliberately not
// inherited through extends: resolveChain copies the child's value verbatim,
// so a formula that says nothing about pour does not pour, whatever its
// parents do.
func Pours(ref, townRoot, rigName string) (bool, error) {
	var content []byte
	if hasFormulaSuffix(ref) {
		// Path form: only a ref that is already a readable formula file is
		// taken as one; a name carrying the suffix still resolves by tier.
		if data, err := os.ReadFile(ref); err == nil { //nolint:gosec // G304: ref is a gt-generated temp path or a formula name
			content = data
		}
	}
	if content == nil {
		resolved, err := ResolveFormulaContent(ref, townRoot, rigName)
		if err != nil {
			return false, err
		}
		content = resolved
	}

	// Decode only the pour key. Parse would also Validate, and a formula that
	// bd cooks happily but gastown's validator rejects must still be able to
	// answer this question — the alternative is a warning on every dispatch.
	var declaration struct {
		Pour bool `toml:"pour"`
	}
	if _, err := toml.Decode(string(content), &declaration); err != nil {
		return false, fmt.Errorf("parsing formula %q: %w", ref, err)
	}
	return declaration.Pour, nil
}

// GetEmbeddedFormulaContent returns the raw content of an embedded formula by name.
// The name can be with or without the .formula.toml suffix.
// Returns the content bytes, or an error if the formula is not found.
func GetEmbeddedFormulaContent(name string) ([]byte, error) {
	// Normalize: ensure the filename has the correct suffix
	filename := name
	if !hasFormulaSuffix(filename) {
		filename = filename + ".formula.toml"
	}
	content, err := formulasFS.ReadFile("formulas/" + filename)
	if err != nil {
		return nil, fmt.Errorf("embedded formula %q not found: %w", name, err)
	}
	return content, nil
}

// hasFormulaSuffix checks if a name already has a formula file suffix.
func hasFormulaSuffix(name string) bool {
	return len(name) > len(".formula.toml") &&
		name[len(name)-len(".formula.toml"):] == ".formula.toml"
}

// computeHash computes SHA256 hash of data.
func computeHash(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// getEmbeddedFormulas returns a map of filename -> sha256 for all embedded formulas.
func getEmbeddedFormulas() (map[string]string, error) {
	entries, err := formulasFS.ReadDir("formulas")
	if err != nil {
		return nil, fmt.Errorf("reading formulas directory: %w", err)
	}

	result := make(map[string]string)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		content, err := formulasFS.ReadFile("formulas/" + entry.Name())
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", entry.Name(), err)
		}
		result[entry.Name()] = computeHash(content)
	}
	return result, nil
}

// loadInstalledRecord loads the installed record from disk.
func loadInstalledRecord(formulasDir string) (*InstalledRecord, error) {
	path := filepath.Join(formulasDir, ".installed.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &InstalledRecord{Formulas: make(map[string]string)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading installed record: %w", err)
	}
	var r InstalledRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("parsing installed record: %w", err)
	}
	if r.Formulas == nil {
		r.Formulas = make(map[string]string)
	}
	return &r, nil
}

// saveInstalledRecord saves the installed record to disk.
func saveInstalledRecord(formulasDir string, record *InstalledRecord) error {
	path := filepath.Join(formulasDir, ".installed.json")
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding installed record: %w", err)
	}
	return os.WriteFile(path, data, 0644)
}

// computeFileHash computes SHA256 hash of a file.
func computeFileHash(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return computeHash(data), nil
}

// ProvisionFormulas creates the .beads/formulas/ directory with embedded formulas.
// This is called during gt install for fresh installations.
// If a formula already exists, it is skipped (no overwrite).
// Returns the number of formulas provisioned.
func ProvisionFormulas(beadsPath string) (int, error) {
	embedded, err := getEmbeddedFormulas()
	if err != nil {
		return 0, err
	}

	entries, err := formulasFS.ReadDir("formulas")
	if err != nil {
		return 0, fmt.Errorf("reading formulas directory: %w", err)
	}

	// Create .beads/formulas/ directory
	formulasDir := filepath.Join(beadsPath, ".beads", "formulas")
	if err := os.MkdirAll(formulasDir, 0755); err != nil {
		return 0, fmt.Errorf("creating formulas directory: %w", err)
	}

	// Load existing installed record (or create new)
	installed, err := loadInstalledRecord(formulasDir)
	if err != nil {
		return 0, err
	}

	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		destPath := filepath.Join(formulasDir, entry.Name())

		// Skip if formula already exists (don't overwrite user customizations)
		if _, err := os.Stat(destPath); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return count, fmt.Errorf("checking %s: %w", entry.Name(), err)
		}

		content, err := formulasFS.ReadFile("formulas/" + entry.Name())
		if err != nil {
			return count, fmt.Errorf("reading %s: %w", entry.Name(), err)
		}

		if err := os.WriteFile(destPath, content, 0644); err != nil {
			return count, fmt.Errorf("writing %s: %w", entry.Name(), err)
		}

		// Record the hash we installed
		if hash, ok := embedded[entry.Name()]; ok {
			installed.Formulas[entry.Name()] = hash
		}
		count++
	}

	// Save updated installed record
	if err := saveInstalledRecord(formulasDir, installed); err != nil {
		return count, fmt.Errorf("saving installed record: %w", err)
	}

	return count, nil
}

// CheckFormulaHealth checks the status of all formulas.
// Returns a report of which formulas are ok, outdated, modified, or missing.
func CheckFormulaHealth(beadsPath string) (*HealthReport, error) {
	embedded, err := getEmbeddedFormulas()
	if err != nil {
		return nil, err
	}

	formulasDir := filepath.Join(beadsPath, ".beads", "formulas")
	installed, err := loadInstalledRecord(formulasDir)
	if err != nil {
		return nil, err
	}

	report := &HealthReport{}

	for filename, embeddedHash := range embedded {
		status := FormulaStatus{
			Name:         filename,
			EmbeddedHash: embeddedHash,
		}

		installedHash, wasInstalled := installed.Formulas[filename]
		status.InstalledHash = installedHash

		destPath := filepath.Join(formulasDir, filename)
		currentHash, err := computeFileHash(destPath)

		if os.IsNotExist(err) {
			// File doesn't exist
			if wasInstalled {
				// We installed it before, user deleted it
				status.Status = "missing"
				report.Missing++
			} else {
				// New formula, never installed
				status.Status = "new"
				report.New++
			}
		} else if err != nil {
			// Some other error reading file (e.g. permission denied)
			status.Status = "error"
			report.Error++
		} else {
			status.CurrentHash = currentHash

			if currentHash == embeddedHash {
				// File matches embedded - all good
				status.Status = "ok"
				report.OK++
			} else if wasInstalled && currentHash == installedHash {
				// File matches what we installed, but embedded has changed
				// User hasn't modified, safe to update
				status.Status = "outdated"
				report.Outdated++
			} else if wasInstalled {
				// File was tracked and user modified it - don't overwrite
				status.Status = "modified"
				report.Modified++
				if installedHash != embeddedHash {
					// The shipped default moved after this file was customized.
					// UpdateFormulas will keep skipping it, so the town stays on
					// the local edit until someone reconciles the two by hand.
					status.EmbeddedChanged = true
					report.ModifiedDrift++
				}
			} else {
				// File exists but not tracked (e.g., from older gt version)
				// Safe to update since we have no record of user modification
				status.Status = "untracked"
				report.Untracked++
			}
		}

		report.Formulas = append(report.Formulas, status)
	}

	return report, nil
}

// UpdateReport describes what UpdateFormulas did — and, just as importantly,
// what it deliberately refused to do. Refusing to overwrite a locally modified
// formula is correct behaviour, but a refusal nobody is told about is
// indistinguishable from an update that worked (gt-bxu).
type UpdateReport struct {
	Updated     int
	Reinstalled int
	// Skipped names the locally modified formulas left untouched, sorted.
	Skipped []string
	// Drifted is the subset of Skipped whose embedded copy has ALSO moved
	// since install: the town is pinned to its local edit while the shipped
	// default has changed underneath it. No amount of re-running --fix will
	// clear these; only a hand reconcile will.
	Drifted []string
}

// UpdateFormulas updates formulas that are safe to update (outdated, missing, or untracked).
// Skips user-modified formulas (tracked files that user changed).
// Returns counts of updated, skipped (modified), and reinstalled (missing).
//
// Callers that need to tell the operator WHICH formulas were skipped, or which
// of them are drifted, should use UpdateFormulasDetailed instead.
func UpdateFormulas(beadsPath string) (updated, skipped, reinstalled int, err error) {
	report, err := UpdateFormulasDetailed(beadsPath)
	return report.Updated, len(report.Skipped), report.Reinstalled, err
}

// UpdateFormulasDetailed is UpdateFormulas with a report that names the
// formulas it skipped and flags the drifted subset.
func UpdateFormulasDetailed(beadsPath string) (*UpdateReport, error) {
	result := &UpdateReport{}

	embedded, err := getEmbeddedFormulas()
	if err != nil {
		return result, err
	}

	formulasDir := filepath.Join(beadsPath, ".beads", "formulas")
	if err := os.MkdirAll(formulasDir, 0755); err != nil {
		return result, fmt.Errorf("creating formulas directory: %w", err)
	}

	installed, err := loadInstalledRecord(formulasDir)
	if err != nil {
		return result, err
	}

	for filename, embeddedHash := range embedded {
		installedHash, wasInstalled := installed.Formulas[filename]
		destPath := filepath.Join(formulasDir, filename)
		currentHash, fileErr := computeFileHash(destPath)

		shouldInstall := false
		isMissing := false
		isModified := false

		if os.IsNotExist(fileErr) {
			// File doesn't exist - install it
			shouldInstall = true
			if wasInstalled {
				isMissing = true
			}
		} else if fileErr != nil {
			// Error reading file, skip
			continue
		} else if currentHash == embeddedHash {
			// Already up to date
			continue
		} else if wasInstalled && currentHash == installedHash {
			// User hasn't modified, safe to update
			shouldInstall = true
		} else if wasInstalled {
			// Tracked file was modified by user - skip
			isModified = true
		} else {
			// Untracked file (e.g., from older gt version) - safe to update
			shouldInstall = true
		}

		if isModified {
			result.Skipped = append(result.Skipped, filename)
			if installedHash != embeddedHash {
				// Local edit AND the shipped default moved: this skip is
				// permanent until someone reconciles the two by hand.
				result.Drifted = append(result.Drifted, filename)
			}
			continue
		}

		if shouldInstall {
			content, err := formulasFS.ReadFile("formulas/" + filename)
			if err != nil {
				return result, fmt.Errorf("reading %s: %w", filename, err)
			}

			if err := os.WriteFile(destPath, content, 0644); err != nil {
				return result, fmt.Errorf("writing %s: %w", filename, err)
			}

			// Update installed record
			installed.Formulas[filename] = embeddedHash

			if isMissing {
				result.Reinstalled++
			} else {
				result.Updated++
			}
		}
	}

	// Embedded formulas come out of a map, so fix an order the caller can print.
	sort.Strings(result.Skipped)
	sort.Strings(result.Drifted)

	// Save updated installed record
	if err := saveInstalledRecord(formulasDir, installed); err != nil {
		return result, fmt.Errorf("saving installed record: %w", err)
	}

	return result, nil
}
