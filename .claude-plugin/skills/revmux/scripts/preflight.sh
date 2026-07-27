#!/usr/bin/env bash
# preflight.sh - verify revmux and the model CLIs a profile actually needs.
#
# revmux drives `claude` and `codex` as subprocesses, so a missing binary is a run that
# starts, launches agents, and degrades every source before failing with exit 2. Checking
# first turns that into one line of output.
#
# which executors are needed depends on the resolved profile, not on a fixed list: a roster
# entry or a stage declares `executor: codex`, and a user override can change that. The
# authority is `revmux config`, which reports what resolved rather than what ships.
#
# usage: preflight.sh [profile]
#   profile  profile to check; defaults to whatever revmux resolves as its default
#
# output: one `key: value` line per check, then `ok: true|false`
# exit:   0 all good, 1 something missing

set -u

profile="${1:-}"
ok=true

fail() {
    echo "$1"
    ok=false
}

if ! command -v revmux >/dev/null 2>&1; then
    echo "revmux: MISSING"
    echo "install: go install github.com/umputun/revmux/app@latest (binary lands as 'app', rename it)"
    echo "     or: git clone https://github.com/umputun/revmux.git && cd revmux && make install"
    echo "ok: false"
    exit 1
fi
echo "revmux: $(command -v revmux) ($(revmux --version 2>/dev/null || echo 'version unknown'))"

# `revmux config` resolves the whole tree: profiles, rosters, stages and the executor
# vocabulary. It runs no pipeline and writes nothing, so it is safe to call for a probe.
cfg=$(revmux config 2>/dev/null) || {
    fail "config: FAILED - revmux could not resolve its configuration"
    echo "hint: run 'revmux config' directly to see the error"
    echo "ok: false"
    exit 1
}

if command -v jq >/dev/null 2>&1; then
    if [ -n "$profile" ]; then
        known=$(printf '%s' "$cfg" | jq -r --arg p "$profile" '[.profiles[].name] | index($p) // "null"')
        if [ "$known" = "null" ]; then
            fail "profile: UNKNOWN '$profile'"
            echo "available: $(printf '%s' "$cfg" | jq -r '[.profiles[].name] | join(", ")')"
            echo "ok: false"
            exit 1
        fi
        echo "profile: $profile"
        # a stage runs on every review regardless of the roster, so its executor counts too
        executors=$(printf '%s' "$cfg" | jq -r --arg p "$profile" \
            '[(.profiles[] | select(.name == $p) | .roster[].executor), (.stages[].executor)] | unique | .[]')
    else
        echo "profile: (default - checking every executor any profile or stage uses)"
        executors=$(printf '%s' "$cfg" | jq -r '[(.profiles[].roster[].executor), (.stages[].executor)] | unique | .[]')
    fi
else
    # without jq the roster cannot be read, so fall back to the full vocabulary. Over-checking
    # is the safe direction: it can only report a binary the run would not have needed.
    echo "jq: MISSING - checking every known executor instead of the profile's own"
    executors="claude
codex"
fi

for exe in $executors; do
    if command -v "$exe" >/dev/null 2>&1; then
        echo "$exe: $(command -v "$exe")"
    else
        fail "$exe: MISSING - required by this profile's roster or a stage"
    fi
done

echo "ok: $ok"
[ "$ok" = true ] || exit 1
