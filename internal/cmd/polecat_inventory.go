package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/steveyegge/gastown/internal/beads"
	"github.com/steveyegge/gastown/internal/polecat"
)

const polecatSessionKeySep = "\x00"

type polecatSessionSet map[string]string

type polecatInventoryItem struct {
	Rig            string
	Name           string
	State          polecat.State
	Issue          string
	CleanupStatus  string
	ActiveMR       string
	Branch         string
	SessionRunning bool
	SessionName    string
	Disposition    polecat.WorkstateDisposition
}

type polecatActiveWorkEvidence struct {
	BlocksCleanup        bool
	RequiresRestart      bool
	CountsTowardCapacity bool
	Blocker              string
	AssignedIssue        string

	// PausedAgentState carries a deliberate pause (stuck, awaiting-gate, paused,
	// escalated) separately from Blocker. It blocks cleanup like active work does,
	// but it is not work: nothing is running and nothing is at risk, so it routes
	// to WorkstateInput.PausedAgentState — whose remedy is `gt polecat clear-state`
	// — instead of ActiveWorkBlocker, whose remedy is escalation (gt-fbgq).
	PausedAgentState string
}

func newPolecatSessionSet(sessionNames []string) polecatSessionSet {
	sessions := make(polecatSessionSet, len(sessionNames))
	for _, sessionName := range sessionNames {
		rigName, polecatName, ok := parsePolecatSessionName(sessionName)
		if !ok {
			continue
		}
		sessions[polecatSessionKey(rigName, polecatName)] = sessionName
	}
	return sessions
}

func (s polecatSessionSet) lookup(rigName, polecatName string) (string, bool) {
	if s == nil {
		return "", false
	}
	sessionName, ok := s[polecatSessionKey(rigName, polecatName)]
	return sessionName, ok
}

// liveness translates this listing into the tri-state DecideWorkstate consumes.
//
// A nil set is the only thing that maps to Unknown, and that is precise rather
// than cautious: both production callers abort outright when `tmux
// list-sessions` fails, so a non-nil set is always a real enumeration and an
// absent polecat is a real absence. Nil reaches here only from a caller that
// never enumerated at all, and it must not be read as "every polecat is dead" —
// which is exactly what an empty map would say (gt-9f67).
func (s polecatSessionSet) presence(rigName, polecatName string) polecat.SessionPresence {
	if s == nil {
		return polecat.SessionPresenceUnknown
	}
	if _, running := s.lookup(rigName, polecatName); running {
		return polecat.SessionPresent
	}
	return polecat.SessionAbsent
}

func (s polecatSessionSet) namesForRig(rigName string) []string {
	if len(s) == 0 {
		return nil
	}
	var names []string
	for _, sessionName := range s {
		sessionRig, _, ok := parsePolecatSessionName(sessionName)
		if ok && sessionRig == rigName {
			names = append(names, sessionName)
		}
	}
	sort.Strings(names)
	return names
}

func polecatSessionKey(rigName, polecatName string) string {
	return rigName + polecatSessionKeySep + polecatName
}

// polecatBranchMRIndex answers "has this branch ever been submitted, and is an
// MR for it open right now" from a single merge-queue listing, so the inventory
// surface can consult the queue for a whole rig without one lookup per polecat.
//
// It exists because the agent bead's active_mr field is written by gt done and
// by nothing else. When someone rescues a stranded branch with `gt mq submit`,
// the owning polecat's field stays empty and the polecat keeps reporting
// SAFE_TO_NUKE with an open MR against a branch that recycling would delete
// (gt-46rk). The branch is the durable join key; the stored field is not.
type polecatBranchMRIndex struct {
	openMR    map[string]string       // branch -> ID of an open MR for that branch
	submitted map[string]bool         // branch -> an MR exists, open or closed
	byID      map[string]*beads.Issue // MR ID -> the MR bead, open or closed

	// bd resolves IDs the merge-queue listing does not cover — chiefly the
	// source issue of a terminal MR, which is what decides whether a recorded
	// active_mr is stale or still guarding unmerged work. It is nil in tests and
	// wherever the index was built by hand rather than read from a store.
	bd *beads.Beads
	// lookups memoises bd.Show across every polecat in the rig, so a shared
	// source issue costs one process call for the whole listing.
	lookups map[string]polecatMRLookup
}

type polecatMRLookup struct {
	issue *beads.Issue
	err   error
}

// newPolecatBranchMRIndex builds the index from one ListMergeRequests call.
// A nil index is valid and means "the queue was not consulted" — callers must
// treat that as unknown, never as proof that no MR exists.
func newPolecatBranchMRIndex(bd *beads.Beads) (*polecatBranchMRIndex, error) {
	issues, err := bd.ListMergeRequests(beads.ListOptions{Status: "all", Label: "gt:merge-request"})
	if err != nil {
		return nil, err
	}
	index := &polecatBranchMRIndex{
		openMR:    make(map[string]string),
		submitted: make(map[string]bool),
		byID:      make(map[string]*beads.Issue, len(issues)),
		bd:        bd,
	}
	for _, issue := range issues {
		if issue == nil || issue.ID == "" {
			continue
		}
		// Keyed by ID before the branch check: a recorded active_mr is looked up
		// by ID, and an MR whose description carries no branch still has a status
		// worth reporting.
		index.byID[issue.ID] = issue
		mrFields := beads.ParseMRFields(issue)
		if mrFields == nil {
			continue
		}
		branch := strings.TrimSpace(mrFields.Branch)
		if branch == "" {
			continue
		}
		index.submitted[branch] = true
		if issue.Status != "closed" && index.openMR[branch] == "" {
			index.openMR[branch] = issue.ID
		}
	}
	return index, nil
}

// polecatMRIssueReader answers active_mr classification lookups. MR beads come
// out of the queue listing the index already holds, so classifying the common
// case (an open MR) costs no extra process calls; anything the listing does not
// cover falls through to a memoised bd lookup.
type polecatMRIssueReader struct{ index *polecatBranchMRIndex }

func (r polecatMRIssueReader) Show(issueID string) (*beads.Issue, error) {
	index := r.index
	if index == nil {
		return nil, beads.ErrNotFound
	}
	if issue, ok := index.byID[issueID]; ok {
		return issue, nil
	}
	if index.bd == nil {
		// The listing was consulted and does not hold this ID, and there is no
		// second source to ask. Not-found is the honest answer; AssessActiveMR
		// fails closed on it.
		return nil, beads.ErrNotFound
	}
	if cached, ok := index.lookups[issueID]; ok {
		return cached.issue, cached.err
	}
	issue, err := index.bd.Show(issueID)
	if index.lookups == nil {
		index.lookups = make(map[string]polecatMRLookup)
	}
	index.lookups[issueID] = polecatMRLookup{issue: issue, err: err}
	return issue, err
}

// issueReader returns the reader AssessActiveMR should classify against, or nil
// when the queue was never consulted. Nil is load-bearing: AssessActiveMR
// reports status=unverified and stays blocking for a nil reader, which is the
// difference between "we looked and the MR is gone" and "we never looked".
func (i *polecatBranchMRIndex) issueReader() polecat.IssueReader {
	if i == nil {
		return nil
	}
	return polecatMRIssueReader{index: i}
}

func (i *polecatBranchMRIndex) hasMR(branch string) bool {
	if i == nil || branch == "" {
		return false
	}
	return i.submitted[branch]
}

func (i *polecatBranchMRIndex) openMRFor(branch string) string {
	if i == nil || branch == "" {
		return ""
	}
	return i.openMR[branch]
}

func buildPolecatInventoryItem(rigName, polecatName string, fields *beads.AgentFields, activeWork *beads.Issue, sessions polecatSessionSet, mrIndex *polecatBranchMRIndex) polecatInventoryItem {
	return buildPolecatInventoryItemFromEvidence(rigName, polecatName, fields, assessPolecatAssignedIssueWork(activeWork), sessions, mrIndex)
}

func buildPolecatInventoryItemFromEvidence(rigName, polecatName string, fields *beads.AgentFields, activeWorkEvidence polecatActiveWorkEvidence, sessions polecatSessionSet, mrIndex *polecatBranchMRIndex) polecatInventoryItem {
	sessionName, running := sessions.lookup(rigName, polecatName)
	item := polecatInventoryItem{
		Rig:            rigName,
		Name:           polecatName,
		State:          polecat.StateIdle,
		SessionRunning: running,
		SessionName:    sessionName,
	}

	input := polecat.WorkstateInput{
		State: polecat.StateIdle,
		// This surface already had the session listing — it is where
		// SessionRunning above comes from — and still classified without it,
		// because the classifier had no field to put it in. That is how a polecat
		// with no session read PENDING_MR / leave-alone here (gt-9f67).
		SessionPresence: sessions.presence(rigName, polecatName),
	}
	if fields != nil {
		item.CleanupStatus = strings.TrimSpace(fields.CleanupStatus)
		item.ActiveMR = strings.TrimSpace(fields.ActiveMR)
		item.Branch = strings.TrimSpace(fields.Branch)
		switch beads.AgentState(strings.TrimSpace(fields.AgentState)) {
		case beads.AgentStateDone:
			item.State = polecat.StateDone
		}
		input.CleanupStatus = polecat.CleanupStatus(item.CleanupStatus)
		input.PushFailed = fields.PushFailed
		input.MRFailed = fields.MRFailed
		input.MRRefused = fields.MRRefused
		input.Branch = item.Branch
		input.ActiveMR = item.ActiveMR
	}

	// This surface reads beads only — it never runs git, so it cannot know
	// whether unmerged commits remain and deliberately leaves MQCheckRequired
	// false rather than letting DecideWorkstate conclude "not_required" from
	// facts nobody gathered. MRSubmitted is the one merge-queue fact available
	// cheaply, and it is only ever used to CLEAR a recorded refusal, never to
	// assert that submission happened.
	//
	// ReuseFactsMeasured is left false for the same reason, and that is now the
	// operative difference between this surface and the reuse gate rather than a
	// silent one: an otherwise-unblocked polecat reports UNVERIFIED /
	// "idle-unverified" here instead of borrowing the gate's "idle-preserved".
	// Both strings used to come out of the same tail of DecideWorkstate, so a
	// polecat FindIdlePolecat refused for mq-not-submitted still listed as
	// reusable, and nothing said which surface had actually looked (gt-49dp).
	// `gt polecat check-recovery <rig>/<name>` is the surface that measures.
	input.MRSubmitted = mrIndex.hasMR(item.Branch)

	if !activeWorkEvidence.BlocksCleanup && fields != nil {
		activeWorkEvidence = assessPolecatAgentStateWork(beads.AgentState(strings.TrimSpace(fields.AgentState)))
	}

	if activeWorkEvidence.BlocksCleanup {
		item.Issue = activeWorkEvidence.AssignedIssue
		if activeWorkEvidence.RequiresRestart || activeWorkEvidence.CountsTowardCapacity {
			if running {
				item.State = polecat.StateWorking
			} else {
				item.State = polecat.StateStalled
			}
		} else if running && !polecat.CleanupStatus(item.CleanupStatus).IsSafe() {
			item.State = polecat.StateReviewNeeded
		}
		input.ActiveWorkBlocker = activeWorkEvidence.Blocker
		input.ActiveWorkCountsTowardCapacity = activeWorkEvidence.CountsTowardCapacity
		input.PausedAgentState = activeWorkEvidence.PausedAgentState
	} else if item.State == polecat.StateIdle && running && !polecat.CleanupStatus(item.CleanupStatus).IsSafe() {
		item.State = polecat.StateReviewNeeded
	}

	// Keyed on the blocker being empty rather than on BlocksCleanup: a pause
	// blocks cleanup without accounting for the hook, and the hook outranks it.
	// Under the old condition a stuck polecat's unaccounted hook went unreported.
	if fields != nil && input.ActiveWorkBlocker == "" {
		if hookBead := strings.TrimSpace(fields.HookBead); hookBead != "" {
			input.ActiveWorkBlocker = fmt.Sprintf("hook_bead=%s status=unverified", hookBead)
		}
	}
	// The recorded active_mr gets classified, not just echoed. This used to
	// format a literal `status=unknown` for every polecat carrying the field —
	// 19 of 19 town-wide — so a pointer at an MR that closed a day ago was
	// indistinguishable at a glance from one filed seconds ago, and both read
	// PENDING_MR forever (gt-hx10). AssessActiveMR is the same fail-closed
	// classifier the recovery and reuse paths use: a terminal MR only stops
	// blocking when its source issue is proven terminal too, and an unconsulted
	// queue stays blocking as status=unverified.
	//
	// openMRProven tracks the STRONG half of ActiveMRBlocker: an MR bead that was
	// looked up and read back open. ActiveMRBlocker is deliberately fail-closed
	// and also carries status=unverified and lookup_error, which prove nothing —
	// so the handed-off promotion below keys on this instead (gt-mkpm).
	openMRProven := false
	if item.ActiveMR != "" {
		assessment := polecat.AssessActiveMR(mrIndex.issueReader(), polecat.ActiveMRInput{
			ActiveMR:        item.ActiveMR,
			SourceIssueHint: polecatActiveMRSourceHint(fields),
		})
		if assessment.Pending {
			input.ActiveMRBlocker = assessment.Reason
			openMRProven = !assessment.Stale && assessment.MRStatus != ""
		}
	}
	if input.ActiveMRBlocker == "" {
		if openMR := mrIndex.openMRFor(item.Branch); openMR != "" {
			openMRProven = true
			// An open MR nobody recorded on this agent bead — the rescue-submit case
			// (gt-46rk). The branch it points at is the only copy of that work, and
			// the polecat would otherwise read SAFE_TO_NUKE right up until recycling
			// force-deleted the branch out from under the queue. This also catches
			// the polecat whose recorded active_mr just proved stale while a live MR
			// for its branch exists under a different ID.
			item.ActiveMR = openMR
			input.ActiveMR = openMR
			input.ActiveMRBlocker = "active_mr=" + openMR + " status=open source=branch-index"
		}
	}

	// A dead session with work still attached is "stalled" only until you ask
	// whether the work is in the queue. This surface has the answer already — it
	// indexed the rig's merge requests once for the whole listing — so ask before
	// printing the word for the failure case over a polecat that succeeded
	// (gt-mkpm).
	item.State = polecat.HandedOffState(item.State, openMRProven)

	input.State = item.State
	item.Disposition = polecat.DecideWorkstate(input)
	return item
}

var polecatSummaryWorkStatuses = []beads.IssueStatus{
	beads.IssueStatusHooked,
	beads.StatusInProgress,
	beads.StatusOpen,
	beads.StatusBlocked,
	beads.StatusDeferred,
}

var polecatSummaryWorkStatusRank = func() map[string]int {
	ranks := make(map[string]int, len(polecatSummaryWorkStatuses))
	for i, status := range polecatSummaryWorkStatuses {
		ranks[string(status)] = i
	}
	return ranks
}()

func listActivePolecatWorkByName(bd *beads.Beads, rigName string) (map[string]*beads.Issue, error) {
	byName := make(map[string]*beads.Issue)
	issues, err := bd.ListIssueStatuses(polecatSummaryWorkStatuses...)
	if err != nil {
		return nil, err
	}
	for _, issue := range issues {
		evidence := assessPolecatAssignedIssueWork(issue)
		if !evidence.BlocksCleanup {
			continue
		}
		name, ok := polecatNameFromAssignee(rigName, issue.Assignee)
		if !ok {
			continue
		}
		if current := byName[name]; current == nil || polecatSummaryIssueRank(issue) < polecatSummaryIssueRank(current) {
			byName[name] = issue
		}
	}
	return byName, nil
}

func polecatSummaryIssueRank(issue *beads.Issue) int {
	if issue == nil {
		return len(polecatSummaryWorkStatuses)
	}
	if rank, ok := polecatSummaryWorkStatusRank[issue.Status]; ok {
		return rank
	}
	return len(polecatSummaryWorkStatuses)
}

func polecatNameFromAssignee(rigName, assignee string) (string, bool) {
	prefix := rigName + "/polecats/"
	if !strings.HasPrefix(assignee, prefix) {
		return "", false
	}
	name := strings.TrimPrefix(assignee, prefix)
	if name == "" || strings.Contains(name, "/") {
		return "", false
	}
	return name, true
}

func assessPolecatAssignedIssueWork(issue *beads.Issue) polecatActiveWorkEvidence {
	if issue == nil || beads.IsAgentBead(issue) || beads.IsProtectedBead(issue) || beads.IssueStatus(issue.Status).IsTerminal() {
		return polecatActiveWorkEvidence{}
	}
	requiresRestart := polecatSummaryIssueRequiresRestart(beads.IssueStatus(issue.Status))
	return polecatActiveWorkEvidence{
		BlocksCleanup:        true,
		RequiresRestart:      requiresRestart,
		CountsTowardCapacity: requiresRestart,
		Blocker:              fmt.Sprintf("assigned_work=%s status=%s", issue.ID, issue.Status),
		AssignedIssue:        issue.ID,
	}
}

func polecatSummaryIssueRequiresRestart(status beads.IssueStatus) bool {
	switch status {
	case beads.IssueStatusHooked, beads.StatusInProgress, beads.StatusOpen:
		return true
	default:
		return false
	}
}

func assessPolecatAgentStateWork(state beads.AgentState) polecatActiveWorkEvidence {
	if state == "" || state == beads.AgentStateIdle || state == beads.AgentStateDone || state == beads.AgentStateNuked {
		return polecatActiveWorkEvidence{}
	}
	if state.IsActive() {
		return polecatActiveWorkEvidence{
			BlocksCleanup:        true,
			RequiresRestart:      true,
			CountsTowardCapacity: true,
			Blocker:              fmt.Sprintf("agent_state=%s", state),
		}
	}
	if state.IsPaused() {
		return polecatActiveWorkEvidence{
			BlocksCleanup:    true,
			PausedAgentState: string(state),
		}
	}
	return polecatActiveWorkEvidence{}
}

func polecatActiveWorkLookupError(err error) polecatActiveWorkEvidence {
	if err == nil {
		return polecatActiveWorkEvidence{}
	}
	return polecatActiveWorkEvidence{
		BlocksCleanup: true,
		Blocker:       fmt.Sprintf("assigned_work status=lookup_error: %v", err),
	}
}

// polecatActiveMRSourceHint supplies the source issue AssessActiveMR needs when
// a terminal MR bead no longer carries one — either because the MR was reaped
// or because its description predates the field. last_source_issue survives the
// hook being cleared, which is exactly when active_mr is set, so it is the
// better hint of the two.
func polecatActiveMRSourceHint(fields *beads.AgentFields) string {
	if fields == nil {
		return ""
	}
	if source := strings.TrimSpace(fields.LastSourceIssue); source != "" {
		return source
	}
	return strings.TrimSpace(fields.HookBead)
}

func parsePolecatAgentFields(issue *beads.Issue) *beads.AgentFields {
	if issue == nil {
		return nil
	}
	fields := beads.ParseAgentFields(issue.Description)
	fields.AgentState = beads.ResolveAgentState(issue.Description, issue.AgentState)
	return fields
}
