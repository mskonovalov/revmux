# shellcheck shell=bash
# revmux - tmux window backend. SOURCED by launch-revmux.sh (never run standalone).
#
# Runs revmux in a server-owned tmux *window* instead of a `display-popup`. Two triggers:
#   - agent-deck auto-detection - a background window that never steals focus, and
#   - explicit REVMUX_TMUX_WINDOW=1 - disconnect-resilient interactive review: the window opens
#     focused and the previously active window is restored on exit.
#
# A server-owned window survives a client disconnect (SSH drop, VPN expiry) with zero loss -
# reattaching brings the still-running review back, which a `display-popup` cannot do. That matters
# more for revmux than for a quick diff read: a review runs for minutes, and losing the client
# partway through would otherwise throw away the whole run.
#
# Why the agent-deck trigger exists: agent-deck (https://github.com/asheshgoplani/agent-deck)
# renders each of its sessions through a tmux *control-mode* client, which does NOT render
# `tmux display-popup` at all - the popup is invisible and `display-popup -E` blocks forever, so the
# agent waits on a review the human can never see. agent-deck does surface real tmux windows, so
# under agent-deck revmux runs in a background window named after the task under review. It appears
# in the session tree to switch to when convenient and never pops over another session.
#
# Activation:
#   REVMUX_TMUX_WINDOW=1   force window mode, focused + restore prior window on exit (any tmux)
#   REVMUX_TMUX_WINDOW=0   force the popup path (skip this backend)
#   unset                  auto: background window mode only when agent-deck is detected
#
# Reuses from the caller (launch-revmux.sh): TMPBASE, CWD, DIR_NAME, TITLE_TASK and the helpers
# sq() / write_rc_cmd() / read_rc() / print_report_and_exit(). The caller guarantees $TMUX is set
# and tmux is on PATH before sourcing this. This file is SOURCED, so it must not install an EXIT
# trap - that would clobber the caller's cleanup. It cleans up explicitly instead, and either
# returns (window mode off, caller falls through to the popup) or exits the process.

# _rm_focus marks the interactive opt-in, set only when the user explicitly forces window mode.
# Capture it BEFORE the auto-detection below folds user-opt and agent-deck into one _rm_winmode=1:
# the focus + restore behavior is opt-in only, since agent-deck's whole point is not stealing focus.
_rm_focus=0
[ "${REVMUX_TMUX_WINDOW:-}" = 1 ] && _rm_focus=1

_rm_winmode="${REVMUX_TMUX_WINDOW:-}"
if [ -z "$_rm_winmode" ]; then
    # agent-deck markers: its env var (also mirrored into the tmux session env), with the
    # agentdeck_* session-name prefix as a fallback signal
    if [ -n "${AGENTDECK_INSTANCE_ID:-}" ] \
        || tmux show-environment AGENTDECK_INSTANCE_ID >/dev/null 2>&1 \
        || tmux display-message -p '#{session_name}' 2>/dev/null | grep -q '^agentdeck_'; then
        _rm_winmode=1
    else
        _rm_winmode=0
    fi
fi
# not window mode → return to the launcher, which uses the popup path. Returning here, before any
# trap or sentinel work, leaves the caller's environment untouched.
[ "$_rm_winmode" = 1 ] || return 0

_rm_winname="revmux: ${DIR_NAME}${TITLE_TASK:+ [$TITLE_TASK]}"

_rm_sentinel=$(mktemp "$TMPBASE/revmux-done-XXXXXX")
rm -f "$_rm_sentinel"

# interactive mode: remember the active window so it can be restored after the review
_rm_prevwin=""
if [ "$_rm_focus" = 1 ]; then
    _rm_prevwin=$(tmux display-message -p '#{window_id}' 2>/dev/null || true)
fi

# open the review in a background window (-d: do not steal the active window; -c: start dir).
# -P -F prints the new window id so it can be watched. Mirror the popup path's `sh -c "$REVMUX_CMD"`
# invocation - every backend runs the command through sh, and REVMUX_CMD is built sh-compatible.
# If tmux cannot create the window, fail loudly rather than busy-waiting on a sentinel that will
# never appear.
if ! _rm_winid=$(tmux new-window -d -P -F '#{window_id}' -c "$CWD" -n "$_rm_winname" \
        -- sh -c "$(write_rc_cmd "$_rm_sentinel")"); then
    rm -f "$_rm_sentinel" "$_rm_sentinel".tmp
    echo "revmux: failed to open tmux review window" >&2
    exit 1
fi

if [ "$_rm_focus" = 1 ]; then
    tmux select-window -t "$_rm_winid" 2>/dev/null || true
fi

# Wait for the review to finish. The sentinel carries revmux's exit code, written before the inner
# shell exits, so it exists by the time the window closes on a normal finish.
#
# **The wait is bounded on the window still existing, not on a timer.** A review legitimately runs
# for many minutes, so no timeout is safe; but if the window disappears without a sentinel - killed,
# or tmux died - a timer-free loop would hang forever. Watching the window turns that into an exit.
while [ ! -f "$_rm_sentinel" ]; do
    tmux list-windows -F '#{window_id}' 2>/dev/null | grep -qxF "$_rm_winid" || break
    sleep 0.3
done

# restore the window that was active before the review took focus (interactive mode only)
if [ "$_rm_focus" = 1 ] && [ -n "$_rm_prevwin" ]; then
    tmux select-window -t "$_rm_prevwin" 2>/dev/null || true
fi

_rm_rc=1
[ -f "$_rm_sentinel" ] && _rm_rc=$(read_rc "$_rm_sentinel")
rm -f "$_rm_sentinel" "$_rm_sentinel".tmp
print_report_and_exit "${_rm_rc:-1}"
