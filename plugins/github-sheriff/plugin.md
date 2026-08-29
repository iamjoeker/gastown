+++
name = "github-sheriff"
description = "Monitor GitHub CI checks on open PRs and create beads for failures"
version = 1

[gate]
type = "cooldown"
duration = "2h"

[tracking]
labels = ["plugin:github-sheriff", "category:ci-monitoring"]
digest = true

[execution]
timeout = "2m"
notify_on_failure = true
severity = "low"
+++

# GitHub Sheriff

Polls GitHub for open pull requests, categorizes them by readiness, and creates
`ci-failure` beads for new failures. Implements the PR Sheriff pattern from the
[Gas Town User Manual](https://steve-yegge.medium.com/gas-town-emergency-user-manual-cf0e4556d74b)
as a Deacon plugin.

Categorizes each PR as:
- **Easy win**: CI passing, small (<200 LOC changed), no merge conflicts
- **Needs review**: CI failing, large, or has conflicts

Requires: `gh` CLI installed and authenticated (`gh api user`).

## Detection

Verify `gh` is available and authenticated.

**Do not use `gh auth status` as the predicate (gt-zp1q).** It was measured on
this host *exiting 0* while printing `The token in default is invalid.`, so
`gh auth status; if [ $? -ne 0 ]` reads "authenticated" for the entire duration
of an outage — and the `2>/dev/null` this block used to carry threw away the one
place the failure was stated. An expired token survived 17 consecutive merges
that way.

Ask for the capability instead of for a status report about it. `gh api user`
uses the same credential on the same path as every call below, returns 401 and
exits nonzero on an invalid token, and cannot drift from what this plugin needs:

```bash
# stderr is captured, not discarded: "produced nothing" and "never ran" are
# indistinguishable otherwise, and the second is the more common one.
if ! GH_LOGIN=$(gh api user --jq .login 2>&1) || [ -z "$GH_LOGIN" ]; then
  echo "SKIP: gh CLI cannot authenticate to GitHub: $GH_LOGIN"
  exit 0
fi
echo "gh authenticated as $GH_LOGIN"
```

The `-z` arm matters as much as the exit status: a `gh` that exits 0 while
returning no login has not demonstrated anything, and treating exit 0 alone as
proof is the defect being fixed.

Detect the repo from the rig's git remote. On fork-backed rigs, `origin` is
the fork used for pushes, and the canonical repo lives on the `upstream`
remote — prefer it so PRs are read from upstream, not the fork (gt-ca4j).
Fall back to `origin` when no `upstream` remote is configured, and to
explicit config if detection fails entirely:

```bash
REPO_URL=$(git -C "$GT_RIG_ROOT" remote get-url upstream 2>/dev/null)
if [ -z "$REPO_URL" ]; then
  REPO_URL=$(git -C "$GT_RIG_ROOT" remote get-url origin 2>/dev/null)
fi
REPO=$(echo "$REPO_URL" | sed -E 's|.*github\.com[:/]||; s|\.git$||')

if [ -z "$REPO" ]; then
  echo "SKIP: could not detect GitHub repo from rig remote"
  exit 0
fi
```

## Action

### Step 1: List open PRs with full details

Fetch all open PRs in a single GraphQL call via `gh`. This returns additions,
deletions, mergeable status, and CI check results without per-PR API overhead:

```bash
SINCE=$(date -d '7 days ago' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -v-7d +%Y-%m-%dT%H:%M:%SZ)
PRS=$(gh pr list --repo "$REPO" --state open \
  --json number,title,author,additions,deletions,mergeable,statusCheckRollup,url,updatedAt \
  --limit 100 | jq --arg since "$SINCE" '[.[] | select(.updatedAt >= $since)]')

PR_COUNT=$(echo "$PRS" | jq length)
if [ "$PR_COUNT" -eq 0 ]; then
  echo "No open PRs found for $REPO"
  exit 0
fi
```

### Step 2: Categorize each PR

Process each PR using process substitution (not a pipe) so array modifications
persist after the loop:

```bash
EASY_WINS=()
NEEDS_REVIEW=()
FAILURES=()

while IFS= read -r PR_JSON; do
  [ -z "$PR_JSON" ] && continue

  PR_NUM=$(echo "$PR_JSON" | jq -r '.number')
  PR_TITLE=$(echo "$PR_JSON" | jq -r '.title')
  AUTHOR=$(echo "$PR_JSON" | jq -r '.author.login')
  ADDITIONS=$(echo "$PR_JSON" | jq -r '.additions // 0')
  DELETIONS=$(echo "$PR_JSON" | jq -r '.deletions // 0')
  MERGEABLE=$(echo "$PR_JSON" | jq -r '.mergeable')
  TOTAL_CHANGES=$((ADDITIONS + DELETIONS))

  # Determine CI status from statusCheckRollup
  TOTAL_CHECKS=$(echo "$PR_JSON" | jq '.statusCheckRollup | length')
  PASSING_CHECKS=$(echo "$PR_JSON" | jq '[.statusCheckRollup[] | select(
    .conclusion == "SUCCESS" or .conclusion == "NEUTRAL" or
    .conclusion == "SKIPPED" or .state == "SUCCESS"
  )] | length')

  if [ "$TOTAL_CHECKS" -gt 0 ] && [ "$TOTAL_CHECKS" -eq "$PASSING_CHECKS" ]; then
    CI_PASS=true
  else
    CI_PASS=false
  fi

  # Collect individual check failures for bead creation
  while IFS= read -r CHECK; do
    [ -z "$CHECK" ] && continue
    CHECK_NAME=$(echo "$CHECK" | jq -r '.name')
    CHECK_URL=$(echo "$CHECK" | jq -r '.detailsUrl // .targetUrl // empty')
    FAILURES+=("$PR_NUM|$PR_TITLE|$CHECK_NAME|$CHECK_URL")
  done < <(echo "$PR_JSON" | jq -c '.statusCheckRollup[] | select(
    .conclusion == "FAILURE" or .conclusion == "CANCELLED" or
    .conclusion == "TIMED_OUT" or .state == "FAILURE" or .state == "ERROR"
  )')

  # Categorize PR
  if [ "$MERGEABLE" = "MERGEABLE" ] && [ "$CI_PASS" = true ] && [ "$TOTAL_CHANGES" -lt 200 ]; then
    EASY_WINS+=("PR #$PR_NUM: $PR_TITLE (by $AUTHOR, +$ADDITIONS/-$DELETIONS)")
  else
    REASONS=""
    [ "$MERGEABLE" != "MERGEABLE" ] && REASONS+="conflicts "
    [ "$CI_PASS" != true ] && REASONS+="ci-failing "
    [ "$TOTAL_CHANGES" -ge 200 ] && REASONS+="large(${TOTAL_CHANGES}loc) "
    NEEDS_REVIEW+=("PR #$PR_NUM: $PR_TITLE (by $AUTHOR, ${REASONS% })")
  fi
done < <(echo "$PRS" | jq -c '.[]')

# Report categorized PRs
if [ ${#EASY_WINS[@]} -gt 0 ]; then
  echo "Easy wins (${#EASY_WINS[@]}):"
  printf '  %s\n' "${EASY_WINS[@]}"
fi
if [ ${#NEEDS_REVIEW[@]} -gt 0 ]; then
  echo "Needs review (${#NEEDS_REVIEW[@]}):"
  printf '  %s\n' "${NEEDS_REVIEW[@]}"
fi
```

## Record Result

```bash
SUMMARY="$REPO: $PR_COUNT PRs — ${#EASY_WINS[@]} easy win(s), ${#NEEDS_REVIEW[@]} need review, ${#FAILURES[@]} CI failure(s) detected"
echo "$SUMMARY"
```

On success:
```bash
gt plugin record-run --plugin github-sheriff --result success \
  --title "github-sheriff: $SUMMARY" --description "$SUMMARY" >/dev/null 2>&1 || true
```

On failure:
```bash
gt plugin record-run --plugin github-sheriff --result failure \
  --title "github-sheriff: FAILED" \
  --description "GitHub sheriff failed: $ERROR" >/dev/null 2>&1 || true

gt escalate "Plugin FAILED: github-sheriff" \
  --severity low \
  --reason "$ERROR"
```
