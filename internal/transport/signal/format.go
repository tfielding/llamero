// Markdown from the model, converted into Signal's styled text.
//
// signal-cli-rest-api (text_mode "styled") understands **bold**, *italic*,
// ~strikethrough~, `monospace` and ||spoiler||, with \ as an escape. Everything
// else Markdown uses (headings, tables, rules, links, code fences) is rewritten
// into something that reads well on a phone, and stray marker characters are
// escaped so they show up literally.
//
// The converter itself lives in internal/markdown; this file only supplies the
// Signal renderer.
package signal

import (
	"strings"

	"github.com/tfielding/llamero/internal/markdown"
)

// ToSignal converts Markdown to Signal styled text.
func ToSignal(md string) string {
	return markdown.To(md, markdown.Renderer{
		Escape: escape,
		Bold:   func(s string) string { return "**" + s + "**" },
		Italic: func(s string) string { return "*" + s + "*" },
		Strike: func(s string) string { return "~" + s + "~" },
		InlineCode: func(s string) string {
			return "`" + escape(s) + "`"
		},
		CodeBlock: func(lines []string) []string {
			var out []string
			for _, line := range lines {
				if strings.TrimSpace(line) == "" {
					out = append(out, "")
				} else {
					out = append(out, "`"+escape(line)+"`")
				}
			}
			return out
		},
		Link: func(_ func(string) string, text, url string) string {
			if text == url {
				return url
			}
			return text + " (" + url + ")"
		},
	})
}

// escape makes marker characters literal for the styled-text parser.
func escape(s string) string {
	return strings.NewReplacer("*", `\*`, "~", `\~`, "`", "\\`", "||", `\|\|`).Replace(s)
}
