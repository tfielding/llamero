// Package markdown converts model-produced Markdown into a transport's styled
// text.
//
// The Signal and Telegram transports used to carry two near-identical copies of this
// logic, differing only in the markup they emit (Signal: **bold**, *italic*, ~strike~,
// `code`; Telegram: <b>, <i>, <s>, <code>). Both are now driven by a single converter,
// with the per-transport differences confined to a Renderer.
package markdown

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	fenceRe   = regexp.MustCompile("^\\s*(```|~~~)")
	headingRe = regexp.MustCompile(`^\s{0,3}#{1,6}\s+(.*?)\s*#*\s*$`)
	ruleRe    = regexp.MustCompile(`^\s{0,3}([-*_])(\s*[-*_]){2,}\s*$`)
	bulletRe  = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	quoteRe   = regexp.MustCompile(`^\s{0,3}>\s?(.*)$`)
	tableSep  = regexp.MustCompile(`^\s*\|?\s*:?-{2,}:?\s*(\|\s*:?-{2,}:?\s*)*\|?\s*$`)

	codeRe     = regexp.MustCompile("`([^`\n]+)`")
	imageRe    = regexp.MustCompile(`!\[([^\]]*)\]\(([^)\s]+)[^)]*\)`)
	linkRe     = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)[^)]*\)`)
	boldItalRe = regexp.MustCompile(`\*\*\*([^*\n]+?)\*\*\*`)
	boldRe     = regexp.MustCompile(`\*\*([^*\n]+?)\*\*`)
	boldUndRe  = regexp.MustCompile(`__([^_\n]+?)__`)
	italRe     = regexp.MustCompile(`(^|[^\w*\\])\*([^*\s](?:[^*\n]*[^*\s])?)\*`)
	italUndRe  = regexp.MustCompile(`(^|[^\w])_([^_\s](?:[^_\n]*[^_\s])?)_`)
	strikeRe   = regexp.MustCompile(`~~([^~\n]+?)~~`)
	holdRe     = regexp.MustCompile(`\x{E000}(\d+)\x{E001}`) // placeholder: private-use runes around an index
)

// Renderer describes the per-transport markup produced from Markdown.
//
// Bold, Italic, Strike and InlineCode wrap an already-escaped span; Escape is the
// final pass over plain text. InlineCode receives raw span content and escapes
// internally, as does CodeBlock and Link.
type Renderer struct {
	// Escape escapes marker characters in plain text.
	Escape func(s string) string
	// Bold, Italic and Strike wrap an already-escaped span.
	Bold   func(s string) string
	Italic func(s string) string
	Strike func(s string) string
	// InlineCode wraps a raw inline code span, escaping it internally.
	InlineCode func(s string) string
	// CodeBlock renders a fenced code block (raw lines) as one or more lines.
	CodeBlock func(lines []string) []string
	// Link renders a raw Markdown link (text, url). It receives hold, the shared
	// placeholder mechanism, for transports whose markup must survive the final
	// escape pass; transports whose markup is plain text ignore it.
	Link func(hold func(string) string, text, url string) string
}

// To converts Markdown into the transport's styled text.
func To(md string, r Renderer) string {
	lines := strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		line := lines[i]

		// Code block.
		if fenceRe.MatchString(line) {
			var code []string
			for i++; i < len(lines) && !fenceRe.MatchString(lines[i]); i++ {
				code = append(code, lines[i])
			}
			out = append(out, r.CodeBlock(code)...)
			continue
		}

		// Table: header row followed by a separator row.
		if strings.Contains(line, "|") && i+1 < len(lines) && tableSep.MatchString(lines[i+1]) {
			header := cells(line)
			i += 2
			for ; i < len(lines) && strings.Contains(lines[i], "|") && strings.TrimSpace(lines[i]) != ""; i++ {
				out = append(out, tableRow(header, cells(lines[i]), r)...)
				out = append(out, "")
			}
			i--
			continue
		}

		switch {
		case ruleRe.MatchString(line):
			out = append(out, "")
		case headingRe.MatchString(line):
			out = append(out, bold(headingRe.FindStringSubmatch(line)[1], r))
		case bulletRe.MatchString(line):
			m := bulletRe.FindStringSubmatch(line)
			level := len(strings.ReplaceAll(m[1], "\t", "  ")) / 2
			mark := "• "
			if level > 0 {
				mark = "◦ "
			}
			out = append(out, strings.Repeat("  ", level)+mark+inline(strings.TrimRight(m[2], " \t"), r))
		case quoteRe.MatchString(line):
			out = append(out, "▍"+inline(quoteRe.FindStringSubmatch(line)[1], r))
		default:
			out = append(out, inline(strings.TrimRight(line, " \t"), r))
		}
	}
	return collapse(out)
}

// tableRow renders one row as a bold title (first cell) followed by "Header: value" lines.
func tableRow(header, row []string, r Renderer) []string {
	var lines []string
	for j, c := range row {
		if c == "" {
			continue
		}
		if j == 0 {
			lines = append(lines, bold(c, r))
			continue
		}
		if j < len(header) && header[j] != "" {
			lines = append(lines, bold(header[j]+":", r)+" "+inline(c, r))
		} else {
			lines = append(lines, inline(c, r))
		}
	}
	return lines
}

func cells(line string) []string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// bold wraps text in bold, removing emphasis markers inside it so styles don't nest oddly.
func bold(s string, r Renderer) string {
	s = strings.TrimSpace(strings.NewReplacer("**", "", "__", "").Replace(s))
	if s == "" {
		return ""
	}
	return r.Bold(inline(s, r))
}

// inline converts emphasis, code and links, and escapes everything else.
func inline(s string, r Renderer) string {
	var held []string
	hold := func(x string) string {
		held = append(held, x)
		return fmt.Sprintf("\uE000%d\uE001", len(held)-1)
	}

	s = codeRe.ReplaceAllStringFunc(s, func(m string) string {
		return hold(r.InlineCode(m[1 : len(m)-1]))
	})
	s = imageRe.ReplaceAllString(s, "$1 ($2)")
	s = linkRe.ReplaceAllStringFunc(s, func(m string) string {
		p := linkRe.FindStringSubmatch(m)
		return r.Link(hold, p[1], p[2])
	})
	wrap := func(re *regexp.Regexp, wrapFn func(string) string) {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			return hold(wrapFn(r.Escape(re.FindStringSubmatch(m)[1])))
		})
	}
	wrap(boldItalRe, r.Bold)
	wrap(boldRe, r.Bold)
	wrap(boldUndRe, r.Bold)
	wrap(strikeRe, r.Strike)
	for _, re := range []*regexp.Regexp{italRe, italUndRe} {
		s = re.ReplaceAllStringFunc(s, func(m string) string {
			p := re.FindStringSubmatch(m)
			return p[1] + hold(r.Italic(r.Escape(p[2])))
		})
	}

	s = r.Escape(s)
	for holdRe.MatchString(s) { // held spans can contain other held spans
		s = holdRe.ReplaceAllStringFunc(s, func(m string) string {
			var n int
			fmt.Sscanf(holdRe.FindStringSubmatch(m)[1], "%d", &n)
			return held[n]
		})
	}
	return s
}

// collapse trims blank lines at the ends and squeezes runs of blank lines to one.
func collapse(lines []string) string {
	var out []string
	blank := true
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			if !blank {
				out = append(out, "")
			}
			blank = true
			continue
		}
		out = append(out, l)
		blank = false
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}
