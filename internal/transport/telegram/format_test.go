package telegram

import "testing"

func TestToHTML(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"heading", "### 1.  What TCP Is  ", "<b>1.  What TCP Is</b>"},
		{"heading with bold", "## **Big** idea", "<b>Big idea</b>"},
		{"rule dropped", "a\n\n---\n\nb", "a\n\nb"},
		{"bullets", "- one  \n  - two\n* three", "• one\n  ◦ two\n• three"},
		{"numbered kept", "1. **First**", "1. <b>First</b>"},
		{"bold italic strike", "**b** *i* _u_ __bb__ ~~s~~ ***bi***", "<b>b</b> <i>i</i> <i>u</i> <b>bb</b> <s>s</s> <b>bi</b>"},
		{"html escaped", "5 < 6 & 7 > 3 <b>not bold</b>", "5 &lt; 6 &amp; 7 &gt; 3 &lt;b&gt;not bold&lt;/b&gt;"},
		{"stray markers kept", "5 * 3, ~2s, a || b", "5 * 3, ~2s, a || b"},
		{"snake_case untouched", "use snake_case_name here", "use snake_case_name here"},
		{"inline code escaped", "run `a<b && c`", "run <code>a&lt;b &amp;&amp; c</code>"},
		{"link", `see [RFC 793](https://x.org/793?a=1&b="2")`, `see <a href="https://x.org/793?a=1&amp;b=&#34;2&#34;">RFC 793</a>`},
		{"not a web link", "[run](javascript:alert)", "run (javascript:alert)"},
		{"bold link text", "**[docs](https://a.b)**", `<b><a href="https://a.b">docs</a></b>`},
		{"code block", "```go\nif a < b {\n\n}\n```", "<pre>if a &lt; b {\n\n}</pre>"},
		{"quote", "> hi **there**", "▍hi <b>there</b>"},
		{"blank lines collapsed", "a\n\n\n\nb\n\n", "a\n\nb"},
		{
			"table",
			"| Feature | Purpose |\n|---|:---:|\n| **Seq** | Order `x` |\n| Ack | Confirm |",
			"<b>Seq</b>\n<b>Purpose:</b> Order <code>x</code>\n\n<b>Ack</b>\n<b>Purpose:</b> Confirm",
		},
		{"unclosed", "this **never closes and *this either", "this **never closes and *this either"},
	}
	for _, c := range cases {
		if got := ToHTML(c.in); got != c.want {
			t.Errorf("%s:\n got: %q\nwant: %q", c.name, got, c.want)
		}
	}
}
