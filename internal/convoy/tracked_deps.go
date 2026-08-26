package convoy

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/doltserver"
)

// Reading a convoy's tracked issues.
//
// `bd dep list <convoy> -t tracks --json` JOINS the dependencies table against
// the issues table, so it returns NOTHING for a dependency whose target lives in
// a different Dolt database — which is every convoy that tracks rig work from
// the HQ store (GH #2624). Measured 2026-08-25 against five live convoys: the
// join returned [] for all five while the raw dependencies table returned one
// tracked issue each. The dashboard read the join, so it rendered every convoy
// as having zero tracked beads and reported the town as empty while five
// polecats were working.
//
// Reading the dependencies table directly is the only lookup that answers for
// cross-database convoys, so both surfaces use this one.

// depQueryTimeout bounds the direct Dolt query. Long enough for a loaded
// server, short enough that a hung server does not hold a dashboard render.
const depQueryTimeout = 30 * time.Second

// TrackedIssueIDs returns the deduplicated IDs of the issues a convoy tracks,
// read from the dependencies table with no join against issues.
//
// dir is the directory whose beads store holds the convoy (the town root for
// HQ convoys). External refs (external:prefix:id) are unwrapped to bare IDs.
func TrackedIssueIDs(dir, convoyID string) ([]string, error) {
	return RawDepIDsViaDolt(dir, convoyID, "down", "tracks")
}

// RawDepIDsViaDolt queries the raw dependencies table over the Dolt SQL server.
//
// direction is "down" (issue_id → depends_on_id) or "up" (depends_on_id →
// issue_id). depType filters by dependency type ("tracks", "blocks"); empty
// means all types.
func RawDepIDsViaDolt(dir, issueID, direction, depType string) ([]string, error) {
	// Bead IDs are system-generated alphanumeric strings; validate before
	// interpolating anything derived from them.
	if !IsValidBeadID(issueID) {
		return nil, fmt.Errorf("invalid bead ID: %q", issueID)
	}
	if depType != "" && !IsValidBeadID(depType) {
		return nil, fmt.Errorf("invalid dep type: %q", depType)
	}

	beadsDir := beads.ResolveBeadsDir(dir)
	cfg, ok := DoltRuntimeConfig(beadsDir)
	if !ok || cfg.Database == "" || cfg.Port == 0 {
		return nil, fmt.Errorf("missing server metadata for %s", beadsDir)
	}
	host := cfg.Host
	if host == "" {
		host = "127.0.0.1"
	}
	dsn := fmt.Sprintf("root@tcp(%s)/%s?parseTime=true", net.JoinHostPort(host, strconv.Itoa(cfg.Port)), url.PathEscape(cfg.Database))
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), depQueryTimeout)
	defer cancel()

	typedQuery, typedArgs := rawDepSQLArgs(issueID, direction, depType, false)
	ids, err := queryRawDepIDs(ctx, db, typedQuery, typedArgs)
	if err == nil {
		return ids, nil
	}
	legacyQuery, legacyArgs := rawDepSQLArgs(issueID, direction, depType, true)
	return queryRawDepIDs(ctx, db, legacyQuery, legacyArgs)
}

// RawDepSQL renders the same query as a literal string, for callers that reach
// the dependencies table through `bd sql` instead of a direct connection.
func RawDepSQL(issueID, direction, depType string, legacy bool) string {
	query, args := rawDepSQLArgs(issueID, direction, depType, legacy)
	for _, arg := range args {
		query = strings.Replace(query, "?", "'"+arg.(string)+"'", 1)
	}
	return query
}

func rawDepSQLArgs(issueID, direction, depType string, legacy bool) (string, []any) {
	var query string
	var args []any
	if direction == "up" {
		if legacy {
			query = "SELECT issue_id FROM dependencies WHERE depends_on_id = ?"
			args = append(args, issueID)
		} else {
			query = "SELECT issue_id FROM dependencies WHERE (depends_on_issue_id = ? OR depends_on_wisp_id = ? OR depends_on_external LIKE ? ESCAPE '!')"
			args = append(args, issueID, issueID, "%:"+strings.ReplaceAll(issueID, "_", "!_"))
		}
	} else if legacy {
		query = "SELECT depends_on_id FROM dependencies WHERE issue_id = ?"
		args = append(args, issueID)
	} else {
		query = "SELECT COALESCE(depends_on_issue_id, depends_on_wisp_id, depends_on_external) AS depends_on_id FROM dependencies WHERE issue_id = ?"
		args = append(args, issueID)
	}
	if depType != "" {
		query += " AND type = ?"
		args = append(args, depType)
	}
	return query, args
}

func queryRawDepIDs(ctx context.Context, db *sql.DB, query string, args []any) ([]string, error) {
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	seen := make(map[string]bool)
	var ids []string
	for rows.Next() {
		var rawID sql.NullString
		if err := rows.Scan(&rawID); err != nil {
			return nil, err
		}
		if !rawID.Valid {
			continue
		}
		id := beads.ExtractIssueID(rawID.String)
		if id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return ids, nil
}

// IsValidBeadID checks that a string is safe for SQL interpolation in dep
// queries. Bead IDs contain only alphanumeric chars, hyphens, dots, underscores.
func IsValidBeadID(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '.' || c == '_') {
			return false
		}
	}
	return true
}

// RuntimeConfig names the Dolt server a beads store is served from.
type RuntimeConfig struct {
	Source   string
	Database string
	Host     string
	Port     int
}

// DoltRuntimeConfig reads a beads store's server metadata. The second return is
// false when the store is not Dolt-in-server-mode, in which case there is no
// server to query and callers must use bd.
func DoltRuntimeConfig(beadsDir string) (RuntimeConfig, bool) {
	metadataPath := filepath.Join(beadsDir, "metadata.json")
	data, err := os.ReadFile(metadataPath)
	if err != nil {
		return RuntimeConfig{}, false
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
		return RuntimeConfig{}, false
	}
	if metadata.Backend != "dolt" || metadata.DoltMode != "server" {
		return RuntimeConfig{}, false
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

	return RuntimeConfig{
		Source:   metadataPath,
		Database: database,
		Host:     host,
		Port:     port,
	}, true
}
