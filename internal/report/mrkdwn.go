// SPDX-FileCopyrightText: 2026 Agaetis
// SPDX-License-Identifier: Apache-2.0

package report

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// SectionLimit is Slack's limit on a section's text (docs.slack.dev,
// Block Kit reference).
const SectionLimit = 3000

// The model writes Markdown; Slack's mrkdwn is another syntax
// (https://docs.slack.dev/messaging/formatting-message-text).
var (
	// Slack shows no headings: a heading becomes a bold line.
	mdHeading = regexp.MustCompile(`^#{1,6}[ \t]+(.+?)[ \t#]*$`)
	// Slack's bold is one asterisk; Markdown's is two, or two underscores.
	mdBold = regexp.MustCompile(`\*\*(\S(?:.*?\S)??)\*\*`)
	// Underscores only between word boundaries: A__B__C is an identifier.
	mdBoldUnder = regexp.MustCompile(`(^|[^\pL\pN_])__(\S(?:.*?\S)??)__([^\pL\pN_]|$)`)
	// Slack renders no Markdown list: a dash or an asterisk becomes a bullet.
	mdBullet = regexp.MustCompile(`^([ \t]*)[-*][ \t]+`)
	// Slack's link is <url|text>.
	mdLink = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)\)`)
	// Slack requires &, < and > to be escaped in mrkdwn text, code included.
	mrkdwnEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
)

// Escape escapes Slack's control characters, &, < and >, in a mrkdwn
// text: what TakeCare did not write (a name, an alert's) never becomes
// markup.
func Escape(s string) string { return mrkdwnEscape.Replace(s) }

// fence reports whether a line opens or closes a fenced code block, whose
// content is only escaped.
func fence(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "```")
}

// unbold replaces bold markers with mark on each side. The underscore
// form runs until stable: a match consumes the character after it, which
// may open the next one (__a__ __b__).
func unbold(line, mark string) string {
	line = mdBold.ReplaceAllString(line, mark+"$1"+mark)
	for {
		next := mdBoldUnder.ReplaceAllString(line, "$1"+mark+"$2"+mark+"$3")
		if next == line {
			return line
		}
		line = next
	}
}

// Mrkdwn renders the model's Markdown as Slack mrkdwn, line by line.
func Mrkdwn(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	code := false
	for _, line := range lines {
		line = strings.TrimRight(mrkdwnEscape.Replace(line), " \t\r")
		if fence(line) || code {
			code = code != fence(line)
			out = append(out, line)
			continue
		}
		line = unbold(line, "*")
		if m := mdHeading.FindStringSubmatch(line); m != nil {
			if len(out) > 0 && out[len(out)-1] != "" {
				out = append(out, "")
			}
			line = "*" + strings.Trim(m[1], "*") + "*"
		}
		line = mdBullet.ReplaceAllString(line, "$1• ")
		out = append(out, mdLink.ReplaceAllString(line, "<$2|$1>"))
	}
	return strings.Join(out, "\n")
}

// Plain drops the Markdown markers a reader would see in plain text:
// heading marks, bold marks; a link becomes "text (url)".
func Plain(s string) string {
	lines := strings.Split(s, "\n")
	code := false
	for i, line := range lines {
		if fence(line) || code {
			code = code != fence(line)
			continue
		}
		line = unbold(line, "")
		line = mdHeading.ReplaceAllString(line, "$1")
		lines[i] = mdLink.ReplaceAllString(line, "$1 ($2)")
	}
	return strings.Join(lines, "\n")
}

// Section cuts a converted mrkdwn text to SectionLimit bytes with an
// ellipsis: on a rune boundary, leaving room for the ellipsis' 3 bytes,
// and never inside an escape such as &amp;.
func Section(body string) string {
	if len(body) <= SectionLimit {
		return body
	}
	cut := SectionLimit - len("…")
	for cut > 0 && !utf8.RuneStart(body[cut]) {
		cut--
	}
	if i := strings.LastIndexByte(body[:cut], '&'); i >= 0 && cut-i < len("&amp;") && !strings.Contains(body[i:cut], ";") {
		cut = i
	}
	return body[:cut] + "…"
}

// Thousands writes 42560 as 42,560.
func Thousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
