#!/usr/bin/env bash
# PostToolUse guard: keep the workflow's job names in step with the branch
# ruleset's required status checks.
#
# The ruleset names required checks as literal strings ("lint", "test",
# "integration", "security"), and those strings are job ids in lint-and-test.yml.
# Nothing in the repo connects the two. Rename a job and GitHub keeps waiting for
# a context that will never report again, so pull requests hang forever rather
# than failing — the loudest possible symptom with the quietest possible cause.
set -uo pipefail
cd "${CLAUDE_PROJECT_DIR:-.}" || exit 0

f=$(jq -r '.tool_input.file_path // empty')
case "$f" in
*.github/workflows/lint-and-test.yml) ;;
*) exit 0 ;;
esac

# The ruleset is the source of truth but it lives on GitHub, so this needs the
# network. Skip rather than block an edit when gh is absent, unauthenticated, or
# offline — a guard that fails closed on a flaky connection is worse than none.
required=$(gh api "repos/{owner}/{repo}/rules/branches/main" \
	--jq '.[] | select(.type=="required_status_checks")
	          | .parameters.required_status_checks[].context' 2>/dev/null | sort)
[ -n "$required" ] || exit 0

jobs=$(python3 -c '
import sys, yaml
print("\n".join(yaml.safe_load(open(sys.argv[1]))["jobs"]))' "$f" 2>/dev/null | sort)
[ -n "$jobs" ] || exit 0

missing=$(comm -23 <(echo "$required") <(echo "$jobs"))
if [ -n "$missing" ]; then
	echo "Branch ruleset requires status checks that no job in $f produces: ${missing//$'\n'/, }" >&2
	echo "Jobs present: ${jobs//$'\n'/, }" >&2
	echo "Restore the job id, or update the ruleset's required checks to match. Left as is," >&2
	echo "every pull request waits forever for a check that nothing will ever report." >&2
	exit 2
fi
