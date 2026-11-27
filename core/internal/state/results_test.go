package state

import "testing"

func TestExtractDocumentTitleAndPlainExcerpt(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		want     string
		wantRest string
	}{
		{
			name:     "quoted Title prefix",
			text:     "Title: \"Sudden Impact in Santa Clara: Can Darnold's Return Slow the Niners?\"\n\nThe NFC West gets its rematch.",
			want:     "Sudden Impact in Santa Clara: Can Darnold's Return Slow the Niners?",
			wantRest: "The NFC West gets its rematch.",
		},
		{
			name:     "markdown heading",
			text:     "# Weekly Ops Summary\n\nAll systems green.",
			want:     "Weekly Ops Summary",
			wantRest: "All systems green.",
		},
		{
			name:     "bold opening title line",
			text:     "**Trip Plan: San Diego**\n\nFlights and hotel options.",
			want:     "Trip Plan: San Diego",
			wantRest: "Flights and hotel options.",
		},
		{
			name:     "plain prose falls back to empty",
			text:     "Here is a regular conversational sentence.\n\nMore details.",
			want:     "",
			wantRest: "Here is a regular conversational sentence.\n\nMore details.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, rest := ExtractDocumentTitle(tc.text)
			if got != tc.want {
				t.Fatalf("ExtractDocumentTitle title = %q, want %q", got, tc.want)
			}
			if rest != tc.wantRest {
				t.Fatalf("ExtractDocumentTitle rest = %q, want %q", rest, tc.wantRest)
			}
		})
	}

	if got := DocumentTitle("Plain text answer.", "Research Seahawks game"); got != "Research Seahawks game" {
		t.Fatalf("DocumentTitle fallback = %q, want %q", got, "Research Seahawks game")
	}

	excerpt := PlainExcerpt("## Matchup\n\nThe **Seahawks** visit [Levi's Stadium](https://example.com) on Sunday.", 120)
	if excerpt != "Matchup The Seahawks visit Levi's Stadium on Sunday." {
		t.Fatalf("PlainExcerpt = %q", excerpt)
	}
}

func TestTextPartsSegmentsCoverPageAndTrailingNarrative(t *testing.T) {
	raw := "The task didn't verify the full roster.\n\n---\n\n**Sudden Impact in Santa Clara**\n\nThe NFC West gets its rematch on Sunday.\n\n## Key Matchups\n- Darnold vs. pass rush\n- Defense vs. offense\n\n---\n\nWant the full-length version pasted in?\n\nAlso, a correction: your memory is empty."

	parts := TextParts(raw)
	if len(parts) != 3 {
		t.Fatalf("len(parts) = %d, want 3: %+v", len(parts), parts)
	}
	if parts[0].Type != PartText || parts[0].Text != "The task didn't verify the full roster." {
		t.Fatalf("parts[0] = %+v", parts[0])
	}
	if parts[1].Type != PartPage || parts[1].Title != "Sudden Impact in Santa Clara" {
		t.Fatalf("parts[1] = %+v", parts[1])
	}
	if parts[2].Type != PartText || parts[2].Text != "Want the full-length version pasted in?\n\nAlso, a correction: your memory is empty." {
		t.Fatalf("parts[2] = %+v", parts[2])
	}

	// Short conversational replies stay a single plain text part.
	short := TextParts("Sounds good! I'll check that now.")
	if len(short) != 1 || short[0].Type != PartText {
		t.Fatalf("short parts = %+v, want single PartText", short)
	}

	// Bubble hint forces a single bubble even for structured text.
	forced := TextParts("<!-- nori:bubble -->\n## Quick List\n- One\n- Two")
	if len(forced) != 1 || forced[0].Type != PartText || forced[0].Display != "bubble" {
		t.Fatalf("forced bubble parts = %+v", forced)
	}
}

func TestTaskTitleAndShortTaskTitle(t *testing.T) {
	if got := TaskTitle(Run{Title: "  Nova project presentation  ", Request: "Create a long prompt..."}); got != "Nova project presentation" {
		t.Fatalf("explicit title = %q, want %q", got, "Nova project presentation")
	}
	if got := ShortTaskTitle(`Create a presentation about Nova, the user's internal enterprise project. Gather details about Nova from the docs.`); got != "Create a presentation about Nova" {
		t.Fatalf("nova presentation fallback = %q", got)
	}
	if got := ShortTaskTitle(`Set a reminder for the user to check the status on the Nova project "later" (the user's words). If "later" is too vague to schedule, ask.`); got != `Set a reminder to check the status on the Nova project "later"` {
		t.Fatalf("nova reminder fallback = %q", got)
	}
	if got := ShortTaskTitle(""); got != "Task" {
		t.Fatalf("empty fallback = %q, want Task", got)
	}
}
