package tui

import (
	"fmt"
	"strings"
)

// copyTargetKind enumerates the selectable clipboard destinations offered by
// the /copy picker. The same three kinds exist on the web side
// (packages/jcode-ui/src/lib/copyTargets.ts) — keep the extraction rules in
// sync so TUI, Web and Desktop copy byte-identical text.
type copyTargetKind int

const (
	copyTargetFull copyTargetKind = iota
	copyTargetCode
	copyTargetQuote
)

// copyTarget is one selectable clipboard destination extracted from a single
// assistant response snapshot. Text is captured at picker-open time, so a
// still-streaming response cannot shift an index between open and copy.
//
// Only the assistant's own response markdown is analyzed: tool outputs,
// approval prompts and other hidden metadata are never part of a target.
type copyTarget struct {
	kind     copyTargetKind
	label    string // picker title, e.g. "Full response" / "Code block 2"
	detail   string // secondary line, e.g. "go · main.go · 512 B"
	text     string // exact bytes written to the clipboard
	index    int    // 1-based index within the kind
	lang     string // code blocks: fence language ("" if none)
	filename string // code blocks: title=/lang:file chrome ("" if none)
}

// analyzeCopyTargets splits an assistant response (raw markdown, the same
// bytes Ctrl+Y copies) into selectable copy targets: the full response, every
// fenced code block, and every blockquote group.
//
// Extraction rules (mirrored by the web analyzer):
//   - Fences: 3+ backticks or 3+ tildes (never mixed), optionally indented up
//     to 3 spaces. The closing fence is a line of only the same character,
//     at least as long, indented no deeper than the opener. The fence markers
//     and the info string never enter the copied text; the opener's
//     indentation is stripped from content lines.
//   - An unterminated fence (streaming response) still yields its partial
//     content, so copying mid-stream is stable.
//   - Blockquotes: consecutive `>` lines form one target (a blank line ends
//     the group). The `>` marker and one following space are stripped per
//     line; leading/trailing blank lines are trimmed. Quotes inside code
//     fences are code, not quotes.
//   - The full-response target is always first and carries the whole response.
func analyzeCopyTargets(md string) []copyTarget {
	normalized := strings.ReplaceAll(md, "\r\n", "\n")
	full := strings.TrimSpace(normalized)
	if full == "" {
		return nil
	}

	targets := []copyTarget{{
		kind:   copyTargetFull,
		label:  "Full response",
		detail: fmt.Sprintf("%s · %d lines", humanCopySize(len(full)), strings.Count(full, "\n")+1),
		text:   full,
	}}

	lines := strings.Split(normalized, "\n")

	var (
		inFence     bool
		fenceChar   byte
		fenceLen    int
		fenceIndent int
		fenceInfo   string
		codeLines   []string
		codeIdx     int
		quoteLines  []string
		quoteIdx    int
	)

	flushQuote := func() {
		defer func() { quoteLines = nil }()
		// Trim leading/trailing empty lines; keep interior verbatim.
		start, end := 0, len(quoteLines)
		for start < end && strings.TrimSpace(quoteLines[start]) == "" {
			start++
		}
		for end > start && strings.TrimSpace(quoteLines[end-1]) == "" {
			end--
		}
		if start >= end {
			return
		}
		body := make([]string, 0, end-start)
		for _, l := range quoteLines[start:end] {
			body = append(body, strings.TrimRight(l, " \t"))
		}
		text := strings.Join(body, "\n")
		if strings.TrimSpace(text) == "" {
			return
		}
		quoteIdx++
		targets = append(targets, copyTarget{
			kind:   copyTargetQuote,
			label:  fmt.Sprintf("Blockquote %d", quoteIdx),
			detail: quotePreview(text),
			text:   text,
			index:  quoteIdx,
		})
	}

	flushCode := func() {
		codeIdx++
		text := strings.Join(codeLines, "\n")
		lang, filename := parseCopyCodeInfo(fenceInfo)
		detail := humanCopySize(len(text))
		if lang != "" || filename != "" {
			chrome := strings.TrimSpace(lang + " " + filename)
			detail = chrome + " · " + detail
		}
		targets = append(targets, copyTarget{
			kind:     copyTargetCode,
			label:    fmt.Sprintf("Code block %d", codeIdx),
			detail:   detail,
			text:     text,
			index:    codeIdx,
			lang:     lang,
			filename: filename,
		})
		codeLines = nil
	}

	for _, line := range lines {
		indent := len(line) - len(strings.TrimLeft(line, " "))
		content := line[indent:]

		if inFence {
			if c, n, rest, ok := copyFencePrefix(content); ok && c == fenceChar && n >= fenceLen &&
				indent <= fenceIndent && strings.TrimSpace(rest) == "" {
				// Closing fence: only the fence chars (and whitespace) remain.
				inFence = false
				flushCode()
				continue
			}
			codeLines = append(codeLines, dedentCopyLine(line, fenceIndent))
			continue
		}

		if c, n, rest, ok := copyFencePrefix(content); ok && n >= 3 &&
			(c == '~' || !strings.Contains(rest, "`")) {
			// Opening fence (backtick fences cannot have backticks in the info).
			flushQuote()
			inFence = true
			fenceChar = c
			fenceLen = n
			fenceIndent = indent
			fenceInfo = strings.TrimSpace(rest)
			continue
		}

		if strings.HasPrefix(content, ">") {
			rest := strings.TrimPrefix(content[1:], " ")
			quoteLines = append(quoteLines, rest)
			continue
		}

		// A blank line ends the current blockquote group; so does any other
		// block content.
		flushQuote()
	}
	if inFence {
		flushCode() // unterminated fence while streaming — copy the partial block
	}
	flushQuote()

	return targets
}

// copyFencePrefix reports the fence character, its repeat count at the start
// of the (indent-stripped) line, and the remainder of the line (the info
// string for opening fences, "" for closing fences).
func copyFencePrefix(content string) (byte, int, string, bool) {
	if content == "" {
		return 0, 0, "", false
	}
	c := content[0]
	if c != '`' && c != '~' {
		return 0, 0, "", false
	}
	n := 0
	for n < len(content) && content[n] == c {
		n++
	}
	return c, n, content[n:], true
}

// dedentCopyLine strips up to n leading spaces from a code-fence content line
// (the opener's indentation is not part of the code).
func dedentCopyLine(line string, n int) string {
	stripped := 0
	for stripped < n && stripped < len(line) && line[stripped] == ' ' {
		stripped++
	}
	return line[stripped:]
}

// parseCopyCodeInfo parses a fence info string into language + filename using
// the same conventions as the web renderer: `lang`, `lang:file`,
// `lang title=file` (quoted or bare).
func parseCopyCodeInfo(info string) (lang, filename string) {
	trimmed := strings.TrimSpace(info)
	if trimmed == "" {
		return "", ""
	}
	head := strings.Fields(trimmed)[0]
	lang = head
	if before, after, ok := strings.Cut(head, ":"); ok {
		lang = before
		filename = after
	}
	rest := trimmed[len(head):]
	for _, m := range []string{`title="`, "title='", "title="} {
		i := strings.Index(rest, m)
		// Word-boundary guard: `title=` must not match inside `subtitle=`
		// (mirrors the web analyzer's \btitle= regex).
		if i > 0 && isWordByte(rest[i-1]) {
			i = -1
		}
		if i >= 0 {
			val := rest[i+len(m):]
			switch m[len(m)-1] {
			case '"', '\'':
				if end := strings.IndexByte(val, m[len(m)-1]); end >= 0 {
					filename = val[:end]
				}
			default:
				if fields := strings.Fields(val); len(fields) > 0 {
					filename = fields[0]
				}
			}
			break
		}
	}
	return lang, filename
}

// isWordByte reports whether b is a word character for boundary checks
// (letters, digits, underscore — same class as the web \b regex).
func isWordByte(b byte) bool {
	return b == '_' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// quotePreview builds a one-line preview for a blockquote picker entry.
func quotePreview(text string) string {
	first := strings.SplitN(text, "\n", 2)[0]
	first = strings.TrimSpace(first)
	const max = 48
	r := []rune(first)
	if len(r) > max {
		first = string(r[:max]) + "…"
	}
	return first
}

// humanCopySize renders a byte count for picker labels (KB = 1024 B).
func humanCopySize(n int) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	return fmt.Sprintf("%.1f KB", float64(n)/1024)
}
