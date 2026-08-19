package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/steveyegge/gastown/internal/beads"
	gtconfig "github.com/steveyegge/gastown/internal/config"
	"github.com/steveyegge/gastown/internal/daemon"
	"github.com/steveyegge/gastown/internal/doltserver"
	"github.com/steveyegge/gastown/internal/style"
	"github.com/steveyegge/gastown/internal/workspace"
)

var doltCmd = &cobra.Command{
	Use:     "dolt",
	GroupID: GroupServices,
	Short:   "Manage the Dolt SQL server",
	RunE:    requireSubcommand,
	Long: `Manage the Dolt SQL server for Gas Town beads.

The Dolt server provides multi-client access to all rig databases,
avoiding the single-writer limitation of embedded Dolt mode.

Server configuration:
  - Port: 3307 (avoids conflict with MySQL on 3306)
  - User: root (default Dolt user, no password for localhost)
  - Data directory: .dolt-data/ (contains all rig databases)

Each rig (hq, gastown, beads) has its own database subdirectory.`,
}

var doltInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize and repair Dolt workspace configuration",
	Long: `Verify and repair the Dolt workspace configuration.

This command scans all rig metadata.json files for Dolt server configuration
and ensures the referenced databases actually exist. It fixes the broken state
where metadata.json says backend=dolt but the database is missing from .dolt-data/.

For each broken workspace, it will:
  1. Check if local .beads/dolt/ data exists and migrate it
  2. Otherwise, create a fresh database in .dolt-data/

This is safe to run multiple times (idempotent). It will not modify workspaces
that are already healthy.`,
	RunE: runDoltInit,
}

var doltStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the Dolt server",
	Long: `Start the Dolt SQL server in the background.

The server will run until stopped with 'gt dolt stop'.`,
	RunE: runDoltStart,
}

var doltStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the Dolt server",
	Long:  `Stop the running Dolt SQL server.`,
	RunE:  runDoltStop,
}

var doltRestartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Restart the Dolt server (kills imposters)",
	Long: `Stop the Dolt SQL server, kill any imposter servers on the configured port,
and start the correct server from the configured data directory.

This is the nuclear option for recovering from a hijacked port — when another
process (e.g., bd's embedded Dolt server) has taken over the port with a
different data directory, serving empty/wrong databases.

Steps:
  1. Stop the tracked server (via PID file)
  2. Kill any other dolt sql-server on the configured port (imposters)
  3. Start the correct server from .dolt-data/`,
	RunE: runDoltRestart,
}

var doltKillImpostersCmd = &cobra.Command{
	Use:   "kill-imposters",
	Short: "Kill dolt servers hijacking this workspace's port",
	Long: `Find and kill any dolt sql-server that holds this workspace's configured
port but serves from a different data directory (an "imposter").

This is safe to run at any time. It only kills servers that are:
  1. Listening on the same port as this workspace's Dolt config
  2. Serving from a data directory OTHER than this workspace's .dolt-data/

It never kills the workspace's own legitimate Dolt server.

Examples:
  gt dolt kill-imposters          # Kill imposters on configured port
  gt dolt kill-imposters --dry-run # Preview without killing`,
	RunE: runDoltKillImposters,
}

var doltKillImpostersDry bool

var doltStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show Dolt server status",
	Long:  `Show the current status of the Dolt SQL server.`,
	RunE:  runDoltStatus,
}

var doltLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "View Dolt server logs",
	Long:  `View the Dolt server log file.`,
	RunE:  runDoltLogs,
}

var doltDumpCmd = &cobra.Command{
	Use:   "dump",
	Short: "Collect non-fatal Dolt server diagnostics",
	Long: `Collect a non-fatal Dolt diagnostic snapshot for incident response.

This command does not send SIGQUIT. Dolt 1.86.5 terminates sql-server after
SIGQUIT, so default diagnostics gather process metadata and recent logs only.`,
	RunE: runDoltDump,
}

var doltSQLCmd = &cobra.Command{
	Use:   "sql",
	Short: "Open Dolt SQL shell",
	Long: `Open an interactive SQL shell to the Dolt database.

Works in both embedded mode (no server) and server mode.
For multi-client access, start the server first with 'gt dolt start'.`,
	RunE: runDoltSQL,
}

var doltInitRigCmd = &cobra.Command{
	Use:   "init-rig <name>",
	Short: "Initialize a new rig database",
	Long: `Initialize a new rig database in the Dolt data directory.

Each rig (e.g., gastown, beads) gets its own database that will be
served by the Dolt server. The rig name becomes the database name
when connecting via MySQL protocol.

Example:
  gt dolt init-rig gastown
  gt dolt init-rig beads`,
	Args: cobra.ExactArgs(1),
	RunE: runDoltInitRig,
}

var doltListCmd = &cobra.Command{
	Use:   "list",
	Short: "List available rig databases",
	Long:  `List all rig databases in the Dolt data directory.`,
	RunE:  runDoltList,
}

var doltMigrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Migrate existing dolt databases to centralized data directory",
	Long: `Migrate existing dolt databases from .beads/dolt/ locations to the
centralized .dolt-data/ directory structure.

This command will:
1. Detect existing dolt databases in .beads/dolt/ directories
2. Move them to .dolt-data/<rigname>/
3. Remove the old empty directories

Use --dry-run to preview what would be moved (source/target paths and sizes)
without making any changes.

After migration, start the server with 'gt dolt start'.`,
	RunE: runDoltMigrate,
}

var doltFixMetadataCmd = &cobra.Command{
	Use:   "fix-metadata",
	Short: "Update metadata.json in all rig .beads directories",
	Long: `Ensure all rig .beads/metadata.json files have correct Dolt server configuration.

This fixes the split-brain problem where bd falls back to local embedded databases
instead of connecting to the centralized Dolt server. It updates metadata.json with:
  - backend: "dolt"
  - dolt_mode: "server"
  - dolt_database: "<rigname>"

Safe to run multiple times (idempotent). Preserves any existing fields in metadata.json.`,
	RunE: runDoltFixMetadata,
}

var doltRecoverCmd = &cobra.Command{
	Use:   "recover",
	Short: "Detect and recover from Dolt read-only state",
	Long: `Detect if the Dolt server is in read-only mode and attempt recovery.

When the Dolt server enters read-only mode (e.g., from concurrent write
contention on the storage manifest), all write operations fail. This command:

  1. Probes the server to detect read-only state
  2. Stops the server if read-only
  3. Restarts the server
  4. Verifies recovery with a write probe

If the server is already writable, this is a no-op.

The daemon performs this check automatically every 30 seconds. Use this command
for immediate recovery without waiting for the daemon's health check loop.`,
	RunE: runDoltRecover,
}

var doltSyncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Push Dolt databases to DoltHub remotes",
	Long: `Push all local Dolt databases to their configured DoltHub remotes.

When the Dolt server is running, pushes via SQL (CALL DOLT_PUSH) so the server
stays up and running agents are not disrupted. Falls back to CLI push (which
requires stopping the server) only when the server is not running.

This command automates the tedious process of pushing each database individually:
  1. Optionally purges closed ephemeral beads (--gc)
  2. Iterates databases in .dolt-data/
  3. For each database with a configured remote, pushes via SQL or CLI
  4. Reports success/failure per database

Use --db to sync a single database, --dry-run to preview, or --force for force-push.
Use --gc to purge closed ephemeral beads (wisps, convoys) before pushing.

Examples:
  gt dolt sync                # Push all databases with remotes
  gt dolt sync --dry-run      # Preview what would be pushed
  gt dolt sync --db gastown   # Push only the gastown database
  gt dolt sync --force        # Force-push all databases
  gt dolt sync --gc           # Purge closed ephemeral beads, then push
  gt dolt sync --gc --dry-run # Preview purge + push without changes`,
	RunE: runDoltSync,
}

var doltPullCmd = &cobra.Command{
	Use:   "pull",
	Short: "Pull Dolt databases from remotes",
	Long: `Pull all local Dolt databases from their configured remotes.

When the Dolt server is running, pulls via SQL (CALL DOLT_PULL) so the server
stays up and avoids lock contention. Falls back to CLI pull only when the server
is not running.

This is the safe way to pull databases — using 'dolt pull' directly on a database
that the server is managing can cause exclusive lock contention and prevent
server restarts.

Examples:
  gt dolt pull                # Pull all databases with remotes
  gt dolt pull --db xtm       # Pull only the xtm database
  gt dolt pull --dry-run      # Preview what would be pulled`,
	RunE: runDoltPull,
}

var doltCleanupCmd = &cobra.Command{
	Use:   "cleanup",
	Short: "Remove orphaned databases from .dolt-data/",
	Long: `Detect and remove orphaned databases from the .dolt-data/ directory.

An orphaned database is one that exists in .dolt-data/ but is not referenced
by any rig's metadata.json. These are typically left over from partial setups,
renamed databases, or failed migrations.

Use --dry-run to preview what would be removed without making changes.

Examples:
  gt dolt cleanup             # Remove all orphaned databases
  gt dolt cleanup --dry-run   # Preview what would be removed`,
	RunE: runDoltCleanup,
}

var doltRollbackCmd = &cobra.Command{
	Use:   "rollback [backup-dir]",
	Short: "Restore .beads directories from a migration backup",
	Long: `Roll back a migration by restoring .beads directories from a backup.

If no backup directory is specified, the most recent migration-backup-TIMESTAMP/
directory is used automatically.

This command will:
1. Stop the Dolt server if running
2. Find the specified (or most recent) backup
3. Restore all .beads directories from the backup
4. Reset metadata.json files to their pre-migration state
5. Validate the restored state with bd list

The backup directory is expected to be in the format created by the migration
formula's backup step (migration-backup-YYYYMMDD-HHMMSS/).`,
	Args: cobra.MaximumNArgs(1),
	RunE: runDoltRollback,
}

var doltMigrateWispsCmd = &cobra.Command{
	Use:   "migrate-wisps",
	Short: "Migrate agent beads from issues to wisps table",
	Long: `Create the wisps table infrastructure and migrate existing agent beads.

This command:
1. Creates the wisps table (dolt_ignored, same schema as issues)
2. Creates auxiliary tables (wisp_labels, wisp_comments, wisp_events, wisp_dependencies)
3. Copies agent beads (issue_type='agent') from issues to wisps
4. Copies associated labels, comments, events, and dependencies
5. Closes the originals in the issues table

Idempotent — safe to run multiple times. Use --dry-run to preview.

After migration, 'bd mol wisp list' will work and agent lifecycle
(spawn, sling, work, done, nuke, respawn) uses the wisps table.`,
	RunE: runDoltMigrateWisps,
}

var (
	doltLogLines     int
	doltLogFollow    bool
	doltMigrateDry   bool
	doltCleanupDry   bool
	doltCleanupForce bool

	doltMigrateWispsDry bool
	doltMigrateWispsDB  string
	doltRollbackDry     bool
	doltRollbackList    bool
	doltSyncDry         bool
	doltSyncForce       bool
	doltSyncDB          string
	doltSyncGC          bool
	doltPullDry         bool
	doltPullDB          string
)

func init() {
	doltCmd.AddCommand(doltInitCmd)
	doltCmd.AddCommand(doltStartCmd)
	doltCmd.AddCommand(doltStopCmd)
	doltCmd.AddCommand(doltRestartCmd)
	doltCmd.AddCommand(doltKillImpostersCmd)
	doltCmd.AddCommand(doltStatusCmd)
	doltCmd.AddCommand(doltLogsCmd)
	doltCmd.AddCommand(doltDumpCmd)
	doltCmd.AddCommand(doltSQLCmd)
	doltCmd.AddCommand(doltInitRigCmd)
	doltCmd.AddCommand(doltListCmd)
	doltCmd.AddCommand(doltMigrateCmd)
	doltCmd.AddCommand(doltFixMetadataCmd)
	doltCmd.AddCommand(doltRecoverCmd)
	doltCmd.AddCommand(doltCleanupCmd)
	doltCmd.AddCommand(doltRollbackCmd)
	doltCmd.AddCommand(doltSyncCmd)
	doltCmd.AddCommand(doltPullCmd)
	doltCmd.AddCommand(doltMigrateWispsCmd)

	doltKillImpostersCmd.Flags().BoolVar(&doltKillImpostersDry, "dry-run", false, "Preview without killing")

	doltCleanupCmd.Flags().BoolVar(&doltCleanupDry, "dry-run", false, "Preview what would be removed without making changes")
	doltCleanupCmd.Flags().BoolVar(&doltCleanupForce, "force", false, "Remove databases even if they have user tables")
	doltLogsCmd.Flags().IntVarP(&doltLogLines, "lines", "n", 50, "Number of lines to show")
	doltLogsCmd.Flags().BoolVarP(&doltLogFollow, "follow", "f", false, "Follow log output")

	doltMigrateCmd.Flags().BoolVar(&doltMigrateDry, "dry-run", false, "Preview what would be migrated without making changes")

	doltRollbackCmd.Flags().BoolVar(&doltRollbackDry, "dry-run", false, "Show what would be restored without making changes")
	doltRollbackCmd.Flags().BoolVar(&doltRollbackList, "list", false, "List available backups and exit")

	doltSyncCmd.Flags().BoolVar(&doltSyncDry, "dry-run", false, "Preview what would be pushed without pushing")
	doltSyncCmd.Flags().BoolVar(&doltSyncForce, "force", false, "Force-push to remotes")
	doltSyncCmd.Flags().StringVar(&doltSyncDB, "db", "", "Sync a single database instead of all")
	doltSyncCmd.Flags().BoolVar(&doltSyncGC, "gc", false, "Purge closed ephemeral beads before push (requires bd purge)")

	doltPullCmd.Flags().BoolVar(&doltPullDry, "dry-run", false, "Preview what would be pulled without pulling")
	doltPullCmd.Flags().StringVar(&doltPullDB, "db", "", "Pull a single database instead of all")

	doltMigrateWispsCmd.Flags().BoolVar(&doltMigrateWispsDry, "dry-run", false, "Preview what would be migrated without making changes")
	doltMigrateWispsCmd.Flags().StringVar(&doltMigrateWispsDB, "db", "", "Target database (default: auto-detect from rig)")

	rootCmd.AddCommand(doltCmd)
}

func runDoltStart(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("Dolt server is remote (%s) — start/stop managed externally", config.HostPort())
	}

	// Check for databases before starting — user-facing guard for manual starts.
	// Internal callers (install, migrate) may legitimately start with an empty
	// data dir and create databases afterward via bd init.
	databases, _ := doltserver.ListDatabases(townRoot)
	if len(databases) == 0 {
		return fmt.Errorf("no databases found in %s\nInitialize with: gt dolt init-rig <name>", config.DataDir)
	}

	if err := doltserver.Start(townRoot); err != nil {
		return err
	}

	// Get state for display
	state, _ := doltserver.LoadState(townRoot)

	fmt.Printf("%s Dolt server started (PID %d, port %d)\n",
		style.Bold.Render("✓"), state.PID, config.Port)
	fmt.Printf("  Data dir: %s\n", state.DataDir)
	fmt.Printf("  Databases: %s\n", style.Dim.Render(strings.Join(state.Databases, ", ")))
	fmt.Printf("  Connection: %s\n", style.Dim.Render(doltserver.GetConnectionString(townRoot)))

	// Verify all filesystem databases are actually served by the SQL server.
	// Use retry since Start() only waits 500ms — DBs may still be loading.
	served, missing, verifyErr := doltserver.VerifyDatabasesWithRetry(townRoot, 5)
	if verifyErr != nil {
		fmt.Printf("  %s Could not verify databases: %v\n", style.Dim.Render("⚠"), verifyErr)
	} else if len(missing) > 0 {
		fmt.Printf("\n%s Some databases exist on disk but are NOT served:\n", style.Bold.Render("⚠"))
		for _, db := range missing {
			fmt.Printf("  - %s\n", db)
		}
		fmt.Printf("\n  Served: %v\n", served)
		fmt.Printf("  This usually means the database has a stale manifest.\n")
		fmt.Printf("  Try: %s\n", style.Dim.Render("cd \"$GT_ROOT\"/.dolt-data/<db> && dolt fsck --repair"))
	} else {
		fmt.Printf("  %s All %d databases verified\n", style.Bold.Render("✓"), len(served))
	}

	return nil
}

func runDoltKillImposters(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("Dolt server is remote — imposter detection requires local server")
	}

	conflictPID, conflictDataDir := doltserver.CheckPortConflict(townRoot)
	if conflictPID == 0 {
		fmt.Printf("%s No imposters found on port %d\n", style.Bold.Render("✓"), config.Port)
		return nil
	}

	fmt.Printf("Found imposter dolt server:\n")
	fmt.Printf("  PID:      %d\n", conflictPID)
	fmt.Printf("  Data-dir: %s\n", conflictDataDir)
	fmt.Printf("  Expected: %s\n", config.DataDir)

	if doltKillImpostersDry {
		fmt.Printf("\n%s Dry-run — not killing\n", style.Warning.Render("~"))
		return nil
	}

	if err := doltserver.KillImposters(townRoot); err != nil {
		return fmt.Errorf("killing imposter: %w", err)
	}
	fmt.Printf("%s Imposter killed (PID %d)\n", style.Bold.Render("✓"), conflictPID)
	return nil
}

func runDoltStop(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("Dolt server is remote (%s) — start/stop managed externally", config.HostPort())
	}

	_, pid, _ := doltserver.IsRunning(townRoot)

	// Probe BEFORE stopping: /proc/<pid>/cgroup dies with the process, and the
	// unit's MainPID changes the moment it restarts. Remember what it finds —
	// this command is about to destroy the evidence, and the start that follows
	// has to route back through the same unit. (gt-cru5)
	sup := doltserver.ObserveSupervisor(townRoot, pid)

	if err := doltserver.Stop(townRoot); err != nil {
		return err
	}

	if sup.AutoRestarts() {
		fmt.Print(supervisedStopNotice(pid, sup))
		return nil
	}

	fmt.Printf("%s Dolt server stopped (was PID %d)\n", style.Bold.Render("✓"), pid)
	return nil
}

// supervisedStopNotice replaces the "stopped" line when a supervisor is about to
// undo the stop.
//
// Claiming success here is not a cosmetic problem. On 2026-08-18 an operator ran
// `gt down` three times in six minutes because each run reported Dolt stopped
// while gt-dolt.service (Restart=always) brought it back 5s later with a new PID
// — the changed PID reading as "something is wrong" rather than as "gt cannot
// stop this" (gt-09e4).
func supervisedStopNotice(pid int, sup *doltserver.Supervisor) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s Signalled Dolt server (PID %d) — but it is NOT stopped.\n\n",
		style.Warning.Render("!"), pid)
	fmt.Fprintf(&b, "  This server is supervised by %s.\n", sup.Describe())
	b.WriteString("  gt signalled the process; the supervisor starts a replacement on the same\n")
	b.WriteString("  data directory seconds from now, with a new PID. gt does not manage that\n")
	b.WriteString("  unit — running this command again will not change the outcome.\n\n")
	fmt.Fprintf(&b, "  To stop the server for real:\n      %s\n\n", sup.StopCommand())
	b.WriteString("  That takes the town's data plane — beads, mail, identity — offline for\n")
	b.WriteString("  every agent. Bring it back with:\n")
	fmt.Fprintf(&b, "      %s\n", sup.StartCommand())

	return b.String()
}

// printSupervisorStatus reports who owns the Dolt process and whether that owner
// has been quietly replacing it.
//
// Without this, `gt dolt status` describes only the process running right now: a
// healthy server, a fresh PID, a short uptime. On 2026-08-18 Dolt exited twice in
// 75s and systemd restarted it 5s later each time; two agents independently
// escalated "connection refused, then a new PID, and nobody restarted it" and
// neither could attribute it, because the restarter never appears in any gt
// surface and the unit's start limit is off so it is never marked failed
// (hq-njloj, hq-69g3w, gt-qiok). The restart count is the one fact that turns
// "mystery restart" into "the server died and systemd replaced it".
func printSupervisorStatus(sup *doltserver.Supervisor) {
	if sup == nil || sup.Unit == "" {
		return
	}
	fmt.Printf("  Supervisor: %s\n", sup.Describe())

	notices := sup.RestartNotice()
	if len(notices) == 0 {
		return
	}
	fmt.Printf("\n  %s\n", style.Bold.Render("Restart History:"))
	for _, notice := range notices {
		for i, line := range strings.Split(notice, "\n") {
			if i == 0 {
				fmt.Printf("    %s %s\n", style.Warning.Render("!"), line)
			} else {
				fmt.Printf("      %s\n", line)
			}
		}
	}
}

func runDoltRestart(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("Dolt server is remote (%s) — start/stop managed externally", config.HostPort())
	}

	// Step 1: Stop tracked server (if running)
	running, pid, _ := doltserver.IsRunning(townRoot)

	// A gt-side restart under a reviving supervisor is not a restart, it is a
	// port fight: gt rebinds within a second, then the unit's own restart timer
	// fires and its replacement cannot bind. With StartLimitIntervalSec=0 — what
	// this town's unit sets — systemd retries that failure forever. Refuse and
	// hand over the command that restarts the thing actually in charge (gt-09e4).
	if sup := doltserver.DetectSupervisor(pid); sup.AutoRestarts() {
		return fmt.Errorf(`Dolt server is supervised by %s — gt cannot restart it

gt would stop the process and immediately rebind port %d. The supervisor's own
restart then fires and fails to bind, and retries on a loop, while the server gt
started runs unsupervised.

Restart through the supervisor instead:
    %s`,
			sup.Describe(), config.Port,
			strings.Replace(sup.StopCommand(), " stop ", " restart ", 1))
	}

	if running {
		fmt.Printf("Stopping Dolt server (PID %d)...\n", pid)
		if err := doltserver.Stop(townRoot); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: stop failed: %v (continuing with imposter kill)\n", err)
		} else {
			fmt.Printf("%s Stopped\n", style.Bold.Render("✓"))
		}
	}

	// Step 2: Kill any imposters on the port
	fmt.Println("Checking for imposter servers...")
	if err := doltserver.KillImposters(townRoot); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: imposter kill failed: %v\n", err)
	}

	// Brief pause to let port be released
	time.Sleep(500 * time.Millisecond)

	// Step 3: Check for databases before starting
	databases, _ := doltserver.ListDatabases(townRoot)
	if len(databases) == 0 {
		return fmt.Errorf("no databases found in %s\nInitialize with: gt dolt init-rig <name>", config.DataDir)
	}

	// Step 4: Start the correct server
	fmt.Println("Starting Dolt server...")
	if err := doltserver.Start(townRoot); err != nil {
		return fmt.Errorf("restart failed: %w", err)
	}

	// Display status (same as gt dolt start)
	state, _ := doltserver.LoadState(townRoot)

	fmt.Printf("%s Dolt server restarted (PID %d, port %d)\n",
		style.Bold.Render("✓"), state.PID, config.Port)
	fmt.Printf("  Data dir: %s\n", state.DataDir)
	fmt.Printf("  Databases: %s\n", style.Dim.Render(strings.Join(state.Databases, ", ")))
	fmt.Printf("  Connection: %s\n", style.Dim.Render(doltserver.GetConnectionString(townRoot)))

	// Verify databases
	served, missing, verifyErr := doltserver.VerifyDatabasesWithRetry(townRoot, 5)
	if verifyErr != nil {
		fmt.Printf("  %s Could not verify databases: %v\n", style.Dim.Render("⚠"), verifyErr)
	} else if len(missing) > 0 {
		fmt.Printf("\n%s Some databases exist on disk but are NOT served:\n", style.Bold.Render("⚠"))
		for _, db := range missing {
			fmt.Printf("  - %s\n", db)
		}
		fmt.Printf("\n  Served: %v\n", served)
	} else {
		fmt.Printf("  %s All %d databases verified\n", style.Bold.Render("✓"), len(served))
	}

	return nil
}

func runDoltStatus(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	running, pid, err := doltserver.IsRunning(townRoot)
	if err != nil {
		return fmt.Errorf("checking server status: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)

	if config.IsRemote() {
		if running {
			fmt.Printf("%s Dolt server is %s (remote: %s)\n",
				style.Bold.Render("●"),
				style.Bold.Render("reachable"),
				config.HostPort())
		} else {
			fmt.Printf("%s Dolt server is %s (remote: %s)\n",
				style.Dim.Render("○"),
				"not reachable",
				config.HostPort())
		}
		fmt.Printf("  Connection: %s\n", doltserver.GetConnectionString(townRoot))
		printBeadsRuntimeConfig(townRoot)
		if running {
			metrics := doltserver.GetHealthMetrics(townRoot)
			fmt.Printf("\n  %s\n", style.Bold.Render("Resource Metrics:"))
			fmt.Printf("    Query latency: %v\n", metrics.QueryLatency.Round(time.Millisecond))
			fmt.Printf("    Connections:   %d / %d (%.0f%%)\n",
				metrics.Connections, metrics.MaxConnections, metrics.ConnectionPct)
			if metrics.ReadOnly {
				fmt.Printf("\n  %s %s\n",
					style.Bold.Render("!!!"),
					style.Bold.Render("SERVER IS READ-ONLY — contact the remote server admin"))
			}
		}
		return nil
	}

	if running {
		fmt.Printf("%s Dolt server is %s (PID %d)\n",
			style.Bold.Render("●"),
			style.Bold.Render("running"),
			pid)

		// Load state for more details
		state, err := doltserver.LoadState(townRoot)
		if err == nil && !state.StartedAt.IsZero() {
			// Prefer the live process's start time over gt's record of when it
			// launched Dolt — the record survives a restart by anyone else and
			// would overstate uptime (gt-pdd).
			startedAt, _ := doltserver.ResolveStartedAt(pid, state.StartedAt)
			fmt.Printf("  Started: %s (up %s)\n",
				startedAt.Format("2006-01-02 15:04:05"),
				time.Since(startedAt).Round(time.Second))
			fmt.Printf("  Port: %d\n", state.Port)
			fmt.Printf("  Data dir: %s\n", state.DataDir)
			if len(state.Databases) > 0 {
				owners := doltserver.CollectDatabaseOwners(townRoot)
				fmt.Printf("  Databases:\n")
				for _, db := range state.Databases {
					if owner, ok := owners[db]; ok {
						fmt.Printf("    - %-20s (%s)\n", db, owner)
					} else {
						fmt.Printf("    - %s\n", db)
					}
				}
			}
			fmt.Printf("  Connection: %s\n", doltserver.GetConnectionString(townRoot))
			printBeadsRuntimeConfig(townRoot)
		}

		// Who owns this process, and has it been silently replaced? Printed with
		// the process facts above rather than under Resource Metrics: it qualifies
		// the PID and the uptime, both of which describe only the CURRENT server.
		printSupervisorStatus(doltserver.ObserveSupervisor(townRoot, pid))

		// Resource metrics
		metrics := doltserver.GetHealthMetrics(townRoot)
		fmt.Printf("\n  %s\n", style.Bold.Render("Resource Metrics:"))
		fmt.Printf("    Query latency: %v\n", metrics.QueryLatency.Round(time.Millisecond))
		fmt.Printf("    Connections:   %d / %d (%.0f%%)\n",
			metrics.Connections, metrics.MaxConnections, metrics.ConnectionPct)
		fmt.Printf("    Disk usage:    %s\n", metrics.DiskUsageHuman)
		if metrics.ReadOnly {
			fmt.Printf("\n  %s %s\n",
				style.Bold.Render("!!!"),
				style.Bold.Render("SERVER IS READ-ONLY — run 'gt dolt recover' to restart"))
		}

		// Verify all filesystem databases are actually served.
		_, missing, verifyErr := doltserver.VerifyDatabases(townRoot)
		if verifyErr != nil {
			fmt.Printf("\n  %s Database verification failed: %v\n", style.Bold.Render("!"), verifyErr)
		} else if len(missing) > 0 {
			fmt.Printf("\n  %s %s\n", style.Bold.Render("!!!"),
				style.Bold.Render("MISSING DATABASES — exist on disk but not served:"))
			for _, db := range missing {
				fmt.Printf("    - %s\n", db)
			}
			fmt.Printf("  Try: cd \"$GT_ROOT\"/.dolt-data/<db> && dolt fsck --repair\n")
		}

		// Check for orphaned databases
		orphans, orphanErr := doltserver.FindOrphanedDatabases(townRoot)
		if orphanErr == nil && len(orphans) > 0 {
			fmt.Printf("\n  %s %d orphaned database(s) (not referenced by any rig):\n",
				style.Bold.Render("!"), len(orphans))
			for _, o := range orphans {
				fmt.Printf("    - %s (%s%s)\n", o.Name, formatBytes(o.SizeBytes), orphanCreatedSuffix(o))
			}
			allDBs, _ := doltserver.ListDatabases(townRoot)
			fmt.Println()
			fmt.Print(orphanCleanupGuidance(townRoot, orphans, allDBs, "    "))
		}

		if len(metrics.Warnings) > 0 {
			fmt.Printf("\n  %s\n", style.Bold.Render("Warnings:"))
			for _, w := range metrics.Warnings {
				fmt.Printf("    %s %s\n", style.Bold.Render("!"), w)
			}
		}
	} else {
		fmt.Printf("%s Dolt server is %s\n",
			style.Dim.Render("○"),
			"not running")

		// List available databases
		databases, _ := doltserver.ListDatabases(townRoot)
		if len(databases) == 0 {
			fmt.Printf("\n%s No rig databases found in %s\n",
				style.Bold.Render("!"),
				config.DataDir)
			fmt.Printf("  Initialize with: %s\n", style.Dim.Render("gt dolt init-rig <name>"))
		} else {
			fmt.Printf("\nAvailable databases in %s:\n", config.DataDir)
			owners := doltserver.CollectDatabaseOwners(townRoot)
			for _, db := range databases {
				if owner, ok := owners[db]; ok {
					fmt.Printf("  - %-20s (%s)\n", db, owner)
				} else {
					fmt.Printf("  - %s\n", db)
				}
			}
			fmt.Printf("\nStart with: %s\n", style.Dim.Render("gt dolt start"))
		}
	}

	return nil
}

type beadsRuntimeConfig struct {
	Source   string
	Database string
	Host     string
	Port     int
}

func currentBeadsRuntimeConfig() (beadsRuntimeConfig, bool) {
	cwd, err := os.Getwd()
	if err != nil {
		return beadsRuntimeConfig{}, false
	}
	return readBeadsRuntimeConfig(beads.ResolveBeadsDir(cwd))
}

func readBeadsRuntimeConfig(beadsDir string) (beadsRuntimeConfig, bool) {
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return beadsRuntimeConfig{}, false
	}

	var metadata struct {
		Backend        string `json:"backend"`
		Database       string `json:"database"`
		DoltMode       string `json:"dolt_mode"`
		DoltDatabase   string `json:"dolt_database"`
		DoltServerHost string `json:"dolt_server_host"`
		DoltServerPort int    `json:"dolt_server_port"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		return beadsRuntimeConfig{}, false
	}
	if metadata.Backend != "dolt" || metadata.DoltMode != "server" {
		return beadsRuntimeConfig{}, false
	}

	host := metadata.DoltServerHost
	if host == "" {
		host = "127.0.0.1"
	}
	port := metadata.DoltServerPort
	if port == 0 {
		if data, err := os.ReadFile(filepath.Join(beadsDir, "dolt-server.port")); err == nil {
			if parsed, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && parsed > 0 {
				port = parsed
			}
		}
	}
	if port == 0 {
		port = doltserver.DefaultPort
	}
	database := metadata.DoltDatabase
	if database == "" {
		database = metadata.Database
	}

	return beadsRuntimeConfig{
		Source:   metadataPath,
		Database: database,
		Host:     host,
		Port:     port,
	}, true
}

func printBeadsRuntimeConfig(townRoot string) {
	cfg, ok := currentBeadsRuntimeConfig()
	if !ok {
		return
	}
	parts := []string{"server metadata"}
	if cfg.Database != "" {
		parts = append(parts, "database "+cfg.Database)
	}
	if cfg.Host != "" && cfg.Port > 0 {
		parts = append(parts, netJoinHostPort(cfg.Host, cfg.Port))
	}
	if cfg.Source != "" {
		parts = append(parts, "from "+cfg.Source)
	}
	fmt.Printf("  Beads client: %s\n", strings.Join(parts, ", "))
	if hint := beadsScopeHint(cfg.Database, townRoot); hint != "" {
		fmt.Print(hint)
	}
}

func beadsScopeHint(database, townRoot string) string {
	if database != "hq" {
		return ""
	}

	quoted := gtconfig.ShellQuote(townRoot)
	return fmt.Sprintf("    Gas Town town beads use database hq. Use `bd -C %s <cmd>` to read hq-* beads; do not use `bd --global`, which targets Beads' beads_global database.\n"+
		"    `bd -C` only sets BEADS_DIR — it does not chdir, and your maintainer/contributor role is still read from the current directory. To write hq-* beads, `cd %s` first.\n", quoted, quoted)
}

func netJoinHostPort(host string, port int) string {
	return host + ":" + strconv.Itoa(port)
}

func runDoltLogs(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)

	if _, err := os.Stat(config.LogFile); os.IsNotExist(err) {
		return fmt.Errorf("no log file found at %s", config.LogFile)
	}

	if doltLogFollow {
		// Use tail -f for following
		tailCmd := exec.Command("tail", "-f", config.LogFile)
		tailCmd.Stdout = os.Stdout
		tailCmd.Stderr = os.Stderr
		return tailCmd.Run()
	}

	// Use tail -n for last N lines
	tailCmd := exec.Command("tail", "-n", strconv.Itoa(doltLogLines), config.LogFile)
	tailCmd.Stdout = os.Stdout
	tailCmd.Stderr = os.Stderr
	return tailCmd.Run()
}

func runDoltDump(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	running, pid, err := doltserver.IsRunning(townRoot)
	if err != nil {
		return fmt.Errorf("checking server status: %w", err)
	}
	if !running {
		return fmt.Errorf("Dolt server is not running — nothing to dump")
	}

	config := doltserver.DefaultConfig(townRoot)

	fmt.Printf("Dolt diagnostic snapshot (non-fatal)\n")
	fmt.Printf("  Live PID:   %d\n", pid)
	if started, ok := doltserver.ProcessStartTime(pid); ok {
		fmt.Printf("  Live start: %s (up %s)\n",
			started.Format("2006-01-02 15:04:05"),
			time.Since(started).Round(time.Second))
	}
	fmt.Printf("  Port:       %d\n", config.Port)
	fmt.Printf("  Data dir:   %s\n", config.DataDir)
	fmt.Printf("  Log file:   %s\n", config.LogFile)
	fmt.Printf("  Connection: %s\n", doltserver.GetConnectionString(townRoot))

	if info, err := doltserver.ReadSQLServerInfo(townRoot); err == nil {
		fmt.Printf("  SQL metadata: %s\n", info.Path)
		fmt.Printf("    PID:       %d\n", info.PID)
		fmt.Printf("    Port:      %d\n", info.Port)
		if info.ServerID != "" {
			fmt.Printf("    Server ID: %s\n", info.ServerID)
		}
	} else {
		fmt.Printf("  SQL metadata: unavailable (%v)\n", err)
	}

	if state, err := doltserver.LoadState(townRoot); err == nil && state.PID > 0 {
		fmt.Printf("  Daemon state: %s\n", doltserver.StateFile(townRoot))
		fmt.Printf("    PID:       %d", state.PID)
		if state.PID != pid {
			fmt.Printf(" (stale; live PID is %d)", pid)
		}
		fmt.Println()
		if !state.StartedAt.IsZero() {
			fmt.Printf("    Started:   %s", state.StartedAt.Format("2006-01-02 15:04:05"))
			// The recorded start time is when gt launched Dolt. If the live
			// process started later, someone else restarted the server.
			if started, ok := doltserver.ProcessStartTime(pid); ok && started.Sub(state.StartedAt).Abs() > time.Minute {
				fmt.Printf(" (stale; live process started %s)", started.Format("2006-01-02 15:04:05"))
			}
			fmt.Println()
		}
		if state.DataDir != "" {
			fmt.Printf("    Data dir:  %s\n", state.DataDir)
		}
	}

	fmt.Printf("\nRecent Dolt log lines:\n")
	tailCmd := exec.Command("tail", "-n", "200", config.LogFile)
	tailCmd.Stdout = os.Stdout
	tailCmd.Stderr = os.Stderr
	if err := tailCmd.Run(); err != nil {
		fmt.Printf("  (unable to read recent logs: %v)\n", err)
	}

	fmt.Printf("\nNo signal was sent. Do not use kill -QUIT for routine diagnostics unless the Dolt version has been verified not to terminate on SIGQUIT.\n")

	return nil
}

func runDoltSQL(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)

	// Check if server is running - if so, connect via Dolt SQL client
	running, _, _ := doltserver.IsRunning(townRoot)
	if running {
		// Connect to running server using dolt sql client
		// Using --no-tls since server doesn't have TLS configured
		host := config.Host
		if host == "" {
			host = "127.0.0.1"
		}
		sqlArgs := []string{
			"--host", host,
			"--port", strconv.Itoa(config.Port),
			"--user", config.User,
			"--no-tls",
			"sql",
		}
		sqlCmd := exec.Command("dolt", sqlArgs...)
		// GH#2537: Set cmd.Dir to prevent stray .doltcfg/privileges.db in CWD.
		sqlCmd.Dir = config.DataDir
		if config.Password != "" {
			sqlCmd.Env = append(os.Environ(), "DOLT_CLI_PASSWORD="+config.Password)
		}
		sqlCmd.Stdin = os.Stdin
		sqlCmd.Stdout = os.Stdout
		sqlCmd.Stderr = os.Stderr
		return sqlCmd.Run()
	}

	// Server not running - list databases and pick first one for embedded mode
	databases, err := doltserver.ListDatabases(townRoot)
	if err != nil {
		return fmt.Errorf("listing databases: %w", err)
	}

	if len(databases) == 0 {
		return fmt.Errorf("no databases found in %s\nInitialize with: gt dolt init-rig <name>", config.DataDir)
	}

	// Use first database for embedded SQL shell
	dbDir := doltserver.RigDatabaseDir(townRoot, databases[0])
	fmt.Printf("Using database: %s (start server with 'gt dolt start' for multi-database access)\n\n", databases[0])

	sqlCmd := exec.Command("dolt", "sql")
	sqlCmd.Dir = dbDir
	sqlCmd.Stdin = os.Stdin
	sqlCmd.Stdout = os.Stdout
	sqlCmd.Stderr = os.Stderr

	return sqlCmd.Run()
}

func runDoltInitRig(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	rigName := args[0]

	serverWasRunning, created, err := doltserver.InitRig(townRoot, rigName)
	if err != nil {
		return err
	}

	config := doltserver.DefaultConfig(townRoot)
	rigDir := doltserver.RigDatabaseDir(townRoot, rigName)

	if !created {
		fmt.Printf("%s Rig database %q already exists (no-op)\n", style.Bold.Render("✓"), rigName)
		fmt.Printf("  Location: %s\n", rigDir)
		return nil
	}

	fmt.Printf("%s Initialized rig database %q\n", style.Bold.Render("✓"), rigName)
	fmt.Printf("  Location: %s\n", rigDir)
	fmt.Printf("  Data dir: %s\n", config.DataDir)

	if serverWasRunning {
		fmt.Printf("  Server: %s\n", style.Bold.Render("database registered with running server"))
	} else {
		fmt.Printf("\nStart server with: %s\n", style.Dim.Render("gt dolt start"))
	}

	return nil
}

func runDoltInit(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Find workspaces with broken Dolt configuration
	broken, verifyWarning := doltserver.FindBrokenWorkspaces(townRoot)
	if verifyWarning != "" {
		fmt.Printf("  %s %s\n\n", style.Bold.Render("⚠"), verifyWarning)
	}

	// Check for orphaned databases regardless of broken workspaces
	orphans, orphanErr := doltserver.FindOrphanedDatabases(townRoot)

	if len(broken) == 0 {
		// Also check if there are any databases at all
		databases, _ := doltserver.ListDatabases(townRoot)
		if len(databases) == 0 {
			fmt.Println("No Dolt databases found and no workspaces configured for Dolt.")
			fmt.Printf("\nInitialize a rig database with: %s\n", style.Dim.Render("gt dolt init-rig <name>"))
		} else {
			fmt.Printf("%s All workspaces healthy (%d database(s) verified)\n",
				style.Bold.Render("✓"), len(databases))
		}

		// Report orphans even when workspaces are healthy
		if orphanErr == nil && len(orphans) > 0 {
			printOrphanReport(townRoot, orphans, databases)
		}

		return nil
	}

	fmt.Printf("Found %d workspace(s) with broken Dolt configuration:\n\n", len(broken))

	repaired := 0
	for _, ws := range broken {
		if ws.NotServed {
			fmt.Printf("  %s %s: database %q exists on disk but is not served by the running Dolt server\n",
				style.Bold.Render("!"), ws.RigName, ws.ConfiguredDB)
			fmt.Printf("    Try restarting the server: %s\n", style.Dim.Render("gt dolt restart"))
			continue
		}
		fmt.Printf("  %s %s: metadata.json → database %q (missing from .dolt-data/)\n",
			style.Bold.Render("!"), ws.RigName, ws.ConfiguredDB)
		if ws.HasLocalData {
			fmt.Printf("    Local data found at %s\n", style.Dim.Render(ws.LocalDataPath))
		}

		action, err := doltserver.RepairWorkspace(townRoot, ws)
		if err != nil {
			fmt.Printf("    %s Repair failed: %v\n", style.Bold.Render("✗"), err)
			continue
		}

		fmt.Printf("    %s Repaired: %s\n", style.Bold.Render("✓"), action)
		repaired++
	}

	if repaired > 0 {
		fmt.Printf("\n%s Repaired %d/%d workspace(s)\n", style.Bold.Render("✓"), repaired, len(broken))
	}

	// Report orphans after repairs
	if orphanErr == nil && len(orphans) > 0 {
		allDBs, _ := doltserver.ListDatabases(townRoot)
		printOrphanReport(townRoot, orphans, allDBs)
	}

	return nil
}

// printOrphanReport is the orphan section `gt dolt init` prints, in both the
// healthy and the post-repair path. One renderer so the two cannot drift into
// giving different advice about the same databases.
func printOrphanReport(townRoot string, orphans []doltserver.OrphanedDatabase, allDBs []string) {
	fmt.Printf("\n%s %d orphaned database(s) in .dolt-data/ (not referenced by any rig):\n",
		style.Bold.Render("!"), len(orphans))
	for _, o := range orphans {
		fmt.Printf("  - %s (%s%s)\n", o.Name, formatBytes(o.SizeBytes), orphanCreatedSuffix(o))
	}
	fmt.Println()
	fmt.Print(orphanCleanupGuidance(townRoot, orphans, allDBs, "  "))
}

// orphanCleanupGlobs are the directory patterns that match test-pollution
// databases in .dolt-data/. They are deliberately narrow: production databases
// (hq, the rig databases) match none of them.
const orphanCleanupGlobs = "testdb_* beads_t* beads_pt* beads_vr* doctest_* doctortest_*"

// filesystemCleanupRemedy renders the operator instructions printed when there
// are too many orphans for SQL-based cleanup to finish in reasonable time.
//
// gt never executes these lines — they are instruction text — but an operator
// following them literally does execute them, so they have to be correct for
// the machine they are printed on. Two things they must get right (gt-xvwu):
//
//   - The stop step must stop whatever will otherwise restart the server. Under
//     a systemd unit with Restart=always, `gt dolt stop` signals the process and
//     the supervisor starts a new one on the same data directory ~5s later, so
//     the rm then runs against a LIVE server — exactly the filesystem
//     interference that corrupts Dolt databases irrecoverably.
//   - The reassurance must say what it actually covers. "These globs match no
//     production data" is a claim about which directories are deleted; it says
//     nothing about whether the server is down, which is the variable that
//     decides whether the deletion corrupts anything.
func filesystemCleanupRemedy(townRoot string, sup *doltserver.Supervisor) string {
	var b strings.Builder

	b.WriteString("  Instead, stop the server and clean the filesystem.\n\n")

	if desc := sup.Describe(); desc != "" {
		b.WriteString(fmt.Sprintf("  ! This server is supervised by %s.\n", desc))
		if sup.AutoRestarts() {
			b.WriteString("    `gt dolt stop` would only signal the process — the supervisor starts\n")
			b.WriteString("    a new server on the same data directory seconds later. Stop the unit.\n")
		}
		b.WriteString("\n")
	}

	b.WriteString(fmt.Sprintf("    %s\n", sup.StopCommand()))
	b.WriteString("    gt dolt status    # REQUIRED: must say \"not running\" before you delete\n")
	b.WriteString(fmt.Sprintf("    cd %s/.dolt-data && rm -rf %s\n", townRoot, orphanCleanupGlobs))
	b.WriteString(fmt.Sprintf("    %s\n\n", sup.StartCommand()))

	b.WriteString("  Those globs match only orphan test databases — they hold no production\n")
	b.WriteString("  data, and the databases gt found are listed above. That is not a claim\n")
	b.WriteString("  that the rm is safe at any moment: deleting a database directory while\n")
	b.WriteString("  any Dolt server still holds it open can corrupt data unrecoverably.\n")
	b.WriteString("  Verify the server is stopped first — do not skip the status check.\n")

	return b.String()
}

// staleManifestRemedy renders the operator instructions printed when migration
// finishes but some databases on disk are not served, which usually means a
// stale manifest that `dolt fsck --repair` rewrites.
//
// Same hazard as filesystemCleanupRemedy, same reason (gt-4ruo, sibling of
// gt-xvwu): `dolt fsck --repair` WRITES to the database directory, so the stop
// step has to stop whatever would otherwise put a live server back on that
// directory. Under a systemd unit with Restart=always, `gt dolt stop` signals
// the process and the supervisor starts a replacement ~5s later — the operator
// then repairs a database a running server holds open.
//
// dataDir is the town's .dolt-data directory; missing is the set of databases
// that exist there but are not served. Naming each one beats a `<db>`
// placeholder: the operator copies a path instead of composing one, and a
// mistyped path under `fsck --repair` is a write to the wrong database.
func staleManifestRemedy(dataDir string, missing []string, sup *doltserver.Supervisor) string {
	var b strings.Builder

	b.WriteString("  This usually means the database has a stale manifest from migration.\n")
	b.WriteString("  To fix:\n\n")

	if desc := sup.Describe(); desc != "" {
		b.WriteString(fmt.Sprintf("  ! This server is supervised by %s.\n", desc))
		if sup.AutoRestarts() {
			b.WriteString("    `gt dolt stop` would only signal the process — the supervisor starts\n")
			b.WriteString("    a new server on the same data directory seconds later. Stop the unit.\n")
		}
		b.WriteString("\n")
	}

	b.WriteString(fmt.Sprintf("    1. Stop the server:   %s\n", sup.StopCommand()))
	b.WriteString("    2. Verify it is down: gt dolt status    # REQUIRED: must say \"not running\"\n")
	b.WriteString("    3. Repair the DB(s):\n")
	for _, db := range missing {
		b.WriteString(fmt.Sprintf("         cd %s && dolt fsck --repair\n", filepath.Join(dataDir, db)))
	}
	b.WriteString(fmt.Sprintf("    4. Restart:           %s\n\n", sup.StartCommand()))

	b.WriteString("  `dolt fsck --repair` writes to the database directory. Running it while\n")
	b.WriteString("  any Dolt server still holds that database open can corrupt data\n")
	b.WriteString("  unrecoverably. Verify the server is stopped first — do not skip the\n")
	b.WriteString("  status check.\n")

	return b.String()
}

// orphanFixtureBurstWindow is how close together creation times have to be
// before the balk will call them the signature of a single test run. Real
// databases are created when a town or rig is set up, minutes to months apart.
const orphanFixtureBurstWindow = 5 * time.Minute

// cleanupBalk is a refusal `gt dolt cleanup` raises before deleting anything:
// which refusal it is, the operator-facing explanation, and the error the real
// run returns.
type cleanupBalk struct {
	Kind    doltserver.CleanupBalkKind
	Message string
	Err     error
}

// evaluateCleanupBalks returns the first refusal the real run would hit, or nil
// if it would proceed to delete.
//
// The decision is doltserver.EvaluateCleanupBalk's; this only renders it. The
// thresholds used to live here, where internal/doctor could not reach them, and
// `gt doctor --fix` reached the same bulk deletion with no threshold check at
// all. (gt-baj6)
//
// Both balks are evaluated before the --dry-run return so the rehearsal and the
// performance cannot diverge. Previously --dry-run returned first and printed a
// clean deletion list with exit 0 while the real run refused, so the operator
// who did the responsible thing came away with MORE confidence than the one who
// did not — the single failure mode a dry run exists to prevent. (gt-ti84)
func evaluateCleanupBalks(townRoot string, orphans []doltserver.OrphanedDatabase, allDBs []string, force bool) *cleanupBalk {
	switch doltserver.EvaluateCleanupBalk(len(orphans), len(allDBs), force) {
	case doltserver.BalkOrphanRatio:
		return &cleanupBalk{
			Kind:    doltserver.BalkOrphanRatio,
			Message: orphanRatioBalkMessage(orphans, len(allDBs)),
			Err:     fmt.Errorf("refusing to clean %d/%d databases without --force (safety check, gt-xvh)", len(orphans), len(allDBs)),
		}

	case doltserver.BalkTooManyOrphans:
		var b strings.Builder
		fmt.Fprintf(&b, "\n%s Too many orphans (%d) for SQL-based cleanup (max %d).\n",
			style.Bold.Render("!"), len(orphans), doltserver.MaxSQLCleanup)
		b.WriteString("  The server is likely overloaded. SQL cleanup would take hours.\n\n")
		_, pid, _ := doltserver.IsRunning(townRoot)
		b.WriteString(filesystemCleanupRemedy(townRoot, doltserver.DetectSupervisor(pid)))
		return &cleanupBalk{
			Kind:    doltserver.BalkTooManyOrphans,
			Message: b.String(),
			Err:     fmt.Errorf("too many orphans (%d) for SQL cleanup — see instructions above", len(orphans)),
		}
	}

	return nil
}

// orphanCleanupGuidance renders what a REPORTING surface says after it has
// listed orphaned databases: `gt dolt status`, `gt dolt init`, `gt doctor`.
// indent is prefixed to every line so each caller keeps its own layout.
//
// It names no mutating command, which is the whole point. The line it replaced
// was an unconditional "Clean up with: gt dolt cleanup", and the chain a reader
// followed from it was three steps of ordinary-looking tool guidance ending at
// the highest-blast-radius command in the town: status recommends cleanup ->
// cleanup refuses at the orphan ratio and offers --force -> --force is under a
// standing prohibition the reader has no way to know about from here. The
// recommendation was also loudest exactly when it was most dangerous, because
// it fired on every detected orphan, and deletion matters most when detection
// is RIGHT. Correct detection and safe guidance pointed opposite ways. (gt-xhjb)
//
// So this reports and the operator decides. It describes what cleanup would do
// rather than telling anyone to run it, it says up front when cleanup will
// refuse — a fact that was previously discoverable only by running the deletion
// command — and every command it does name is read-only.
func orphanCleanupGuidance(townRoot string, orphans []doltserver.OrphanedDatabase, allDBs []string, indent string) string {
	var b strings.Builder
	line := func(format string, args ...any) {
		fmt.Fprintf(&b, indent+format+"\n", args...)
	}

	// Ask the deletion path itself rather than re-deriving the thresholds, so
	// this cannot describe a refusal cleanup would not raise, or miss one it
	// would. force=false: this describes a plain `gt dolt cleanup`.
	//
	// Cleanup is named in prose and never as a bare invocation, so that the only
	// literal commands anywhere in this text are the two read-only ones below.
	// A reader — or an agent — scanning the output for something to run finds
	// nothing here that deletes.
	switch balk := evaluateCleanupBalks(townRoot, orphans, allDBs, false); {
	case balk == nil:
		line("Cleanup would remove these, skipping any that still hold user tables.")
		line("It has not run.")
	case balk.Kind == doltserver.BalkTooManyOrphans:
		line("Cleanup will REFUSE these: %d orphans is past the %d it can drop by SQL", len(orphans), doltserver.MaxSQLCleanup)
		line("in reasonable time. Its refusal prints a filesystem procedure that")
		line("deletes database directories by hand.")
	default:
		line("Cleanup will REFUSE these: %d of %d databases is above the ratio it", len(orphans), len(allDBs))
		line("stops at. Its refusal offers --force, which deletes every flagged")
		line("database without the per-database check for user tables.")
	}

	line("Read the whole thing without deleting anything: gt dolt cleanup --dry-run")
	line("See who owns every database, not just these: gt dolt list")
	line("To keep one of these permanently, add it to protected_dolt_databases in")
	line("settings/config.json — cleanup then refuses it even under --force.")

	return b.String()
}

// orphanRatioBalkMessage renders the orphan-ratio refusal, or "" when the ratio
// does not trip the check.
//
// The message deliberately does not rank the two explanations. It used to say a
// high ratio "usually means metadata.json files are missing or incorrect, not
// that the databases are actually orphaned" — an assertion the check has no
// evidence for, and one that is wrong in the case it meets most often. The ratio
// crosses 50% because a town is SMALL: with 11 databases, six real test fixtures
// are already 55%. So the message reliably told the operator "these are probably
// not real orphans" at the moment they were, and then pointed at --force to
// override a warning it had just told them to disbelieve. (gt-ti84)
func orphanRatioBalkMessage(orphans []doltserver.OrphanedDatabase, totalDBs int) string {
	// The thresholds are doltserver's, so this cannot describe a refusal the
	// deletion path would not raise. (gt-baj6)
	if !doltserver.OrphanRatioBalks(len(orphans), totalDBs) {
		return ""
	}
	ratio := float64(len(orphans)) / float64(totalDBs)

	var b strings.Builder
	fmt.Fprintf(&b, "\n%s %d of %d databases (%.0f%%) are flagged as orphans.\n\n",
		style.Bold.Render("!"), len(orphans), totalDBs, ratio*100)
	b.WriteString("  Two different situations produce a ratio this high, and this check\n")
	b.WriteString("  cannot tell them apart:\n\n")
	b.WriteString("    - Detection is wrong: metadata.json files are missing or unreadable,\n")
	b.WriteString("      so real databases look unreferenced.\n")
	fmt.Fprintf(&b, "    - Detection is right and the town is small: %d databases total, so a\n", totalDBs)
	b.WriteString("      handful of genuine test fixtures is already a majority.\n\n")
	b.WriteString(orphanCreationEvidence(orphans))
	b.WriteString("  To proceed anyway: gt dolt cleanup --force\n")
	b.WriteString("  To diagnose: gt dolt list   (owner for every database, not just these)\n")
	return b.String()
}

// orphanCreationEvidence renders what the operator can actually decide on.
// Creation times discriminate where the ratio cannot: fixtures are born in a
// burst during a test run, real databases when a town or rig was set up.
func orphanCreationEvidence(orphans []doltserver.OrphanedDatabase) string {
	var known []time.Time
	for _, o := range orphans {
		if !o.CreatedAt.IsZero() {
			known = append(known, o.CreatedAt)
		}
	}
	if len(known) == 0 {
		return "  Creation times, which would discriminate, are not available here — no\n" +
			"  birth time is recorded for these directories. Inspect the databases\n" +
			"  themselves before deleting.\n\n"
	}

	earliest, latest := known[0], known[0]
	for _, t := range known[1:] {
		if t.Before(earliest) {
			earliest = t
		}
		if t.After(latest) {
			latest = t
		}
	}

	var b strings.Builder
	first := earliest.Format("2006-01-02 15:04:05")
	last := latest.Format("2006-01-02 15:04:05")
	b.WriteString("  The creation times listed above are what discriminates.\n")
	if len(known) < len(orphans) {
		fmt.Fprintf(&b, "  %d of the %d flagged databases record one; those were created\n  between %s and %s.\n",
			len(known), len(orphans), first, last)
	} else {
		fmt.Fprintf(&b, "  All %d were created between %s and\n  %s.\n", len(orphans), first, last)
	}
	if len(known) > 1 && latest.Sub(earliest) <= orphanFixtureBurstWindow {
		fmt.Fprintf(&b, "  That is a %s window — the signature of one test run, not of a town's\n",
			latest.Sub(earliest).Round(time.Second))
		b.WriteString("  real databases, which are created when a town or rig is set up.\n\n")
	} else {
		b.WriteString("  Databases born in a burst are test fixtures; databases born when the\n")
		b.WriteString("  town or a rig was set up are real.\n\n")
	}
	return b.String()
}

// orphanCreatedSuffix renders ", created <time>" for the listing, or "" when
// the platform records no birth time for the directory.
func orphanCreatedSuffix(o doltserver.OrphanedDatabase) string {
	if o.CreatedAt.IsZero() {
		return ""
	}
	return ", created " + o.CreatedAt.Format("2006-01-02 15:04:05")
}

func runDoltCleanup(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	orphans, err := doltserver.FindOrphanedDatabases(townRoot)
	if err != nil {
		return fmt.Errorf("finding orphaned databases: %w", err)
	}

	if len(orphans) == 0 {
		fmt.Printf("%s No orphaned databases found in .dolt-data/\n", style.Bold.Render("✓"))
		return nil
	}

	fmt.Printf("Found %d orphaned database(s) in .dolt-data/:\n\n", len(orphans))
	for _, o := range orphans {
		fmt.Printf("  %s %s (%s%s)\n", style.Bold.Render("!"), o.Name, formatBytes(o.SizeBytes), orphanCreatedSuffix(o))
		fmt.Printf("    %s\n", style.Dim.Render(o.Path))
	}

	// Fail closed: without the town's total the orphan ratio cannot be computed,
	// and an unknown ratio must not read as an acceptable one. (gt-baj6)
	allDBs, err := doltserver.ListDatabases(townRoot)
	if err != nil {
		return fmt.Errorf("counting databases for the orphan-ratio safety check: %w", err)
	}
	balk := evaluateCleanupBalks(townRoot, orphans, allDBs, doltCleanupForce)

	if doltCleanupDry {
		if balk != nil {
			fmt.Print(balk.Message)
			fmt.Printf("\n%s This is not a preview of a successful cleanup: the real run stops\n", style.Bold.Render("!"))
			fmt.Printf("  at the refusal above and deletes nothing.\n")
		}
		fmt.Println("\nDry run: no changes made.")
		return nil
	}

	if balk != nil {
		fmt.Print(balk.Message)
		return balk.Err
	}

	fmt.Println()
	removed := 0
	for _, o := range orphans {
		if err := doltserver.RemoveDatabase(townRoot, o.Name, doltCleanupForce); err != nil {
			// If DROP caused read-only, stop immediately and recover (gt-r1cyd)
			if doltserver.IsReadOnlyError(err.Error()) {
				fmt.Printf("  %s DROP put server into read-only mode — attempting recovery...\n", style.Bold.Render("!"))
				if recoverErr := doltserver.RecoverReadOnly(townRoot); recoverErr != nil {
					fmt.Printf("  %s Recovery failed: %v\n", style.Bold.Render("✗"), recoverErr)
					fmt.Printf("  Run: gt dolt stop && gt dolt start\n")
				} else {
					fmt.Printf("  %s Server recovered from read-only state\n", style.Bold.Render("✓"))
				}
				break
			}
			fmt.Printf("  %s Failed to remove %s: %v\n", style.Bold.Render("✗"), o.Name, err)
			continue
		}
		fmt.Printf("  %s Removed %s\n", style.Bold.Render("✓"), o.Name)
		removed++

		// Health check after each DROP to catch read-only early (gt-r1cyd)
		if readOnly, _ := doltserver.CheckReadOnly(townRoot); readOnly {
			fmt.Printf("  %s Server went read-only after DROP — attempting recovery...\n", style.Bold.Render("!"))
			if recoverErr := doltserver.RecoverReadOnly(townRoot); recoverErr != nil {
				fmt.Printf("  %s Recovery failed: %v\n", style.Bold.Render("✗"), recoverErr)
				fmt.Printf("  Run: gt dolt stop && gt dolt start\n")
				break
			}
			fmt.Printf("  %s Server recovered — continuing cleanup\n", style.Bold.Render("✓"))
		}
	}

	fmt.Printf("\n%s Removed %d/%d orphaned database(s)\n",
		style.Bold.Render("✓"), removed, len(orphans))

	return nil
}

// doltDatabaseLabel renders the parenthesised annotation `gt dolt list` prints
// after a database name.
//
// Only databases `gt dolt cleanup` would actually remove are labelled "orphan":
// that column is read as a deletion list, and a database claimed by orphan
// detection through something other than a metadata.json — the rig-prefix
// safety net — gets a label that says so instead. (gt-ti84)
func doltDatabaseLabel(townRoot, db string, owners map[string]string, protected map[string]string, referenced map[string]bool) string {
	if owner, ok := owners[db]; ok {
		return owner
	}
	if !doltserver.IsOrphanDatabase(protected, referenced, db) {
		if doltserver.IsRigPrefixDatabase(townRoot, db) {
			return "rig prefix — no metadata.json owner"
		}
		return "claimed — no metadata.json owner"
	}
	return "orphan"
}

func runDoltList(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	databases, err := doltserver.ListDatabases(townRoot)
	if err != nil {
		return fmt.Errorf("listing databases: %w", err)
	}

	if len(databases) == 0 {
		fmt.Printf("No rig databases found in %s\n", config.DataDir)
		fmt.Printf("\nInitialize with: %s\n", style.Dim.Render("gt dolt init-rig <name>"))
		return nil
	}

	owners := doltserver.CollectDatabaseOwners(townRoot)
	// Decide "orphan" with the predicate `gt dolt cleanup` deletes by, not with
	// the owner map. An owner label is missing whenever no metadata.json names
	// the database, which is a strictly narrower question — that gap put "gt",
	// a real database claimed by the rig-prefix safety net, in this list under
	// the same word cleanup uses for what it removes. (gt-ti84)
	referenced := doltserver.ReferencedDatabases(townRoot)
	// Fail rather than print a column read as a deletion list while unable to
	// say which databases the town marked as never-delete. (gt-xhjb)
	protected, err := doltserver.ProtectedDatabases(townRoot)
	if err != nil {
		return err
	}
	fmt.Printf("Rig databases in %s:\n\n", config.DataDir)
	for _, db := range databases {
		dbDir := doltserver.RigDatabaseDir(townRoot, db)
		fmt.Printf("  %s (%s)\n    %s\n", style.Bold.Render(db), doltDatabaseLabel(townRoot, db, owners, protected, referenced), style.Dim.Render(dbDir))
	}

	return nil
}

func runDoltMigrate(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("Dolt server is remote (%s) — migration requires local server access", config.HostPort())
	}

	// Check if daemon is running - must stop first to avoid race conditions.
	// The daemon spawns many bd processes via gt status heartbeats. If these
	// run concurrently with migration, race conditions occur between old
	// old and new backends.
	daemonRunning, _, _ := daemon.IsRunning(townRoot)
	if daemonRunning {
		return fmt.Errorf("Gas Town daemon is running. Stop it first with: gt daemon stop\n\nThe daemon spawns bd processes that can race with migration.\nStop the daemon, run migration, then restart it.")
	}

	// Check if Dolt server is running - must stop first. Migration moves
	// database directories on disk, so the advice for getting the server down
	// has to name the thing that keeps it down: under an auto-restarting
	// supervisor `gt dolt stop` only signals the process, and an operator who
	// re-runs migrate inside the restart window gets a filesystem migration
	// under a live server. (gt-4ruo)
	//
	// The check itself stays point-in-time — nothing holds the server down for
	// the duration of the migration. migrationGuard below bounds that residual
	// race (gt-2xsa); it does not close it.
	running, pid, _ := doltserver.IsRunning(townRoot)
	if running {
		sup := doltserver.ObserveSupervisor(townRoot, pid)
		msg := fmt.Sprintf("Dolt server is running. Stop it first with: %s", sup.StopCommand())
		if sup.AutoRestarts() {
			msg += fmt.Sprintf("\n\nThis server is supervised by %s: `gt dolt stop` would only\nsignal the process and the supervisor starts a new server on the same data\ndirectory seconds later, while migration is moving it.", sup.Describe())
		}
		return errors.New(msg)
	}

	// No server is running — but "no server right now" is not "no server for
	// the next several minutes". If a supervisor unit owns this town's server
	// and that unit is not confirmed stopped, it can put a live one back on the
	// data directory mid-migration. DetectSupervisor cannot answer this with the
	// server down (no PID, no cgroup), which is why gt-2xsa could only bound the
	// exposure with migrationGuard; the unit remembered in dolt-state.json makes
	// the refusal possible. (gt-cru5)
	if sup := doltserver.RecallSupervisor(townRoot); sup != nil {
		if activeState, stopped := sup.ConfirmedStopped(); !stopped {
			return errors.New(unitNotStoppedRemedy(sup, activeState))
		}
	}

	// Find databases to migrate
	migrations := doltserver.FindMigratableDatabases(townRoot)
	if len(migrations) == 0 {
		fmt.Println("No databases found to migrate.")
		return nil
	}

	fmt.Printf("Found %d database(s) to migrate:\n\n", len(migrations))
	for _, m := range migrations {
		sizeStr := dirSizeHuman(m.SourcePath)
		fmt.Printf("  %s (%s)\n", m.SourcePath, sizeStr)
		fmt.Printf("    → %s\n\n", m.TargetPath)
	}

	if doltMigrateDry {
		fmt.Println("Dry run: no changes made.")
		return nil
	}

	// Perform migrations, re-checking at every directory boundary that the
	// server the precheck found stopped is still stopped. (gt-2xsa)
	guard := &migrationGuard{
		isRunning: func() (bool, int) {
			running, pid, _ := doltserver.IsRunning(townRoot)
			return running, pid
		},
		detect:  doltserver.DetectSupervisor,
		dataDir: config.DataDir,
	}
	if err := runGuardedMigrations(guard, migrations, func(m doltserver.Migration) error {
		return doltserver.MigrateRigFromBeads(townRoot, m.RigName, m.SourcePath)
	}); err != nil {
		return err
	}

	// Update metadata.json for all migrated rigs
	updated, metaErrs := doltserver.EnsureAllMetadata(townRoot)
	if len(updated) > 0 {
		fmt.Printf("\nUpdated metadata.json for: %s\n", strings.Join(updated, ", "))
	}
	for _, err := range metaErrs {
		fmt.Printf("  %s metadata.json update failed: %v\n", style.Dim.Render("⚠"), err)
	}

	fmt.Printf("\n%s Migration complete.\n", style.Bold.Render("✓"))

	// Auto-start the Dolt server to prevent split-brain risk.
	// If bd commands are run before the server starts, they may silently create
	// isolated local databases instead of connecting to the centralized server.
	fmt.Printf("\nStarting Dolt server to prevent split-brain risk...\n")
	if err := doltserver.Start(townRoot); err != nil {
		fmt.Printf("\n%s Could not auto-start Dolt server: %v\n", style.Bold.Render("⚠"), err)
		fmt.Printf("\n%s WARNING: Do NOT run bd commands until the server is started!\n", style.Bold.Render("⚠"))
		fmt.Printf("  Running bd before 'gt dolt start' risks split-brain: bd may create an\n")
		fmt.Printf("  isolated local database instead of connecting to the centralized server.\n")
		fmt.Printf("\n  Start manually with: %s\n", style.Dim.Render("gt dolt start"))
	} else {
		state, _ := doltserver.LoadState(townRoot)
		fmt.Printf("%s Dolt server started (PID %d)\n", style.Bold.Render("✓"), state.PID)

		// Verify the server is actually serving all databases that exist on disk.
		// Dolt silently skips databases with stale manifests after migration,
		// so filesystem discovery and SQL discovery can diverge.
		// Use retry since the server may still be loading databases after Start().
		served, missing, verifyErr := doltserver.VerifyDatabasesWithRetry(townRoot, 5)
		if verifyErr != nil {
			fmt.Printf("  %s Could not verify databases: %v\n", style.Dim.Render("⚠"), verifyErr)
			fmt.Printf("  Migration may be incomplete. Verify manually with: %s\n", style.Dim.Render("gt dolt status"))
			return fmt.Errorf("database verification failed after migration: %w", verifyErr)
		} else if len(missing) > 0 {
			fmt.Printf("\n%s Some databases exist on disk but are NOT served by Dolt:\n", style.Bold.Render("⚠"))
			for _, db := range missing {
				fmt.Printf("  - %s\n", db)
			}
			fmt.Printf("\n  Served databases: %v\n\n", served)
			// Detect against the server that is running NOW, not the PID gt
			// recorded when it started one: on a supervised town the process
			// gt started may already have been replaced.
			_, pid, _ := doltserver.IsRunning(townRoot)
			fmt.Print(staleManifestRemedy(config.DataDir, missing, doltserver.DetectSupervisor(pid)))
			return fmt.Errorf("migration incomplete: %d database(s) exist on disk but are not served: %v", len(missing), missing)
		} else {
			fmt.Printf("  %s All %d databases verified as served\n", style.Bold.Render("✓"), len(served))
		}
	}

	return nil
}

// unitNotStoppedRemedy renders the refusal printed when migration is asked to
// move database directories while the unit that owns the town's Dolt server is
// not confirmed stopped.
//
// It reports the ActiveState it read rather than asserting what the unit is
// doing. "activating" is a unit between a crash and its auto-restart, "active"
// with no server visible is a unit still binding its port or serving elsewhere,
// and an empty string is systemd not answering at all — three different
// situations that need three different operator responses, all of them "not
// yet" for migration.
func unitNotStoppedRemedy(sup *doltserver.Supervisor, activeState string) string {
	var b strings.Builder

	b.WriteString("Dolt is not running, but its supervisor is not confirmed stopped.\n\n")
	fmt.Fprintf(&b, "  This town's Dolt server is owned by %s.\n", sup.Describe())
	if activeState == "" {
		b.WriteString("  systemd gave no ActiveState for it — with no answer, gt cannot establish\n" +
			"  that the unit is down.\n\n")
	} else {
		fmt.Fprintf(&b, "  systemd reports ActiveState=%s, not inactive.\n\n", activeState)
	}
	b.WriteString("  Migration moves database directories on disk and nothing holds the server\n" +
		"  down for the minutes that takes. A unit that is not stopped can put a live\n" +
		"  server back on the data directory while directories are still moving —\n" +
		"  which corrupts whatever is being moved at that moment.\n\n")

	b.WriteString("  Stop the unit, then confirm it:\n\n")
	fmt.Fprintf(&b, "    1. %s\n", sup.StopCommand())
	fmt.Fprintf(&b, "    2. %s   # must print: inactive\n", sup.ConfirmStoppedCommand())
	b.WriteString("    3. gt dolt migrate\n\n")

	fmt.Fprintf(&b, "  Migration starts a server again when it finishes, through %s.\n", sup.Unit)

	return b.String()
}

// runGuardedMigrations moves each database, re-checking through guard that no
// Dolt server has appeared before every move and once after the last one.
//
// The trailing check is not decoration. Without it, a server that appears
// during the final move is never seen: the caller goes on to rewrite
// metadata.json and auto-start a server, and the run reports a clean migration.
//
// migrate is injected so the ordering — check, move, record, check — is pinned
// by tests without moving real database directories.
func runGuardedMigrations(guard *migrationGuard, migrations []doltserver.Migration, migrate func(doltserver.Migration) error) error {
	guard.order = nil
	guard.completed = 0
	for _, m := range migrations {
		guard.order = append(guard.order, m.RigName)
	}

	for _, m := range migrations {
		if err := guard.check(); err != nil {
			return err
		}
		fmt.Printf("Migrating %s...\n", m.RigName)
		if err := migrate(m); err != nil {
			return fmt.Errorf("migrating %s: %w", m.RigName, err)
		}
		guard.recordMoved()
		fmt.Printf("  %s Migrated to %s\n", style.Bold.Render("✓"), m.TargetPath)
	}

	return guard.check()
}

// migrationGuard re-checks, at every database boundary of `gt dolt migrate`,
// that no Dolt server has appeared on the data directory.
//
// runDoltMigrate refuses to start while a server is running, but that check is
// point-in-time and nothing holds the server down for the rest of the run.
// Under a supervisor with Restart=always, an operator who runs `gt dolt stop`
// and immediately runs migrate is inside the ~5s restart window: the precheck
// sees no server, migration starts, and the supervisor puts a live server back
// on the same .dolt-data while directories are still being moved. gt-4ruo made
// that less likely by naming the supervisor's stop command in the precheck's
// advice; advice is not a guarantee, and any other restart cause reopens the
// window. (gt-2xsa)
//
// The guard does not close the window — nothing in gt can stop a supervisor
// from starting a server — it bounds the exposure. A server that appears is
// caught at the next boundary, so the run stops instead of moving the rest of
// the town's databases under it, and the operator is told which databases
// moved and which did not rather than being left to work it out.
//
// Holding the unit stopped for the duration, the other remedy considered, is
// not available here: DetectSupervisor identifies the unit from the running
// process's cgroup, so with the server down there is no PID and no unit name —
// undiscoverable in exactly the window that matters.
type migrationGuard struct {
	// isRunning and detect are injected so the guard's behaviour is testable
	// without a live Dolt server or a live systemd.
	isRunning func() (bool, int)
	detect    func(pid int) *doltserver.Supervisor

	dataDir string

	order     []string // rig names, in the order they are migrated
	completed int      // how many of order have been moved
}

// check reports an error when a Dolt server is on the data directory now. The
// error text is the operator's whole recovery, so callers return it as-is.
func (g *migrationGuard) check() error {
	running, pid := g.isRunning()
	if !running {
		return nil
	}
	return errors.New(serverAppearedDuringMigrationRemedy(
		g.detect(pid), g.dataDir, g.order[:g.completed], g.order[g.completed:]))
}

// recordMoved marks the next database in order as migrated.
func (g *migrationGuard) recordMoved() {
	if g.completed < len(g.order) {
		g.completed++
	}
}

// serverAppearedDuringMigrationRemedy renders the refusal printed when a Dolt
// server turns up part-way through migration.
//
// Two things this has to get right beyond naming the supervisor's stop command:
//
//   - It must report the on-disk state. Migration is a sequence of directory
//     moves, so aborting leaves the town split between the old per-rig layout
//     and .dolt-data. An operator who is not told which databases are where has
//     to reconstruct it before they can safely do anything.
//   - It must not overstate what the check proves. The guard runs BETWEEN
//     moves, so it establishes that no server was visible at each boundary —
//     never that none was up during a move. The most recently moved database is
//     the one that could have been moved out from under a live server, and the
//     message says so instead of implying the abort caught everything.
func serverAppearedDuringMigrationRemedy(sup *doltserver.Supervisor, dataDir string, moved, remaining []string) string {
	var b strings.Builder

	supervised := sup.Describe() != ""

	b.WriteString("A Dolt server appeared while migration was running.\n\n")
	b.WriteString("  The precheck found no server. This run re-checks before every database\n")
	b.WriteString("  move, and a server is on the data directory now, so migration stopped at\n")
	b.WriteString("  the boundary rather than move more directories under a live server.\n\n")

	if supervised {
		b.WriteString(fmt.Sprintf("  ! This server is supervised by %s.\n", sup.Describe()))
		if sup.AutoRestarts() {
			b.WriteString("    `gt dolt stop` would only signal the process — the supervisor starts\n")
			b.WriteString("    a new server on the same data directory seconds later. Stop the unit.\n")
		}
		b.WriteString("\n")
	}

	b.WriteString(fmt.Sprintf("  Moved:     %s\n", joinOrNone(moved)))
	b.WriteString(fmt.Sprintf("  Not moved: %s\n\n", joinOrNone(remaining)))

	if len(moved) > 0 {
		b.WriteString(fmt.Sprintf(
			"  The check runs between moves, so it does not prove the server was down\n"+
				"  for the whole of the last one. Verify %s first — a database moved while\n"+
				"  a server held it open can be corrupt.\n\n", moved[len(moved)-1]))
	}

	b.WriteString("  To finish the migration:\n\n")
	b.WriteString(fmt.Sprintf("    1. Stop the server:   %s\n", sup.StopCommand()))
	b.WriteString("    2. Verify it is down: gt dolt status    # REQUIRED: must say \"not running\"\n")
	b.WriteString("    3. Re-run migration:  gt dolt migrate\n\n")

	b.WriteString(fmt.Sprintf("  Migration is resumable: it skips databases already present in\n  %s, so re-running moves only what is left.\n", dataDir))

	if supervised {
		// This paragraph used to tell the operator to hand the server back to
		// the unit by hand, because the auto-start at the end of migration
		// spawned a process outside it. Start now routes through the unit
		// itself, so that instruction would send them to start a unit that is
		// already running. (gt-cru5)
		b.WriteString(fmt.Sprintf(
			"\n  `gt dolt migrate` starts a server again when it finishes, through\n"+
				"  %s, so it comes back supervised. Nothing to hand back by hand.\n",
			sup.Unit))
	}

	return b.String()
}

// joinOrNone renders a database list for the abort message. An empty list is
// "none" rather than a blank: a blank reads as a rendering bug, and "none" is
// load-bearing on both lines — nothing moved, or nothing is left to move.
func joinOrNone(names []string) string {
	if len(names) == 0 {
		return "none"
	}
	return strings.Join(names, ", ")
}

// dirSizeHuman returns a human-readable size string for a directory tree.
func dirSizeHuman(path string) string {
	var total int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	})
	return formatBytes(total)
}

func runDoltFixMetadata(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	updated, errs := doltserver.EnsureAllMetadata(townRoot)

	if len(updated) > 0 {
		fmt.Printf("%s Updated metadata.json for %d rig(s):\n", style.Bold.Render("✓"), len(updated))
		for _, name := range updated {
			fmt.Printf("  - %s\n", name)
		}
	}

	if len(errs) > 0 {
		fmt.Println()
		for _, err := range errs {
			fmt.Printf("  %s %v\n", style.Dim.Render("⚠"), err)
		}
	}

	if len(updated) == 0 && len(errs) == 0 {
		fmt.Println("No rig databases found. Nothing to update.")
	}

	return nil
}

func runDoltRecover(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("Dolt server is remote (%s) — recovery requires local server access", config.HostPort())
	}

	running, _, _ := doltserver.IsRunning(townRoot)
	if !running {
		return fmt.Errorf("Dolt server is not running — start with 'gt dolt start'")
	}

	readOnly, err := doltserver.CheckReadOnly(townRoot)
	if err != nil {
		return fmt.Errorf("read-only probe failed: %w", err)
	}

	if !readOnly {
		fmt.Printf("%s Dolt server is writable (no recovery needed)\n", style.Bold.Render("✓"))
		return nil
	}

	if err := doltserver.RecoverReadOnly(townRoot); err != nil {
		return fmt.Errorf("recovery failed: %w", err)
	}

	fmt.Printf("%s Dolt server recovered from read-only state\n", style.Bold.Render("✓"))
	return nil
}

func runDoltRollback(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("Dolt server is remote (%s) — rollback requires local server access", config.HostPort())
	}

	// Find available backups
	backups, err := doltserver.FindBackups(townRoot)
	if err != nil {
		return fmt.Errorf("finding backups: %w", err)
	}

	if len(backups) == 0 {
		return fmt.Errorf("no migration backups found in %s\nExpected directories matching: migration-backup-YYYYMMDD-HHMMSS/", townRoot)
	}

	// List mode: show available backups and exit
	if doltRollbackList {
		fmt.Printf("Available migration backups in %s:\n\n", townRoot)
		for i, b := range backups {
			label := ""
			if i == 0 {
				label = " (most recent)"
			}
			fmt.Printf("  %s%s\n", b.Timestamp, label)
			fmt.Printf("    %s\n", style.Dim.Render(b.Path))
			if b.Metadata != nil {
				if createdAt, ok := b.Metadata["created_at"]; ok {
					fmt.Printf("    Created: %v\n", createdAt)
				}
			}
		}
		return nil
	}

	// Determine which backup to use
	var backupPath string
	if len(args) > 0 {
		// User specified a backup directory
		backupPath = args[0]
		// Check if it's a relative path or timestamp
		if _, err := os.Stat(backupPath); os.IsNotExist(err) {
			// Try as a timestamp suffix
			candidate := fmt.Sprintf("migration-backup-%s", args[0])
			candidatePath := fmt.Sprintf("%s/%s", townRoot, candidate)
			if _, err := os.Stat(candidatePath); err == nil {
				backupPath = candidatePath
			} else {
				return fmt.Errorf("backup not found: %s\nUse --list to see available backups", args[0])
			}
		}
	} else {
		// Use the most recent backup
		backupPath = backups[0].Path
	}

	fmt.Printf("Backup: %s\n", backupPath)

	// Dry-run mode: show what would be restored
	if doltRollbackDry {
		fmt.Printf("\n%s Dry run - no changes will be made\n\n", style.Bold.Render("!"))
		printBackupContents(backupPath, townRoot)
		return nil
	}

	// Stop Dolt server if running
	running, _, _ := doltserver.IsRunning(townRoot)
	if running {
		fmt.Println("Stopping Dolt server...")
		if err := doltserver.Stop(townRoot); err != nil {
			return fmt.Errorf("stopping Dolt server: %w", err)
		}
		fmt.Printf("%s Dolt server stopped\n", style.Bold.Render("✓"))
	}

	// Perform the rollback
	fmt.Println("\nRestoring from backup...")
	result, err := doltserver.RestoreFromBackup(townRoot, backupPath)
	if err != nil {
		return fmt.Errorf("rollback failed: %w", err)
	}

	// Report results
	fmt.Println()
	if result.RestoredTown {
		fmt.Printf("  %s Restored town-level .beads\n", style.Bold.Render("✓"))
	}
	for _, rig := range result.RestoredRigs {
		fmt.Printf("  %s Restored %s/.beads\n", style.Bold.Render("✓"), rig)
	}
	for _, rig := range result.SkippedRigs {
		fmt.Printf("  %s Skipped %s (restore failed)\n", style.Dim.Render("⚠"), rig)
	}

	if len(result.MetadataReset) > 0 {
		fmt.Printf("\n  Metadata reset for: %s\n", strings.Join(result.MetadataReset, ", "))
	}

	// Validate restored state
	fmt.Println("\nValidating restored state...")
	validateCmd := exec.Command("bd", "list", "--limit", "5")
	validateCmd.Dir = townRoot
	output, validateErr := validateCmd.CombinedOutput()
	if validateErr != nil {
		fmt.Printf("  %s bd list returned an error: %v\n",
			style.Dim.Render("⚠"), validateErr)
		if len(output) > 0 {
			fmt.Printf("  %s\n", string(output))
		}
	} else {
		fmt.Printf("  %s bd list succeeded\n", style.Bold.Render("✓"))
		if len(output) > 0 {
			// Show first few lines of output
			lines := strings.Split(strings.TrimSpace(string(output)), "\n")
			for _, line := range lines {
				fmt.Printf("  %s\n", style.Dim.Render(line))
			}
		}
	}

	fmt.Printf("\n%s Rollback complete from %s\n", style.Bold.Render("✓"), backupPath)

	return nil
}

// printBackupContents shows what's in a backup directory for dry-run output.
func printBackupContents(backupPath, townRoot string) {
	// Check town-level backup
	townBackup := fmt.Sprintf("%s/town-beads", backupPath)
	if _, err := os.Stat(townBackup); err == nil {
		dst := fmt.Sprintf("%s/.beads", townRoot)
		fmt.Printf("  Would restore: %s\n", style.Dim.Render(dst))
		fmt.Printf("    From: %s\n", style.Dim.Render(townBackup))
	}

	// Check formula-style rig backups
	entries, err := os.ReadDir(backupPath)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		if name == "town-beads" || name == "rigs" {
			continue
		}
		if strings.HasSuffix(name, "-beads") {
			rigName := strings.TrimSuffix(name, "-beads")
			dst := fmt.Sprintf("%s/%s/.beads", townRoot, rigName)
			src := fmt.Sprintf("%s/%s", backupPath, name)
			fmt.Printf("  Would restore: %s\n", style.Dim.Render(dst))
			fmt.Printf("    From: %s\n", style.Dim.Render(src))
		}
	}

	// Check test-backup-style rig backups
	rigsDir := fmt.Sprintf("%s/rigs", backupPath)
	if rigEntries, err := os.ReadDir(rigsDir); err == nil {
		for _, entry := range rigEntries {
			if !entry.IsDir() {
				continue
			}
			rigName := entry.Name()
			beadsDir := fmt.Sprintf("%s/%s/.beads", rigsDir, rigName)
			if _, err := os.Stat(beadsDir); err != nil {
				continue
			}
			dst := fmt.Sprintf("%s/%s/.beads", townRoot, rigName)
			fmt.Printf("  Would restore: %s\n", style.Dim.Render(dst))
			fmt.Printf("    From: %s\n", style.Dim.Render(beadsDir))
		}
	}
}

func runDoltSync(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("Dolt server is remote (%s) — sync requires local server access", config.HostPort())
	}

	// Validate --db flag if set
	if doltSyncDB != "" && !doltserver.DatabaseExists(townRoot, doltSyncDB) {
		return fmt.Errorf("database %q not found in .dolt-data/\nRun 'gt dolt list' to see available databases", doltSyncDB)
	}

	// Check server state
	wasRunning, _, _ := doltserver.IsRunning(townRoot)

	// GC phase: purge closed ephemeral beads (requires running server).
	purgeResults := make(map[string]struct {
		purged int
		err    error
	})
	if doltSyncGC {
		if !wasRunning {
			fmt.Fprintf(os.Stderr, "Warning: --gc requires a running Dolt server, skipping purge\n")
		} else {
			databases, listErr := doltserver.ListDatabases(townRoot)
			if listErr != nil {
				fmt.Fprintf(os.Stderr, "Warning: --gc: could not list databases: %v\n", listErr)
			} else {
				for _, db := range databases {
					if doltSyncDB != "" && db != doltSyncDB {
						continue
					}
					purged, purgeErr := doltserver.PurgeClosedEphemerals(townRoot, db, doltSyncDry)
					purgeResults[db] = struct {
						purged int
						err    error
					}{purged, purgeErr}
				}
			}
		}
	}

	opts := doltserver.SyncOptions{
		Force:  doltSyncForce,
		DryRun: doltSyncDry,
		Filter: doltSyncDB,
	}

	// Use SQL push through the running server (no downtime).
	// Fall back to CLI push (with server stop/restart) only when server isn't running.
	var results []doltserver.SyncResult
	if wasRunning {
		fmt.Printf("Pushing via SQL (server stays running)...\n")
		results = doltserver.SyncDatabasesSQL(townRoot, opts)
	} else {
		fmt.Printf("Server not running — using CLI push...\n")
		results = doltserver.SyncDatabases(townRoot, opts)
	}

	if len(results) == 0 {
		fmt.Println("No databases to sync.")
		return nil
	}

	fmt.Printf("\nSyncing %d database(s)...\n", len(results))

	var pushed, skipped, failed, totalPurged int
	for _, r := range results {
		fmt.Println()
		// Show purge results if --gc was used
		if doltSyncGC {
			if pr, ok := purgeResults[r.Database]; ok {
				if pr.err != nil {
					fmt.Printf("  %s %s gc: %v\n", style.Bold.Render("!"), r.Database, pr.err)
				} else if pr.purged > 0 {
					verb := "purged"
					if doltSyncDry {
						verb = "would purge"
					}
					fmt.Printf("  %s %s gc: %s %d closed ephemeral bead(s)\n", style.Bold.Render("✓"), r.Database, verb, pr.purged)
					totalPurged += pr.purged
				}
			}
		}
		switch {
		case r.Pushed:
			fmt.Printf("  %s %s → origin main\n", style.Bold.Render("✓"), r.Database)
			fmt.Printf("    %s\n", style.Dim.Render(r.Remote))
			pushed++
		case r.DryRun:
			fmt.Printf("  %s %s → origin main (dry run)\n", style.Bold.Render("~"), r.Database)
			fmt.Printf("    %s\n", style.Dim.Render(r.Remote))
			pushed++ // count as would-push for summary
		case r.Skipped:
			fmt.Printf("  %s %s — no remote configured\n", style.Dim.Render("○"), r.Database)
			skipped++
		case r.Error != nil:
			fmt.Printf("  %s %s → origin main\n", style.Bold.Render("✗"), r.Database)
			fmt.Printf("    error: %v\n", r.Error)
			failed++
		}
	}

	summary := fmt.Sprintf("Summary: %d pushed, %d skipped, %d failed", pushed, skipped, failed)
	if doltSyncGC && totalPurged > 0 {
		if doltSyncDry {
			summary += fmt.Sprintf(", %d would be purged", totalPurged)
		} else {
			summary += fmt.Sprintf(", %d purged", totalPurged)
		}
	}
	fmt.Printf("\n%s\n", summary)

	if failed > 0 {
		return fmt.Errorf("%d database(s) failed to sync", failed)
	}
	return nil
}

func runDoltPull(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	config := doltserver.DefaultConfig(townRoot)
	if config.IsRemote() {
		return fmt.Errorf("Dolt server is remote (%s) — pull requires local server access", config.HostPort())
	}

	// Validate --db flag if set
	if doltPullDB != "" && !doltserver.DatabaseExists(townRoot, doltPullDB) {
		return fmt.Errorf("database %q not found in .dolt-data/\nRun 'gt dolt list' to see available databases", doltPullDB)
	}

	// Check server state
	wasRunning, _, _ := doltserver.IsRunning(townRoot)

	opts := doltserver.SyncOptions{
		DryRun: doltPullDry,
		Filter: doltPullDB,
	}

	// Use SQL pull through the running server (no lock contention).
	// Fall back to CLI pull only when server isn't running.
	var results []doltserver.SyncResult
	if wasRunning {
		fmt.Printf("Pulling via SQL (server stays running)...\n")
		results = doltserver.PullDatabasesSQL(townRoot, opts)
	} else {
		fmt.Printf("Server not running — using CLI pull...\n")
		results = doltserver.PullDatabases(townRoot, opts)
	}

	if len(results) == 0 {
		fmt.Println("No databases to pull.")
		return nil
	}

	fmt.Printf("\nPulling %d database(s)...\n", len(results))

	var pulled, skipped, failed int
	for _, r := range results {
		switch {
		case r.Pushed: // reused field = success
			fmt.Printf("  %s %s ← %s\n", style.Bold.Render("✓"), r.Database, r.Remote)
			pulled++
		case r.DryRun:
			fmt.Printf("  %s %s ← %s (dry run)\n", style.Bold.Render("~"), r.Database, r.Remote)
			pulled++
		case r.Skipped:
			fmt.Printf("  %s %s — no remote configured\n", style.Dim.Render("○"), r.Database)
			skipped++
		case r.Error != nil:
			fmt.Printf("  %s %s ← remote\n", style.Bold.Render("✗"), r.Database)
			fmt.Printf("    error: %v\n", r.Error)
			failed++
		}
	}

	fmt.Printf("\nSummary: %d pulled, %d skipped, %d failed\n", pulled, skipped, failed)

	if failed > 0 {
		return fmt.Errorf("%d database(s) failed to pull", failed)
	}
	return nil
}

func runDoltMigrateWisps(cmd *cobra.Command, args []string) error {
	townRoot, err := workspace.FindFromCwdOrError()
	if err != nil {
		return fmt.Errorf("not in a Gas Town workspace: %w", err)
	}

	// Determine which rigs to migrate
	if doltMigrateWispsDB != "" {
		// Migrate a specific rig
		rigDir := filepath.Join(townRoot, doltMigrateWispsDB)
		if _, err := os.Stat(rigDir); os.IsNotExist(err) {
			return fmt.Errorf("rig directory not found: %s", rigDir)
		}
		fmt.Printf("%s Migrating: %s\n", style.Bold.Render("→"), doltMigrateWispsDB)
		result, err := doltserver.MigrateAgentBeadsToWisps(townRoot, rigDir, doltMigrateWispsDry)
		if err != nil {
			return err
		}
		printMigrateWispsResult(result)
		return nil
	}

	// Auto-detect: migrate all rigs that have beads databases
	databases, err := doltserver.ListDatabases(townRoot)
	if err != nil {
		return fmt.Errorf("listing databases: %w", err)
	}

	for _, db := range databases {
		// Skip non-rig databases
		if db == "wl_commons" || strings.HasPrefix(db, "testdb_") {
			continue
		}
		// Find the rig directory for this database.
		// The "hq" database lives at the town root itself, not townRoot/hq.
		rigDir := filepath.Join(townRoot, db)
		if db == "hq" {
			rigDir = townRoot
		} else if _, err := os.Stat(rigDir); os.IsNotExist(err) {
			continue // Not a rig directory
		}
		fmt.Printf("\n%s Migrating: %s\n", style.Bold.Render("→"), db)
		result, err := doltserver.MigrateAgentBeadsToWisps(townRoot, rigDir, doltMigrateWispsDry)
		if err != nil {
			fmt.Printf("  %s %s: %v\n", style.Bold.Render("✗"), db, err)
			continue
		}
		printMigrateWispsResult(result)
	}
	return nil
}

func printMigrateWispsResult(result *doltserver.MigrateWispsResult) {
	if result.WispsTableCreated {
		fmt.Printf("  %s Created wisps table\n", style.Bold.Render("✓"))
	}
	for _, t := range result.AuxTablesCreated {
		fmt.Printf("  %s Created %s\n", style.Bold.Render("✓"), t)
	}
	if result.AgentsCopied > 0 {
		fmt.Printf("  %s Copied %d agent beads to wisps\n", style.Bold.Render("✓"), result.AgentsCopied)
	}
	if result.LabelsCopied > 0 {
		fmt.Printf("  %s Copied %d labels\n", style.Bold.Render("✓"), result.LabelsCopied)
	}
	if result.CommentsCopied > 0 {
		fmt.Printf("  %s Copied %d comments\n", style.Bold.Render("✓"), result.CommentsCopied)
	}
	if result.EventsCopied > 0 {
		fmt.Printf("  %s Copied %d events\n", style.Bold.Render("✓"), result.EventsCopied)
	}
	if result.DepsCopied > 0 {
		fmt.Printf("  %s Copied %d dependencies\n", style.Bold.Render("✓"), result.DepsCopied)
	}
	if result.AgentsClosed > 0 {
		fmt.Printf("  %s Closed %d original agent beads\n", style.Bold.Render("✓"), result.AgentsClosed)
	}
	if result.AgentsCopied == 0 && len(result.AuxTablesCreated) == 0 && !result.WispsTableCreated {
		fmt.Printf("  %s Already migrated (no changes needed)\n", style.Bold.Render("✓"))
	}
}
