package ui

// agentLines is the focused agent's full scrollback, thinking included. These are the forensic views:
// the combined log answers what is happening, a pane answers what one agent actually did.
func (m Model) agentLines() []string {
	a := m.focused()
	if a == nil {
		return []string{"no such agent"}
	}
	if len(a.lines) == 0 {
		return []string{"waiting for " + a.spec.Name + "..."}
	}
	return a.lines
}

// focused is the agent behind the focused tab, nil on tab 0 or a tab past the roster.
func (m Model) focused() *agentState {
	i := m.view.tab - 1
	if i < 0 || i >= len(m.agents) {
		return nil
	}
	return m.agents[i]
}
