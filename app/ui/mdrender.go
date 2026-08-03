package ui

import (
	"slices"
	"strings"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
	gstyles "charm.land/glamour/v2/styles"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/lipgloss"
	xansi "github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	gparser "github.com/yuin/goldmark/parser"
	gtext "github.com/yuin/goldmark/text"
)

// mdKey names a cached document. Named rather than a bare string because lines takes the key and the
// source next to each other, and swapping them fails silently in the pane instead of at the call.
type mdKey string

type mdCacheKey struct {
	key   mdKey
	width int
}

// mdDoc is one request for a rendered document: what names it, what it says, the pane width it has to
// fit, and the left pad. The pad is cached with the render, since the browser re-lays the whole report
// on every frame.
type mdDoc struct {
	key    mdKey
	src    string
	width  int
	indent int
}

// mdCacheEntry is one rendered document: its pane lines, the bytes they hold, and the frame that last
// asked for it. The frame stamp is what eviction reads.
type mdCacheEntry struct {
	lines []string
	size  int
	frame uint64
}

// mdRenderer turns markdown documents into pane lines through glamour. One glamour renderer per width,
// since WithWordWrap bakes the width in and two panes ask for two different widths.
type mdRenderer struct {
	style     ansi.StyleConfig
	profile   termenv.Profile
	parser    gparser.Parser
	renderers map[int]*glamour.TermRenderer
	cache     map[mdCacheKey]mdCacheEntry
	cached    int    // bytes of rendered output the cache is holding
	frame     uint64 // the layout pass in progress, stamped onto every entry it touches
}

// newMDRenderer takes the two terminal facts the renderer needs, both read from the lipgloss renderer
// newStyles builds against the tty, so the panes and the frame agree about what the terminal can do.
func newMDRenderer(profile termenv.Profile, dark bool) *mdRenderer {
	return &mdRenderer{
		style:   mdStyle(dark),
		profile: profile,
		// the same construction glamour makes in NewTermRenderer, so escapeHTML sees the document
		// glamour will see and the offsets it collects address the same bytes
		parser: goldmark.New(
			goldmark.WithExtensions(extension.GFM, extension.DefinitionList),
			goldmark.WithParserOptions(gparser.WithAutoHeadingID()),
		).Parser(),
		renderers: make(map[int]*glamour.TermRenderer),
		cache:     make(map[mdCacheKey]mdCacheEntry),
	}
}

// mdStyle picks glamour's base style for the terminal's background, puts h1's markdown hash back and
// takes the padding off an inline code span. The h1 foreground must be cleared along with the band:
// both glamour styles spell it as 228, legible only against the purple that goes with it.
func mdStyle(dark bool) ansi.StyleConfig {
	s := gstyles.LightStyleConfig
	if dark {
		s = gstyles.DarkStyleConfig
	}
	s.H1.Prefix = "# "
	s.H1.Suffix = ""
	s.H1.BackgroundColor = nil
	s.H1.Color = nil
	s.Code.Prefix = ""
	s.Code.Suffix = ""
	return s
}

// mdMaxCache is the rendered-byte threshold at which a closing pass drops what it did not read. It is
// not a ceiling: the pass's own working set stays whatever it comes to.
const mdMaxCache = 32 << 20

// beginFrame opens a layout pass. Every pane runs one, not only the two that render documents — see
// .claude/rules/tui.md, where scoping it narrower is recorded as a retention bug nothing reports.
func (r *mdRenderer) beginFrame() { r.frame++ }

// endFrame closes a layout pass and is the only place the bound evicts anything. It is deferred by each
// pane entry point, so a pane that returns early is still a closed pass.
func (r *mdRenderer) endFrame() {
	if r.cached > mdMaxCache {
		r.evict()
	}
}

// lines renders one document to pane lines, caching by key and width. A hit is restamped with the
// current frame, which is how a pass tells eviction what it read.
func (r *mdRenderer) lines(d mdDoc) []string {
	ck := mdCacheKey{key: d.key, width: d.width}
	if e, ok := r.cache[ck]; ok {
		if e.frame != r.frame {
			e.frame = r.frame
			r.cache[ck] = e
		}
		return e.lines
	}

	out := r.render(d.src, d.width-d.indent)
	if d.indent > 0 {
		pad := strings.Repeat(" ", d.indent)
		for i, l := range out {
			out[i] = pad + l
		}
	}

	size := 0
	for _, l := range out {
		size += len(l)
	}
	r.cache[ck] = mdCacheEntry{lines: out, size: size, frame: r.frame}
	r.cached += size
	return out
}

// evict drops every entry the pass that has just ended did not ask for. The closing pass's own working
// set is the floor and may carry the cache past mdMaxCache: dropping an entry it still uses re-renders
// that entry on every repaint.
func (r *mdRenderer) evict() {
	for k, e := range r.cache {
		if e.frame == r.frame {
			continue
		}
		delete(r.cache, k)
		r.cached -= e.size
	}
}

// mdMaxDoc caps the source a document render is attempted on, in bytes. Measured against this package's
// style at width 100, with fenced code and tables the worst class: 64 KiB renders in ~110ms into ~3.4 MB,
// 1 MiB into ~2.8s and ~84 MB.
const mdMaxDoc = 64 << 10

// render is the uncached path, and the one place tabs are expanded — per line, because expandTabs never
// resets its column on a newline. A source over mdMaxDoc, a construction failure or a render failure all
// fall back to the inline renderer, so an oversized input stays readable rather than blank or truncated.
func (r *mdRenderer) render(src string, width int) []string {
	if len(src) <= mdMaxDoc {
		if tr := r.build(width); tr != nil {
			lines := strings.Split(src, "\n")
			for i, line := range lines {
				lines[i] = expandTabs(line, mdTabStop)
			}
			if out, err := tr.Render(r.escapeHTML(r.joinTightListLines(strings.Join(lines, "\n")))); err == nil {
				return r.trim(r.downsample(out), width)
			}
		}
	}
	return r.inline(src, width)
}

// mdEscaper turns the three characters that make a span parse as raw HTML into the entities that
// survive the sanitizer. Ampersand must come first: a single-pass replacer never revisits what it has
// written, so an ampersand already in the text is escaped once.
var mdEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// joinTightListLines turns a soft line break into a space before glamour sees it, since a list item's
// block keeps the newline where a paragraph's does not. The breaks come from a parse, never a scan: a
// newline in a fence or a table is content. A hard break is deliberate and stays.
func (r *mdRenderer) joinTightListLines(src string) string {
	if !strings.Contains(src, "\n") {
		return src
	}

	// each span is the newline plus the continuation indent between two runs of one item's text
	var spans [][2]int
	doc := r.parser.Parse(gtext.NewReader([]byte(src)))
	err := gast.Walk(doc, func(n gast.Node, entering bool) (gast.WalkStatus, error) {
		if !entering {
			return gast.WalkContinue, nil
		}
		t, ok := n.(*gast.Text)
		if !ok || !t.SoftLineBreak() || t.HardLineBreak() || !r.inPlainListItem(t) {
			return gast.WalkContinue, nil
		}
		// measured off the source, not off the next sibling: a continuation opening with a code span is
		// a CodeSpan, whose segments start inside the backticks
		if end, ok := r.softBreakEnd(src, t.Segment.Stop); ok {
			spans = append(spans, [2]int{t.Segment.Stop, end})
		}
		return gast.WalkContinue, nil
	})
	if err != nil || len(spans) == 0 {
		return src
	}
	slices.SortFunc(spans, func(a, b [2]int) int { return a[0] - b[0] })

	var out strings.Builder
	out.Grow(len(src))
	last := 0
	for _, sp := range spans {
		if sp[0] < last || sp[1] > len(src) {
			continue
		}
		out.WriteString(src[last:sp[0]])
		out.WriteString(" ")
		last = sp[1]
	}
	out.WriteString(src[last:])
	return out.String()
}

// inPlainListItem reports whether t sits in a list item or a tight definition description that no
// blockquote encloses. The scope is the whole safety of the rewrite and has been wrong in both
// directions: too wide splices a blockquote's `>` into the text, too narrow leaves a container ragged.
// Anything added to the switch needs both a joined and a blockquoted case in the test.
func (r *mdRenderer) inPlainListItem(t *gast.Text) bool {
	inItem := false
	for n := gast.Node(t); n != nil; n = n.Parent() {
		switch n.(type) {
		case *gast.Blockquote:
			return false
		case *gast.ListItem, *east.DefinitionDescription:
			inItem = true
		}
	}
	return inItem
}

// softBreakEnd returns the offset past a soft break at from — trailing blanks, an optional CR, the
// newline, the continuation indent. False when there is no newline there, so nothing the parse missed
// gets joined. The CR is what makes a CRLF document join at all: goldmark counts it as space, so the
// text segment stops before it.
func (r *mdRenderer) softBreakEnd(src string, from int) (int, bool) {
	i := from
	for i < len(src) && (src[i] == ' ' || src[i] == '\t' || src[i] == '\r') {
		i++
	}
	if i >= len(src) || src[i] != '\n' {
		return 0, false
	}
	i++
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	return i, true
}

// escapeHTML entity-escapes exactly the spans goldmark reads as raw HTML, so glamour's bluemonday
// StrictPolicy prints them instead of deleting them. The spans come from a parse, never a scan for
// angle brackets: escaping every `<` reaches into code, autolinks and blockquote markers.
func (r *mdRenderer) escapeHTML(src string) string {
	if !strings.Contains(src, "<") {
		return src
	}

	var spans [][2]int
	add := func(s gtext.Segment) {
		if s.Start >= 0 && s.Stop > s.Start {
			spans = append(spans, [2]int{s.Start, s.Stop})
		}
	}
	doc := r.parser.Parse(gtext.NewReader([]byte(src)))
	err := gast.Walk(doc, func(n gast.Node, entering bool) (gast.WalkStatus, error) {
		if !entering {
			return gast.WalkContinue, nil
		}
		switch n := n.(type) {
		case *gast.RawHTML:
			for i := range n.Segments.Len() {
				add(n.Segments.At(i))
			}
		case *gast.HTMLBlock:
			lines := n.Lines()
			for i := range lines.Len() {
				add(lines.At(i))
			}
			if n.HasClosure() {
				add(n.ClosureLine)
			}
		}
		return gast.WalkContinue, nil
	})
	if err != nil || len(spans) == 0 {
		return src
	}
	slices.SortFunc(spans, func(a, b [2]int) int { return a[0] - b[0] })

	var out strings.Builder
	out.Grow(len(src) + 4*len(spans))
	last := 0
	for _, sp := range spans {
		if sp[0] < last || sp[1] > len(src) {
			continue
		}
		out.WriteString(src[last:sp[0]])
		out.WriteString(mdEscaper.Replace(src[sp[0]:sp[1]]))
		last = sp[1]
	}
	out.WriteString(src[last:])
	return out.String()
}

// inline renders a document through the one-line-at-a-time path. No element it returns carries an
// embedded newline, which is what the pane model addresses a document by — Wrap over a whole document
// returns one element with newlines inside it whenever the longest line already fits.
func (r *mdRenderer) inline(src string, width int) []string {
	out := []string{}
	for line := range strings.SplitSeq(src, "\n") {
		out = append(out, Wrap("", markdown(expandTabs(line, mdTabStop)), width)...)
	}
	return out
}

// build gets or creates the renderer for width, returning nil where every caller falls back. The style
// is always explicit: glamour reads os.Stdout and the terminal's background on its AutoStyle path, and
// stdout is a pipe whenever the report is redirected.
func (r *mdRenderer) build(width int) *glamour.TermRenderer {
	if width < 1 {
		return nil
	}
	if tr, ok := r.renderers[width]; ok {
		return tr
	}
	tr, err := glamour.NewTermRenderer(
		glamour.WithStyles(r.style),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return nil
	}
	r.renderers[width] = tr
	return tr
}

// downsample rewrites a rendered document's colors into what the terminal can show, since glamour v2
// dropped the color-profile option and emits the style as written. Runs before trim, so the cached
// lines are the bytes the terminal receives.
func (r *mdRenderer) downsample(out string) string {
	p := colorprofile.TrueColor
	switch r.profile {
	case termenv.Ascii:
		p = colorprofile.Ascii
	case termenv.ANSI:
		p = colorprofile.ANSI
	case termenv.ANSI256:
		p = colorprofile.ANSI256
	case termenv.TrueColor:
		return out // nothing to rewrite, and the writer takes the same shortcut
	}
	var buf strings.Builder
	buf.Grow(len(out))
	w := colorprofile.Writer{Forward: &buf, Profile: p}
	if _, err := w.Write([]byte(out)); err != nil {
		return out // a failed rewrite is a wrong palette, never a blank pane
	}
	return buf.String()
}

// trim splits a rendered document into pane lines, drops the blank lines glamour brackets it with, and
// hard-wraps whatever is still too wide, since glamour does not wrap a long line inside a fence. The
// document margin is left alone: stripping it cuts into a code row opening with a background sequence.
func (r *mdRenderer) trim(out string, width int) []string {
	lines := strings.Split(out, "\n")
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(xansi.Strip(lines[start])) == "" {
		start++
	}
	for end > start && strings.TrimSpace(xansi.Strip(lines[end-1])) == "" {
		end--
	}

	res := make([]string, 0, end-start)
	for _, line := range lines[start:end] {
		if lipgloss.Width(line) <= width {
			res = append(res, line)
			continue
		}
		res = append(res, strings.Split(xansi.Hardwrap(line, width, true), "\n")...)
	}
	return res
}

// reset drops the renderers and the cache, bounding memory by the widths in use. It is not a
// correctness mechanism: both maps are keyed by width, so a resize renders correctly without it.
func (r *mdRenderer) reset() {
	r.renderers = make(map[int]*glamour.TermRenderer)
	r.cache = make(map[mdCacheKey]mdCacheEntry)
	r.cached = 0
}
