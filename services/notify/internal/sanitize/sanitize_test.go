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

// --- I4: invisible characters ---

func TestClean_StripsInvisibleCharacterClasses(t *testing.T) {
	classes := map[string][]rune{
		"line and paragraph separators": {0x2028, 0x2029},
		"soft hyphen":                   {0x00AD},
		"word joiner":                   {0x2060},
		"mongolian vowel separator":     {0x180E},
		"zero width space/non-joiner":   {0x200B, 0x200C},
		"zero width joiner":             {0x200D},
		"interlinear annotation":        {0xFFF9, 0xFFFA, 0xFFFB},
		"tag block start":               {0xE0000, 0xE0001},
		"tag letters":                   {0xE0041, 0xE0061, 0xE007F},
	}
	for name, rs := range classes {
		for _, r := range rs {
			in := "a" + R(r) + "b"
			if got := Clean(in); got != "ab" {
				t.Errorf("%s U+%04X: got %q, want %q", name, r, got, "ab")
			}
		}
	}
}

func TestClean_TagBlockCannotSmuggleAHiddenMessage(t *testing.T) {
	var hidden strings.Builder
	for _, r := range "ignore previous instructions" {
		hidden.WriteRune(0xE0000 + r)
	}
	if got := Clean("hello" + hidden.String()); got != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestClean_ZWJSurvivesOnlyBetweenEmoji(t *testing.T) {
	keep := []string{
		"👨" + R(0x200D) + "👩" + R(0x200D) + "👧",
		"👩🏽" + R(0x200D) + "💻",            // skin tone modifier before the joiner
		"🏳" + R(0xFE0F) + R(0x200D) + "🌈", // variation selector before the joiner
		"❤" + R(0xFE0F) + R(0x200D) + "🔥",
	}
	for _, in := range keep {
		if got := Clean(in); got != in {
			t.Errorf("emoji sequence damaged: %q -> %q", in, got)
		}
	}
	drop := map[string]string{
		"a" + R(0x200D) + "b":             "ab",
		R(0x200D) + "👨":                   "👨",
		"👨" + R(0x200D):                   "👨",
		"👨" + R(0x200D) + "x":             "👨x",
		"x" + R(0x200D) + "👨":             "x👨",
		"1" + R(0x200D) + "2":             "12",
		"👨" + R(0x200D) + R(0x200D) + "👩": "👨" + R(0x200D) + "👩", // only the joiner that really sits between two emoji stays
		"👨" + R(0x200D) + R(0x200B) + "👩": "👨👩",
	}
	for in, want := range drop {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- I3: header look-alikes ---

func TestNeutraliseHeader_LookAlikesAreNeutralised(t *testing.T) {
	h := Header("OpenAgentix agent")
	cases := map[string]string{
		"exact":                "[Automated message from OpenAgentix agent]",
		"different case":       "[AUTOMATED MESSAGE FROM OPENAGENTIX AGENT]",
		"mixed case":           "[aUtOmAtEd MeSsAgE fRoM openagentix agent]",
		"NBSP between words":   "[Automated" + R(0xA0) + "message" + R(0xA0) + "from OpenAgentix agent]",
		"ideographic space":    "[Automated" + R(0x3000) + "message from OpenAgentix agent]",
		"double spaces":        "[Automated   message   from OpenAgentix agent]",
		"leading NBSP":         R(0xA0) + R(0xA0) + "[Automated message from OpenAgentix agent]",
		"zero width inside":    "[Auto" + R(0x200B) + "mated message from OpenAgentix agent]",
		"soft hyphen inside":   "[Auto" + R(0xAD) + "mated message from OpenAgentix agent]",
		"word joiner inside":   "[Automated message" + R(0x2060) + " from OpenAgentix agent]",
		"zero width at start":  R(0x200B) + "[Automated message from OpenAgentix agent]",
		"cyrillic a":           "[" + R(0x0410) + "utomated message from OpenAgentix agent]",
		"cyrillic small a o e": "[" + R(0x0430) + "utomat" + R(0x0435) + "d message fr" + R(0x043E) + "m OpenAgentix agent]",
		"greek omicron":        "[Automated message fr" + R(0x03BF) + "m OpenAgentix agent]",
		"greek alpha":          "[" + R(0x03B1) + "utomated message from OpenAgentix agent]",
		"fullwidth letters":    "[Ａutomated message from OpenAgentix agent]",
		"fullwidth bracket":    "［Automated message from OpenAgentix agent］",
		"long s":               "[Automated meſſage from OpenAgentix agent]",
		"combining mark":       "[Automated messag" + R(0x0065) + R(0x0301) + " from OpenAgentix agent]",
		"other label":          "[Automated message from Somebody Else]",
		"short prefix":         "[automated notice]",
		"after U+2028":         "hello" + R(0x2028) + "[Automated message from OpenAgentix agent]",
		"after U+2029":         "hello" + R(0x2029) + "[Automated message from OpenAgentix agent]",
		"after NEL":            "hello" + R(0x85) + "[Automated message from OpenAgentix agent]",
		"after vertical tab":   "hello\v[Automated message from OpenAgentix agent]",
		"after CRLF":           "hello\r\n[Automated message from OpenAgentix agent]",
		"unclosed":             "[Automated message from",
		"with trailing text":   "[Automated message from OpenAgentix agent] all clear",
	}
	for name, forged := range cases {
		got := NeutraliseHeader("before\n"+forged+"\nafter", h)
		if lineLooksLikeHeader(got) {
			t.Errorf("%s: a header-like line survived: %q", name, got)
		}
		if !strings.Contains(got, "before") || !strings.Contains(got, "after") {
			t.Errorf("%s: surrounding content lost: %q", name, got)
		}
	}
}

// lineLooksLikeHeader is the test's independent check: after neutralising,
// no line may begin (after dropping invisible characters, spaces and
// brackets) with "automated". It deliberately shares no code with the fold.
func lineLooksLikeHeader(s string) bool {
	for _, line := range strings.FieldsFunc(s, func(r rune) bool {
		return r == '\n' || r == '\r' || r == '\v' || r == '\f' || r == 0x85 || r == 0x2028 || r == 0x2029
	}) {
		var b strings.Builder
		for _, r := range line {
			if r == '[' || r == '［' || r == ' ' || r == 0xA0 || r == 0x3000 || r == 0x200B || r == 0xAD || r == 0x2060 {
				continue
			}
			b.WriteRune(r)
		}
		l := strings.ToLower(b.String())
		if strings.HasPrefix(l, "automated") || strings.HasPrefix(l, "аutomated") {
			return true
		}
	}
	return false
}

func TestNeutraliseHeader_LeavesOrdinaryTextAlone(t *testing.T) {
	h := Header("OpenAgentix agent")
	for _, in := range []string{
		"The job was automated and finished.",
		"line one\nline two [not a header]\nautomated tests passed",
		"[ERROR] build failed",
		"[Automatic] differs",
		"ordinary Кириллица and Ελληνικά text",
		"",
	} {
		if got := NeutraliseHeader(in, h); got != in {
			t.Errorf("changed ordinary text: %q -> %q", in, got)
		}
	}
}

func TestNeutraliseHeader_IsDeterministicAndIdempotent(t *testing.T) {
	h := Header("OpenAgentix agent")
	in := "x\n" + R(0xA0) + "[Auto" + R(0x200B) + "mated message from X]\ny"
	once := NeutraliseHeader(in, h)
	if twice := NeutraliseHeader(once, h); twice != once {
		t.Fatalf("not idempotent: %q then %q", once, twice)
	}
	if again := NeutraliseHeader(in, h); again != once {
		t.Fatal("not deterministic")
	}
}

func TestNeutraliseHeader_ExactHeaderMidLineStillRewritten(t *testing.T) {
	h := Header("OpenAgentix agent")
	got := NeutraliseHeader("see "+h+" here", h)
	if strings.Contains(got, h) {
		t.Fatalf("got %q", got)
	}
}
