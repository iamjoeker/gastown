package beads

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Pinning a wisp (gt-31nn).
//
// Two independent protections guard a wisp from deletion, and they are not
// interchangeable:
//
//   - a protected LABEL (reaper.ProtectedWispLabels) keeps the row out of a
//     plain purge;
//   - wisps.pinned = 1 additionally keeps it out of the archive-then-delete
//     path, which deliberately exports and then removes rows that are protected
//     BY TYPE and not pinned (reaper.archivableProtectWhere).
//
// An MR wisp needs both. It carries the record of a merge — branch, commit SHA,
// source issue, how it ended — and gastown already assumes it is pinned:
// mq submit and the refinery both force-close their own MRs precisely because a
// pin would otherwise refuse the close (gt-6dp, gt-obth). Nothing wrote the
// column, so the assumption held only for whichever rows a human had swept by
// hand.
//
// bd has no --pinned flag on create or update, so SQL against the wisps table is
// the only route to the column, exactly as it is for wisp_type.

// WispPinUpdateSQL builds the statement that pins the given wisp IDs, for
// `bd sql`.
//
// The statement targets the wisps table only. Callers pass IDs they just
// created or just read back, so a stray match is not possible, but writing to
// the wisps table by name is kept explicit: a typo that reached the issues
// table would pin a permanent bead and quietly block its close.
func WispPinUpdateSQL(ids []string) (string, error) {
	quoted, err := quoteWispIDs(ids)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("UPDATE wisps SET pinned = 1 WHERE id IN (%s)",
		strings.Join(quoted, ", ")), nil
}

// WispPinnedCountSQL builds the control query for WispPinUpdateSQL: how many of
// those IDs actually read pinned = 1 afterwards.
//
// COALESCE because the column is nullable and an unpinned row may hold NULL
// rather than 0; `pinned = 1` alone is still the right test for pinned, but the
// COALESCE keeps this query's predicate identical to the one the reaper uses to
// decide protection, so the control answers the question the purge will ask.
func WispPinnedCountSQL(ids []string) (string, error) {
	quoted, err := quoteWispIDs(ids)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("SELECT COUNT(*) AS n FROM wisps WHERE COALESCE(pinned, 0) = 1 AND id IN (%s)",
		strings.Join(quoted, ", ")), nil
}

// PinWisps sets wisps.pinned = 1 on the given wisp IDs and confirms the write
// landed, in the database that owns rig. An empty rig means "the database this
// wrapper already resolves to".
//
// Routing is the same routing Create(CreateOptions{Rig: ...}) uses, on purpose:
// the row was written by that create, so the pin has to reach the same store.
// A wisp UPDATE gets no prefix routing from bd, so a wrapper rooted anywhere
// else — the town, another rig — would run the statement against a database
// that has no such row, and an UPDATE that matches nothing exits 0.
//
// Which is why the write is followed by a read. Every way this can fail
// silently ends in the same place: the caller believes the record is protected,
// and it is not. The count separates the three outcomes the caller cares about
// — the statement errored, it ran and matched nothing, it worked — instead of
// collapsing the first two into a nil error.
func (b *Beads) PinWisps(rig string, ids ...string) error {
	updateSQL, err := WispPinUpdateSQL(ids)
	if err != nil {
		return err
	}
	countSQL, err := WispPinnedCountSQL(ids)
	if err != nil {
		return err
	}

	target, err := b.forRig(rig)
	if err != nil {
		return err
	}

	if _, err := target.run("sql", updateSQL); err != nil {
		return fmt.Errorf("pinning %s: %w", strings.Join(ids, ", "), err)
	}

	out, err := target.run("sql", "--json", countSQL)
	if err != nil {
		return fmt.Errorf("confirming the pin on %s: %w", strings.Join(ids, ", "), err)
	}
	pinned, err := parsePinnedCount(out)
	if err != nil {
		return fmt.Errorf("confirming the pin on %s: %w", strings.Join(ids, ", "), err)
	}
	if pinned != len(ids) {
		return fmt.Errorf("pin did not stick: %d of %d wisp(s) read pinned=1 after the update (%s)",
			pinned, len(ids), strings.Join(ids, ", "))
	}
	return nil
}

// forRig returns a wrapper bound to rig's database. An empty rig returns b
// unchanged. Mirrors the redirect Create performs for CreateOptions.Rig.
func (b *Beads) forRig(rig string) (*Beads, error) {
	if rig == "" {
		return b, nil
	}
	targetDir, err := b.targetBeadsDirForCreate(CreateOptions{Rig: rig})
	if err != nil {
		return nil, err
	}
	if targetDir == "" || targetDir == b.getResolvedBeadsDir() {
		return b, nil
	}
	return &Beads{
		workDir:    b.workDir,
		beadsDir:   targetDir,
		serverPort: b.serverPort,
		isolated:   b.isolated,
	}, nil
}

// parsePinnedCount reads the count out of `bd sql --json` output.
//
// The payload is an array of row objects keyed by column alias, and bd may emit
// notices on stdout ahead of it, so the array is located rather than assumed to
// start at byte zero. An empty array is a parse failure, not a zero: COUNT(*)
// always returns one row, so no rows means the output was not the answer to
// this query.
func parsePinnedCount(out []byte) (int, error) {
	idx := bytes.IndexByte(out, '[')
	if idx < 0 {
		return 0, fmt.Errorf("no JSON array in bd sql output: %q", strings.TrimSpace(string(out)))
	}
	var rows []struct {
		N int `json:"n"`
	}
	if err := json.Unmarshal(out[idx:], &rows); err != nil {
		return 0, fmt.Errorf("parsing bd sql output: %w", err)
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("bd sql returned no rows for a COUNT(*) query")
	}
	return rows[0].N, nil
}
