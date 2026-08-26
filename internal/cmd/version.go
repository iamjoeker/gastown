package cmd

import (
	"fmt"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/version"
)

// Version information - set at build time via ldflags
var (
	Version = "1.2.1"
	// Build can be set via ldflags at compile time
	Build = "dev"
	// Commit and Branch - the git revision the binary was built from, stamped
	// at link time by `make build` from the gastown source repo. These are the
	// ONLY trustworthy provenance: see commitProvenance below for why nothing
	// is ever derived from a repository found at runtime.
	Commit = ""
	Branch = ""
	// BuildTime is the UTC timestamp of the build, stamped by `make build`.
	BuildTime = ""
	// BuiltProperly is set to "1" by `make build`. If empty, the binary was built
	// with raw `go build` and is likely unsigned (will be killed on macOS).
	BuiltProperly = ""
)

// Provenance of the commit hash a binary reports. The distinction is
// load-bearing: only provenanceStamped identifies the gastown commit this
// binary was compiled from. (gt-5mvj)
const (
	// provenanceNone: no build commit is available at all.
	provenanceNone = iota
	// provenanceStamped: -X ...cmd.Commit was set at link time by `make build`,
	// which reads the sha from the gastown source repo. Authoritative.
	provenanceStamped
	// provenanceBuildInfo: taken from Go's vcs.revision build setting. Go stamps
	// this from whichever git repository encloses the build directory, which is
	// not necessarily the gastown repo — a gastown tree with no .git of its own
	// makes git walk up and stamp the *enclosing* repo's HEAD. Reporting such a
	// sha as the build commit is what gt-5mvj filed: it resolves in a real repo,
	// so an ancestry test against it succeeds and answers confidently wrong.
	provenanceBuildInfo
)

var versionVerbose bool
var versionShort bool
var versionCommitOnly bool

var versionCmd = &cobra.Command{
	Use:         "version",
	GroupID:     GroupDiag,
	Annotations: map[string]string{AnnotationPolecatSafe: "true"},
	Short:       "Print version information",
	Long: `Print the gt version, build type, git branch, and commit hash.

Output includes the semantic version, whether this is a dev or release build,
and the gastown revision the binary was built from.

Build provenance is stamped at link time by 'make build'. It is never derived
from a git repository found at runtime, because the repository a gt process
happens to be standing in has no relationship to what was compiled. When the
provenance was not stamped, this command reports the commit as "unknown"
rather than substituting a plausible sha from somewhere else.

Use 'gt version --commit' for the machine-readable build commit ("unknown"
when it was not stamped).`,
	Run: func(cmd *cobra.Command, args []string) {
		commit, provenance := resolveCommitHash()

		if versionCommitOnly {
			// Only a link-time stamp identifies this binary. Anything else is
			// reported as unknown so callers cannot ancestry-test a foreign sha.
			if provenance == provenanceStamped {
				fmt.Println(commit)
			} else {
				fmt.Println("unknown")
			}
			return
		}

		if versionShort {
			fmt.Printf("%s-%s\n", Version, Build)
			return
		}

		branch := resolveBranch()

		switch {
		case provenance == provenanceStamped && branch != "":
			fmt.Printf("gt version %s (%s: %s@%s)\n", Version, Build, branch, version.ShortCommit(commit))
		case provenance == provenanceStamped:
			fmt.Printf("gt version %s (%s: %s)\n", Version, Build, version.ShortCommit(commit))
		default:
			// Never print an unstamped sha in the "branch@sha" form: that form
			// reads as an identification of this binary, and it would not be one.
			fmt.Printf("gt version %s (%s: build commit unknown — not stamped by 'make build')\n", Version, Build)
			if provenance == provenanceBuildInfo {
				fmt.Printf("  Go build info reports %s, but that revision comes from whichever repo\n", version.ShortCommit(commit))
				fmt.Printf("  enclosed the build directory and is not verified to be a gastown commit.\n")
			}
			fmt.Printf("  Verify a deployment by exercising the changed behaviour, not by this string.\n")
		}

		if versionVerbose {
			if BuildTime != "" {
				fmt.Printf("Build time: %s\n", BuildTime)
			}
			fmt.Printf("Timestamp: %s\n", time.Now().Format(time.RFC3339))
			fmt.Printf("Go version: %s\n", runtime.Version())
		}
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
	versionCmd.Flags().BoolVarP(&versionVerbose, "verbose", "v", false, "Show extended version info including timestamp")
	versionCmd.Flags().BoolVar(&versionShort, "short", false, "Output only the version number (e.g., 0.5.0-362)")
	versionCmd.Flags().BoolVar(&versionCommitOnly, "commit", false, `Output only the stamped build commit, or "unknown"`)

	// Pass the build-time commit to the version package for stale binary checks
	if Commit != "" {
		version.SetCommit(Commit)
	}
}

// resolveCommitHash returns the commit this binary reports plus the provenance
// of that value. It performs no repository lookup: the answer is fixed at link
// time (or by Go's build stamping) and cannot vary with the working directory.
func resolveCommitHash() (string, int) {
	if Commit != "" {
		return Commit, provenanceStamped
	}

	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" && setting.Value != "" {
				return setting.Value, provenanceBuildInfo
			}
		}
	}

	return "", provenanceNone
}

// resolveBranch returns the branch stamped into the binary at build time, or
// "" when none was stamped.
//
// It deliberately does NOT fall back to running git in the working directory.
// That fallback labelled the binary with whatever branch the *caller's* cwd was
// on, so the same binary reported a different branch from each directory and
// none of them described the build. Combined with an unstamped sha it produced
// strings like "main@0417eec3ecd2" in which neither half came from the gastown
// repo. An absent branch is the correct answer when nothing was stamped.
// (gt-5mvj)
func resolveBranch() string {
	if Branch != "" {
		return Branch
	}

	// Build-time VCS detection. Same caveat as vcs.revision — this describes
	// the repo that enclosed the build dir — so it is only used as a label
	// alongside a stamped commit, never on its own.
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.branch" && setting.Value != "" {
				return setting.Value
			}
		}
	}

	return ""
}
