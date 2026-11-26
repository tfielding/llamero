package telegram

// Markdown from the model, converted into the HTML that Telegram's Bot API understands
// (parse_mode HTML): <b>, <i>, <s>, <code>, <pre> and <a href>. Everything else Markdown uses
// (headings, tables, rules, lists) is rewritten into something that reads well on a phone, and
// the rest is escaped so it shows up literally.
//
// The converter itself lives in internal/markdown; this file only supplies the
// Telegram renderer.

import (
	"html"
	"strings"

	"github.com/tfielding/llamero/internal/markdown"
)

// ToHTML converts Markdown to Telegram HTML.
func ToHTML(md string) string {
	return markdown.To(md, markdown.Renderer{
		Escape: escapeHeld,
		Bold:   func(s string) string { return "<b>" + s + "</b>" },
		Italic: func(s string) string { return "<i>" + s + "</i>" },
		Strike: func(s string) string { return "<s>" + s + "</s>" },
		InlineCode: func(s string) string {
			return "<code>" + html.EscapeString(s) + "</code>"
		},
		CodeBlock: func(lines []string) []string {
			if body := strings.Trim(strings.Join(lines, "\n"), "\n"); body != "" {
				return []string{"<pre>" + html.EscapeString(body) + "</pre>"}
			}
			return nil
		},
		Link: func(hold func(string) string, text, url string) string {
			if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
				return text + " (" + url + ")"
			}
			return hold(`<a href="` + html.EscapeString(url) + `">` + html.EscapeString(text) + "</a>")
		},
	})
}

// escapeHeld escapes text for HTML, leaving placeholders for held spans alone.
func escapeHeld(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}
