package tui

import (
	"bytes"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// RenderCap is the most of a file the preview will format.
//
// Formatting cost is linear in input: glamour measures ~4.4ms/KB here, so 64KB
// lands under ~200ms while the largest artifact in a real task root — a 2.1MB
// CSV — would take almost five seconds. The preview follows the cursor, so a
// file big enough to be felt is a file that makes scrolling stutter. Past this
// much the preview shows the beginning and says so.
const RenderCap = 64 * 1024

// binarySniff is how far into a file the preview looks for a NUL before
// deciding the thing is not text. A text file that opens with 8KB of printable
// bytes is text; the alternative is rendering a .png into the pane.
const binarySniff = 8 * 1024

// Preview is a file formatted for the detail pane.
type Preview struct {
	Body      string // rendered, ready to put in a viewport
	Truncated bool   // only the first RenderCap bytes were formatted
	Binary    bool   // not text, so Body describes it rather than showing it
	Size      int64  // the whole file's size, not the part that was read
}

// previewKey identifies a formatted result. Width is part of it because
// markdown is wrapped to the pane, and modtime because a file an agent is still
// writing has to re-render rather than serve the copy from before the write.
type previewKey struct {
	path    string
	width   int
	modTime time.Time
	size    int64
}

// Renderer formats file content for the detail pane, caching what it has
// already done. Not safe for concurrent use: it belongs to one model, which
// bubbletea only ever touches from one goroutine.
type Renderer struct {
	cache map[previewKey]Preview

	// term is the glamour renderer, rebuilt when the pane width changes. Keeping
	// it costs one allocation per resize instead of one per file.
	term      *glamour.TermRenderer
	termWidth int
}

// NewRenderer returns a renderer with an empty cache.
func NewRenderer() *Renderer {
	return &Renderer{cache: make(map[previewKey]Preview)}
}

// Render formats content for display. Markdown is formatted, other text is
// shown as-is, and anything binary is described instead.
//
// size is the file's true size and content may be short of it, which is how a
// capped read reports that there is more.
func (r *Renderer) Render(path string, content []byte, size int64, modTime time.Time, width int) Preview {
	key := previewKey{path: path, width: width, modTime: modTime, size: size}
	if hit, ok := r.cache[key]; ok {
		return hit
	}

	out := r.render(path, content, size, width)
	r.cache[key] = out
	return out
}

func (r *Renderer) render(path string, content []byte, size int64, width int) Preview {
	out := Preview{Size: size, Truncated: int64(len(content)) < size}

	if isBinary(content) {
		out.Binary = true
		out.Body = styleMuted.Render(fmt.Sprintf("%s · binary, nothing to show", humanSize(size)))
		return out
	}

	text := string(content)
	if !utf8.ValidString(text) {
		text = strings.ToValidUTF8(text, "�")
	}

	if isMarkdown(path) {
		if body, err := r.markdown(text, width); err == nil {
			out.Body = body
			return out
		}
		// Formatting failed, which is not a reason to show nothing: the raw text
		// is still the file, and still what the reader wanted.
	}
	out.Body = text
	return out
}

// markdown formats text to the pane width, reusing the renderer when the width
// has not changed.
func (r *Renderer) markdown(text string, width int) (string, error) {
	wrap := width
	if wrap < 20 {
		wrap = 20
	}
	if r.term == nil || r.termWidth != wrap {
		term, err := glamour.NewTermRenderer(
			glamour.WithStandardStyle(glamourStyle()),
			glamour.WithWordWrap(wrap),
		)
		if err != nil {
			return "", err
		}
		r.term, r.termWidth = term, wrap
	}
	return r.term.Render(text)
}

// glamourStyle picks the built-in theme matching the terminal, so the preview
// is readable on a light background rather than assuming dark — the same reason
// every color in this package is a lipgloss.AdaptiveColor.
func glamourStyle() string {
	if lipgloss.HasDarkBackground() {
		return "dark"
	}
	return "light"
}

// isBinary reports whether content looks like something other than text. A NUL
// in the first few KB is the signal; it is what file(1) leans on and it costs
// one scan of bytes already in memory.
func isBinary(content []byte) bool {
	head := content
	if len(head) > binarySniff {
		head = head[:binarySniff]
	}
	return bytes.IndexByte(head, 0) >= 0
}

// isMarkdown reports whether a path should be formatted rather than shown raw.
func isMarkdown(path string) bool {
	lower := strings.ToLower(path)
	return strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown")
}

// humanSize renders a byte count the way a person would say it.
func humanSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.0f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}
