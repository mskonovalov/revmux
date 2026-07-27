#!/usr/bin/env bash
# task-state.sh - report what a task directory already holds, before writing into it.
#
# Two things have to be known before composing an invocation, and both are easy to get wrong:
#
#   1. where the tasks root actually is. It defaults to ./.revmux/tasks but is a config knob,
#      so a project or user config can move it. Hardcoding the default writes the task
#      somewhere revmux will not look. `revmux config` reports the resolved path.
#   2. which run names are taken. A --run name that already exists is a load-time error, not
#      an overwrite - deliberately, because a round that went badly is the one worth keeping.
#
# Everything above runs/ is caller-owned and revmux never touches it, so an existing
# scope.md is a decision someone made and must not be clobbered blind.
#
# usage: task-state.sh <task-id>
#
# output: `key: value` lines
#   tasks_dir   resolved root
#   task_dir    this task's directory
#   exists      true|false
#   scope       present|MISSING   (required; missing or empty is a load-time error)
#   goal        present|absent    (optional)
#   profile     present|absent    (optional)
#   context     <n> files|absent  (optional)
#   runs        space-separated existing run names, or `none`
# exit: 0 always when revmux resolves; 1 if revmux is unavailable or config fails

set -u

task="${1:-}"
if [ -z "$task" ]; then
    echo "usage: task-state.sh <task-id>" >&2
    exit 1
fi

# same rules as revmux's own options.checkName. Rejecting here means a bad id is caught before the
# caller composes scope.md/goal.md/profile.md into a path revmux will refuse at load. A branch name
# is the common trigger: `feature/foo` contains a separator.
case "$task" in
    /*)     echo "error: task id \"$task\" is absolute" >&2; exit 1 ;;
    .*)     echo "error: task id \"$task\" starts with a dot" >&2; exit 1 ;;
    */*|*\\*) echo "error: task id \"$task\" contains a path separator; replace / with -" >&2; exit 1 ;;
    *..*)   echo "error: task id \"$task\" references a parent directory" >&2; exit 1 ;;
esac

command -v revmux >/dev/null 2>&1 || { echo "error: revmux not on PATH" >&2; exit 1; }

cfg=$(revmux config 2>/dev/null) || { echo "error: revmux config failed" >&2; exit 1; }

if command -v jq >/dev/null 2>&1; then
    tasks_dir=$(printf '%s' "$cfg" | jq -r '.paths.tasks_dir')
else
    # crude but dependency-free: the value of the tasks_dir key, quotes stripped
    tasks_dir=$(printf '%s' "$cfg" | tr ',' '\n' | grep '"tasks_dir"' | head -1 | sed 's/.*"tasks_dir"[[:space:]]*:[[:space:]]*"//; s/".*//')
fi

[ -n "$tasks_dir" ] || { echo "error: could not resolve tasks_dir from revmux config" >&2; exit 1; }

task_dir="$tasks_dir/$task"
echo "tasks_dir: $tasks_dir"
echo "task_dir: $task_dir"

if [ ! -d "$task_dir" ]; then
    echo "exists: false"
    echo "scope: MISSING"
    echo "goal: absent"
    echo "profile: absent"
    echo "context: absent"
    echo "runs: none"
    exit 0
fi

echo "exists: true"

# an empty scope.md fails the run exactly like a missing one, so size is what matters
if [ -s "$task_dir/scope.md" ]; then echo "scope: present"; else echo "scope: MISSING"; fi
[ -s "$task_dir/goal.md" ] && echo "goal: present" || echo "goal: absent"
[ -s "$task_dir/profile.md" ] && echo "profile: present" || echo "profile: absent"

if [ -d "$task_dir/context" ]; then
    n=$(find "$task_dir/context" -type f 2>/dev/null | wc -l | tr -d ' ')
    [ "$n" -gt 0 ] && echo "context: $n files" || echo "context: absent"
else
    echo "context: absent"
fi

runs=""
if [ -d "$task_dir/runs" ]; then
    # newest first, so the most recent round is the one a reader sees at the front. `ls -t` is safe
    # on these specifically: revmux validates a --run name at load and rejects path separators, `..`
    # and absolute paths, so a run directory is always a plain single-line basename.
    # shellcheck disable=SC2012
    runs=$(ls -t "$task_dir/runs" 2>/dev/null | tr '\n' ' ' | sed 's/ *$//')
fi
[ -n "$runs" ] && echo "runs: $runs" || echo "runs: none"
