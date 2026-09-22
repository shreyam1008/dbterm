package uniseg

import "testing"

func TestIndicConjunctsStayTogether(t *testing.T) {
	tests := []struct {
		text  string
		width int
		count int
		first string
	}{
		{text: "क्ष", width: 1, count: 1, first: "क्ष"},
		{text: "श्रे", width: 1, count: 1, first: "श्रे"},
		{text: "अच्युत थापा क्षेत्री", width: 9, count: 9, first: "अ"},
	}

	for _, test := range tests {
		if got := StringWidth(test.text); got != test.width {
			t.Errorf("StringWidth(%q) = %d, want %d", test.text, got, test.width)
		}
		if got := GraphemeClusterCount(test.text); got != test.count {
			t.Errorf("GraphemeClusterCount(%q) = %d, want %d", test.text, got, test.count)
		}
		cluster, rest, width, _ := FirstGraphemeClusterInString(test.text, -1)
		if cluster != test.first || width == 0 || (test.text == test.first && rest != "") {
			t.Errorf("FirstGraphemeClusterInString(%q) = (%q, %q, %d), unexpected", test.text, cluster, rest, width)
		}
	}
}

func TestTerminalWidthsForEmojiAndEastAsianText(t *testing.T) {
	tests := map[string]int{
		"👩‍💻":  2,
		"🏳️‍🌈": 2,
		"中":    2,
		"é":   1,
	}
	for text, want := range tests {
		if got := StringWidth(text); got != want {
			t.Errorf("StringWidth(%q) = %d, want %d", text, got, want)
		}
	}
}

func TestStepStringReportsHardBreaks(t *testing.T) {
	cluster, rest, _, _ := StepString("नेपाली\nअक्षर", -1)
	if cluster == "" || rest == "" {
		t.Fatalf("StepString did not advance through Nepali text: cluster=%q rest=%q", cluster, rest)
	}

	cluster, rest, boundaries, _ := StepString("\nअक्षर", -1)
	if cluster != "\n" || rest != "अक्षर" || boundaries&MaskLine != LineMustBreak {
		t.Fatalf("newline StepString returned cluster=%q rest=%q boundary=%d", cluster, rest, boundaries&MaskLine)
	}
}
