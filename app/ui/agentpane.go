package ui

// agentLines is the focused agent's full scrollback. These are the forensic views:
// the combined log answers what is happening, a pane answers what one agent actually did.
func (m Model) agentLines() []string {
	a := m.focused()
	if a == nil {
		return []string{"no such agent"}
	}
	if len(a.lines) == 0 {
		return []string{"waiting for " + a.spec.Name + "..."}
	}

	// wrapped and rendered exactly as the combined log is. A model writes markdown into its prose and
	// the lines it writes are long; a forensic view that clips them is the one place a reader has gone
	// looking for the detail, so it is the last place to throw the end of it away.
	out := make([]string, 0, len(a.lines))
	for _, e := range a.lines {
		head := m.style.muted.Render(e.at.Format(timeFormat)) + " "
		out = append(out, m.wrap(head, markdown(e.text))...)
	}
	return out
}

// focused is the agent behind the focused tab, nil on tab 0 or a tab past the roster.
func (m Model) focused() *agentState {
	i := m.view.tab - 1
	if i < 0 || i >= len(m.agents) {
		return nil
	}
	return m.agents[i]
}
