package pipeline

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/umputun/revmux/app/executor"
	"github.com/umputun/revmux/app/finding"
)

// save writes one whole artifact — a composed prompt or a stage snapshot — and folds any failure into
// the sticky archive error instead of returning it. An archive gap fails the whole run rather than
// degrading the one stage that hit it, since a report next to a half-written archive reads as complete.
//
// events.jsonl does not come through here: it is a stream written from emit as the run makes decisions,
// held open under the same mutex that guards it.
func (p *Pipeline) save(name string, data []byte) {
	w, err := p.cfg.Archive.Writer(name)
	if err != nil {
		p.fail(fmt.Errorf("open %s: %w", name, err))
		return
	}
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		p.fail(fmt.Errorf("write %s: %w", name, err))
		return
	}
	if err := w.Close(); err != nil {
		p.fail(fmt.Errorf("close %s: %w", name, err))
	}
}

// archivedPrompt is the prompt the process actually receives, which is the only version worth storing.
// Each executor appends something of its own to the composed text: codex its output contract, since it
// has no --json-schema, and claude its narration contract, since --json-schema otherwise leaves it
// with nothing to say. A prompt archived as composed would be missing whichever one the run used, and
// describe a review that did not happen.
//
// It is package-level rather than a method because all three stages call it, each with its own schema.
func archivedPrompt(exec, text string, schema json.RawMessage) string {
	if exec != executorCodex {
		return text + executor.ClaudeNarrationContract(schema)
	}
	return text + executor.CodexOutputContract(schema)
}

// saveStage snapshots the findings as one stage left them, so what synthesis merged and what verify
// rejected is directly visible rather than reconstructed from the raw streams — impossible anyway for
// a source that degraded.
func (p *Pipeline) saveStage(name string, rep finding.Report) {
	buf := &bytes.Buffer{}
	if err := rep.JSON(buf); err != nil {
		p.fail(fmt.Errorf("encode %s: %w", name, err))
		return
	}
	p.save(name, buf.Bytes())
}

// fail records the first archive failure. Later ones are dropped: the run is already failing, and the
// first is the one that says where the archive stopped being complete.
func (p *Pipeline) fail(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err == nil {
		p.err = err
	}
}
