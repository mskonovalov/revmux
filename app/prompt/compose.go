package prompt

import (
	"fmt"
	"strings"
)

// historyGuard closes the injected prior-round block. It lives here rather than with whatever builds
// the inventory because the data and the guard must not be separable: an agent told a prior round
// flagged something tends to confirm it rather than judge it, which is the anchoring failure that
// makes codex a peer rather than a second pass.
const historyGuard = `Each round holds report.md (rendered) and findings.json (machine shape). Read the rounds you judge relevant.

Re-evaluate everything independently. A prior round reporting an issue is not evidence that it is real,
and a prior round missing one is not evidence that it is absent.`

// Vars holds the {{VAR}} substitutions for one composed prompt. Context variables carry absolute
// paths, never file contents, and an absent one carries the caller's "none provided" placeholder —
// Compose never invents one.
type Vars map[string]string

// ComposeOpts is everything composition needs beyond the roster entry.
//
// History is the prior-round inventory only: the task directory plus one generated line per round.
// Compose appends the independence guard itself, so no caller can supply the data without it, and
// an empty History omits the block entirely rather than injecting it empty.
type ComposeOpts struct {
	Vars    Vars
	History string
}

// Compose builds one agent's prompt: the profile body, then every lens the entry carries, then
// substitution, then the injected history. Composition hangs off Profile because an AgentSpec alone
// carries no body.
func (p *Profile) Compose(set *Set, spec AgentSpec, opts ComposeOpts) (string, error) {
	parts := make([]string, 0, len(spec.Lenses)+1)
	if p.Body != "" {
		parts = append(parts, p.Body)
	}
	for _, name := range spec.Lenses {
		body, err := set.lens(name)
		if err != nil {
			return "", fmt.Errorf("agent %s: %w", spec.Name, err)
		}
		if body != "" {
			parts = append(parts, body)
		}
	}

	out, err := opts.render(strings.Join(parts, "\n\n"))
	if err != nil {
		return "", fmt.Errorf("agent %s: %w", spec.Name, err)
	}
	return out, nil
}

// Compose builds a stage's prompt. A stage has no roster, so it needs no AgentSpec.
func (st *Stage) Compose(opts ComposeOpts) (string, error) {
	out, err := opts.render(st.Body)
	if err != nil {
		return "", fmt.Errorf("stage %s: %w", st.Name, err)
	}
	return out, nil
}

func (o ComposeOpts) render(body string) (string, error) {
	out, err := o.substitute(body)
	if err != nil {
		return "", err
	}
	if o.History == "" {
		return out, nil
	}
	return out + "\n\n" + strings.TrimSpace(o.History) + "\n\n" + historyGuard, nil
}

// substitute replaces every known variable and fails on anything left over. A leftover can only be a
// bug by this point: an unknown name failed at load, and an absent file arrives as a placeholder.
//
// The miss is recorded during the pass rather than found by re-scanning the result, because a
// substituted value is not part of the prompt's own vocabulary. {{FINDINGS}} carries model-authored
// review text that routinely quotes template syntax, and re-scanning would read a finding about an
// unescaped {{message}} as this prompt's own unresolved variable and fail the whole run.
func (o ComposeOpts) substitute(body string) (string, error) {
	missing := ""
	out := varPattern.ReplaceAllStringFunc(body, func(m string) string {
		v, ok := o.Vars[m[2:len(m)-2]]
		if !ok {
			if missing == "" {
				missing = m
			}
			return m
		}
		return v
	})
	if missing != "" {
		return "", fmt.Errorf("unresolved variable %s", missing)
	}
	return out, nil
}
