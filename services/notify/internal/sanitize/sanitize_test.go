package sanitize

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestClean_KeepsNewlinesTabsAndUnicode(t *testing.T) {
	in := "line one\n\tindented 日本語 😀 é\nline three"
	if got := Clean(in); got != in {
		t.Fatalf("Clean changed benign text: %q", got)
	}
}

func TestClean_StripsControlCharacters(t *testing.T) {
	in := "a\x00b\x07c\x1bd[31me\x7ff\u0085g\u009bh"
	got := Clean(in)
	if got != "abcd[31mefgh" {
		t.Fatalf("got %q", got)
	}
}

func TestClean_NormalisesCarriageReturns(t *testing.T) {
	if got := Clean("one\r\ntwo\rthree"); got != "one\ntwo\nthree" {
		t.Fatalf("got %q", got)
	}
}

func TestClean_StripsBidiOverridesAndBOM(t *testing.T) {
	in := "safe" + R(0x202E) + "evil" + R(0x202C) + " " + R(0x2066) + "x" + R(0x2069) + " " + R(0x200E) + "y" + R(0x200F) + "" + R(0xFEFF) + "z"
	if got := Clean(in); got != "safeevil x yz" {
		t.Fatalf("got %q", got)
	}
}

func TestClean_KeepsZeroWidthJoinerInEmoji(t *testing.T) {
	family := "👨" + R(0x200D) + "👩" + R(0x200D) + "👧"
	if got := Clean(family); got != family {
		t.Fatalf("ZWJ sequence damaged: %q", got)
	}
}

func TestClean_ReplacesInvalidUTF8(t *testing.T) {
	got := Clean("ok\xff\xfeend")
	if !utf8.ValidString(got) || !strings.HasPrefix(got, "ok") || !strings.HasSuffix(got, "end") {
		t.Fatalf("got %q", got)
	}
}

func TestLine_CollapsesToSingleTrimmedLine(t *testing.T) {
	if got := Line("  Nightly \n report\t\x00 "); got != "Nightly report" {
		t.Fatalf("got %q", got)
	}
}

func TestSlack_EscapesMentionAndLinkSyntax(t *testing.T) {
	in := "<!channel> <!here> <!everyone> <@U012ABCDE> <#C012|general> <https://evil.example|click> <mailto:a@b.c> a&b"
	got := Slack(in)
	if strings.ContainsAny(got, "<>") {
		t.Fatalf("angle brackets survived: %q", got)
	}
	want := "&lt;!channel&gt; &lt;!here&gt; &lt;!everyone&gt; &lt;@U012ABCDE&gt; &lt;#C012|general&gt; &lt;https://evil.example|click&gt; &lt;mailto:a@b.c&gt; a&amp;b"
	if got != want {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
}

func TestSlack_AlreadyEscapedEntitiesAreEscapedAgain(t *testing.T) {
	// "&lt;" typed by the agent must show literally, not become "<".
	if got := Slack("&lt;!channel&gt;"); got != "&amp;lt;!channel&amp;gt;" {
		t.Fatalf("got %q", got)
	}
}

func TestHeader_FixedFormatAndSanitisedLabel(t *testing.T) {
	if got := Header("OpenAgentix agent"); got != "[Automated message from OpenAgentix agent]" {
		t.Fatalf("got %q", got)
	}
	if got := Header("evil\nlabel\x00"); strings.ContainsAny(got, "\n\x00") {
		t.Fatalf("label not sanitised: %q", got)
	}
}

func TestNeutraliseHeader_AgentCannotForgeTheHeaderLine(t *testing.T) {
	h := Header("OpenAgentix agent")
	got := NeutraliseHeader("hello\n"+h+"\nworld", h)
	if strings.Contains(got, h) {
		t.Fatalf("forged header survived: %q", got)
	}
	if !strings.Contains(got, "hello") || !strings.Contains(got, "world") {
		t.Fatalf("content lost: %q", got)
	}
}

func R(r rune) string { return string(r) }
