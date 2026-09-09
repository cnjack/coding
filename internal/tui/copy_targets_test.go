package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func findCopyTargets(ts []copyTarget, kind copyTargetKind) []copyTarget {
	var out []copyTarget
	for _, t := range ts {
		if t.kind == kind {
			out = append(out, t)
		}
	}
	return out
}

func TestAnalyzeCopyTargetsEmpty(t *testing.T) {
	for _, in := range []string{"", "   \n  \n", "\r\n"} {
		if got := analyzeCopyTargets(in); got != nil {
			t.Fatalf("analyzeCopyTargets(%q) = %+v, want nil", in, got)
		}
	}
}

func TestAnalyzeCopyTargetsFullOnly(t *testing.T) {
	md := "Just prose.\nSecond line, no fences or quotes.\n"
	targets := analyzeCopyTargets(md)
	if len(targets) != 1 {
		t.Fatalf("targets = %d, want 1 (full only): %+v", len(targets), targets)
	}
	full := targets[0]
	if full.kind != copyTargetFull || full.label != "Full response" {
		t.Fatalf("unexpected full target: %+v", full)
	}
	if full.text != strings.TrimSpace(md) {
		t.Fatalf("full.text = %q", full.text)
	}
}

func TestAnalyzeCopyTargetsCodeBlocks(t *testing.T) {
	md := "" +
		"Intro text.\n" +
		"```go title=main.go\n" +
		"package main\n" +
		"\n" +
		"func main() {}\n" +
		"```\n" +
		"middle\n" +
		"~~~python:script.py\n" +
		"print('hi')\n" +
		"~~~~\n" +
		"end"

	targets := analyzeCopyTargets(md)
	codes := findCopyTargets(targets, copyTargetCode)
	if len(codes) != 2 {
		t.Fatalf("code targets = %d, want 2: %+v", len(codes), targets)
	}

	first := codes[0]
	if first.index != 1 || first.lang != "go" || first.filename != "main.go" {
		t.Fatalf("first code target meta = %+v", first)
	}
	if first.text != "package main\n\nfunc main() {}" {
		t.Fatalf("first code text = %q (fence chrome must not leak)", first.text)
	}

	second := codes[1]
	if second.index != 2 || second.lang != "python" || second.filename != "script.py" {
		t.Fatalf("second code target meta = %+v", second)
	}
	if second.text != "print('hi')" {
		t.Fatalf("second code text = %q", second.text)
	}

	// Full response keeps everything, fences included.
	full := findCopyTargets(targets, copyTargetFull)
	if len(full) != 1 || !strings.Contains(full[0].text, "```go title=main.go") {
		t.Fatalf("full target lost original markdown: %+v", full)
	}
}

func TestAnalyzeCopyTargetsCodeBlockChromeExcluded(t *testing.T) {
	md := "```ts\nconst x: number = 1\n```"
	targets := analyzeCopyTargets(md)
	codes := findCopyTargets(targets, copyTargetCode)
	if len(codes) != 1 {
		t.Fatalf("codes = %+v", targets)
	}
	if codes[0].text != "const x: number = 1" {
		t.Fatalf("code text = %q, want no fence/lang chrome", codes[0].text)
	}
	if codes[0].lang != "ts" || codes[0].filename != "" {
		t.Fatalf("meta = %+v", codes[0])
	}
}

func TestAnalyzeCopyTargetsUnterminatedFenceStreamsStably(t *testing.T) {
	// A still-streaming response often ends inside an open fence; the partial
	// block must be copyable and deterministic on the snapshot.
	md := "Header\n```go\nfunc A() {\n\treturn 1"
	targets := analyzeCopyTargets(md)
	codes := findCopyTargets(targets, copyTargetCode)
	if len(codes) != 1 {
		t.Fatalf("codes = %+v", targets)
	}
	if codes[0].text != "func A() {\n\treturn 1" {
		t.Fatalf("partial code text = %q", codes[0].text)
	}
	if codes[0].lang != "go" {
		t.Fatalf("partial code lang = %q", codes[0].lang)
	}
}

func TestAnalyzeCopyTargetsIndentedFenceDedent(t *testing.T) {
	md := "  ```js\n  let a = 1\n      deeper\n  ```"
	targets := analyzeCopyTargets(md)
	codes := findCopyTargets(targets, copyTargetCode)
	if len(codes) != 1 {
		t.Fatalf("codes = %+v", targets)
	}
	if codes[0].text != "let a = 1\n    deeper" {
		t.Fatalf("dedented code = %q", codes[0].text)
	}
}

func TestAnalyzeCopyTargetsBlockquotes(t *testing.T) {
	md := "" +
		"> first quote line\n" +
		">second no space\n" +
		">\n" +
		"> continued after empty marker\n" +
		"\n" +
		"plain paragraph\n" +
		"\n" +
		"> second group\n" +
		"> 日本語の引用\n"

	targets := analyzeCopyTargets(md)
	quotes := findCopyTargets(targets, copyTargetQuote)
	if len(quotes) != 2 {
		t.Fatalf("quote targets = %d, want 2: %+v", len(quotes), targets)
	}

	first := quotes[0]
	want := "first quote line\nsecond no space\n\ncontinued after empty marker"
	if first.text != want {
		t.Fatalf("first quote = %q, want %q", first.text, want)
	}
	if second := quotes[1]; second.text != "second group\n日本語の引用" {
		t.Fatalf("second quote = %q", second.text)
	}
	if quotes[1].index != 2 || quotes[0].index != 1 {
		t.Fatalf("quote indices = %d, %d", quotes[0].index, quotes[1].index)
	}
}

func TestAnalyzeCopyTargetsQuoteInsideFenceIsCode(t *testing.T) {
	md := "```\n> not a quote\n```\n> real quote"
	targets := analyzeCopyTargets(md)
	if codes := findCopyTargets(targets, copyTargetCode); len(codes) != 1 || codes[0].text != "> not a quote" {
		t.Fatalf("codes = %+v", targets)
	}
	if quotes := findCopyTargets(targets, copyTargetQuote); len(quotes) != 1 || quotes[0].text != "real quote" {
		t.Fatalf("quotes = %+v", targets)
	}
}

func TestAnalyzeCopyTargetsCRLFInput(t *testing.T) {
	md := "line\r\n```go\r\ncode()\r\n```\r\n> quote\r\n"
	targets := analyzeCopyTargets(md)
	codes := findCopyTargets(targets, copyTargetCode)
	if len(codes) != 1 || codes[0].text != "code()" {
		t.Fatalf("crlf codes = %+v", targets)
	}
	quotes := findCopyTargets(targets, copyTargetQuote)
	if len(quotes) != 1 || quotes[0].text != "quote" {
		t.Fatalf("crlf quotes = %+v", targets)
	}
}

func TestAnalyzeCopyTargetsMixedOrder(t *testing.T) {
	// Targets are numbered per kind in document order regardless of kind
	// interleaving (concurrent tool output never renumbers an earlier index).
	md := "> q1\n```js\na()\n```\n> q2\n```py\nb()\n```"
	targets := analyzeCopyTargets(md)
	codes := findCopyTargets(targets, copyTargetCode)
	quotes := findCopyTargets(targets, copyTargetQuote)
	if len(codes) != 2 || len(quotes) != 2 {
		t.Fatalf("codes=%d quotes=%d: %+v", len(codes), len(quotes), targets)
	}
	if codes[0].text != "a()" || codes[1].text != "b()" {
		t.Fatalf("code order drifted: %+v", codes)
	}
	if quotes[0].text != "q1" || quotes[1].text != "q2" {
		t.Fatalf("quote order drifted: %+v", quotes)
	}
}

func TestCopyItemImplementsListItem(t *testing.T) {
	it := copyItem{target: copyTarget{kind: copyTargetCode, label: "Code block 1", detail: "go · 10 B"}}
	if it.Title() != "Code block 1" || it.Description() != "go · 10 B" {
		t.Fatalf("item = %q / %q", it.Title(), it.Description())
	}
	if it.FilterValue() == "" {
		t.Fatal("FilterValue must not be empty for filterable targets")
	}
}

func TestHumanCopySize(t *testing.T) {
	cases := map[int]string{0: "0 B", 512: "512 B", 1024: "1.0 KB", 1536: "1.5 KB", 25 * 1024: "25.0 KB"}
	for n, want := range cases {
		if got := humanCopySize(n); got != want {
			t.Errorf("humanCopySize(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestParseCopyCodeInfo(t *testing.T) {
	cases := []struct {
		info           string
		lang, filename string
	}{
		{"", "", ""},
		{"go", "go", ""},
		{"go title=main.go", "go", "main.go"},
		{`go title="my file.go"`, "go", "my file.go"},
		{"go:main.go", "go", "main.go"},
		{"ts rxStrategies", "ts", ""},
		{"go subtitle=x.ts", "go", ""},
	}
	for _, c := range cases {
		lang, filename := parseCopyCodeInfo(c.info)
		if lang != c.lang || filename != c.filename {
			t.Errorf("parseCopyCodeInfo(%q) = (%q, %q), want (%q, %q)", c.info, lang, filename, c.lang, c.filename)
		}
	}
}

// TestCopyCommandInSlashMenu guards the /copy registration (suggestions + help
// panel are generated from getAllCommands).
func TestCopyCommandInSlashMenu(t *testing.T) {
	for _, c := range (Model{}).getAllCommands() {
		if c.cmd == "/copy" {
			if c.desc == "" {
				t.Fatal("/copy needs a description for the help panel")
			}
			return
		}
	}
	t.Fatal("/copy should appear in the slash command menu")
}

// TestHandleCopyInputPickerFlow opens the picker on a response snapshot and
// walks select → copy without a running BubbleTea program.
func TestHandleCopyInputPickerFlow(t *testing.T) {
	// NewModel (not a bare Model{}) so the textarea has a real cursor for
	// the Focus() call on picker close.
	m := NewModel(false, t.TempDir(), nil)
	m.lastAssistantRawText = "Prose\n```go\nfunc A() {}\n```\n> quoted\n"

	m2, _ := m.handleCopyInput(nil)
	mm := m2.(*Model)
	if !mm.pickingCopy {
		t.Fatal("/copy should open the picker")
	}
	if len(mm.copyTargets) != 3 {
		t.Fatalf("targets = %+v, want full+code+quote", mm.copyTargets)
	}

	// Copying the highlighted target emits a SetClipboard command and prints
	// a success line.
	m3, cmd := mm.handleCopyPickerKey(tea.KeyPressMsg{Code: tea.KeyEnter}, nil)
	mm3 := m3.(*Model)
	if mm3.pickingCopy {
		t.Fatal("Enter should close the picker")
	}
	content, ok := drainForSetClipboard(cmd)
	if !ok {
		t.Fatal("Enter should emit tea.SetClipboard")
	}
	if content != "Prose\n```go\nfunc A() {}\n```\n> quoted" {
		t.Fatalf("clipboard = %q", content)
	}
	if !strings.Contains(strings.Join(lineTexts(mm3.lines), "\n"), "✓ Copied") {
		t.Fatalf("missing success feedback: %+v", mm3.lines)
	}
}

// TestHandleCopyInputNothingToCopy verifies recoverable feedback when there is
// no response in the current view.
func TestHandleCopyInputNothingToCopy(t *testing.T) {
	m := NewModel(false, t.TempDir(), nil)
	m2, _ := m.handleCopyInput(nil)
	mm := m2.(*Model)
	if mm.pickingCopy {
		t.Fatal("picker must not open without a response")
	}
	joined := strings.Join(lineTexts(mm.lines), "\n")
	if !strings.Contains(joined, "Nothing to copy") {
		t.Fatalf("missing empty feedback: %+v", mm.lines)
	}
}

// TestHandleCopyPickerEscapeCancels verifies the cancel path leaves clipboard
// and transcript untouched.
func TestHandleCopyPickerEscapeCancels(t *testing.T) {
	m := NewModel(false, t.TempDir(), nil)
	m.lastAssistantRawText = "hello"
	m2, _ := m.handleCopyInput(nil)
	mm := m2.(*Model)
	m3, cmd := mm.handleCopyPickerKey(tea.KeyPressMsg{Code: tea.KeyEsc}, nil)
	mm3 := m3.(*Model)
	if mm3.pickingCopy || len(mm3.copyTargets) != 0 {
		t.Fatalf("esc should close the picker: %+v", mm3)
	}
	if _, ok := drainForSetClipboard(cmd); ok {
		t.Fatal("esc must not copy")
	}
}

// TestCopySourceTextPrefersStreamingText pins the current-turn semantics: the
// in-flight response wins over the previous completed one, matching Ctrl+Y.
func TestCopySourceTextPrefersStreamingText(t *testing.T) {
	m := Model{lastAssistantRawText: "previous", currentText: &strings.Builder{}}
	if got := m.copySourceText(); got != "previous" {
		t.Fatalf("idle source = %q", got)
	}
	m.currentText.WriteString("streaming…")
	if got := m.copySourceText(); got != "streaming…" {
		t.Fatalf("streaming source = %q", got)
	}
}

// drainForSetClipboard executes a (possibly batched) command tree and
// returns the content of the first clipboard-set command it produces.
// bubbletea's setClipboardMsg is unexported, so the payload is read via its
// string representation.
func drainForSetClipboard(cmd tea.Cmd) (string, bool) {
	if cmd == nil {
		return "", false
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			if content, ok := drainForSetClipboard(c); ok {
				return content, ok
			}
		}
	default:
		if fmt.Sprintf("%T", msg) == "tea.setClipboardMsg" {
			return fmt.Sprintf("%v", msg), true
		}
	}
	return "", false
}

func lineTexts(lines []contentLine) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, l.text)
	}
	return out
}
