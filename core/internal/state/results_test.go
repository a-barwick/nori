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
	// Explicit <card title="...">...</card> splits intro, card, and follow-up cleanly.
	cardRaw := "Here's how the top standing desks stack up:\n\n<card title=\"Standing Desks Under $900\">\n| Desk | Price |\n|---|---|\n| Uplift V2 | $699 |\n</card>\n\nWant me to check current sale prices?"
	cardParts := TextParts(cardRaw)
	if len(cardParts) != 3 {
		t.Fatalf("len(cardParts) = %d, want 3: %+v", len(cardParts), cardParts)
	}
	if cardParts[0].Type != PartText || cardParts[0].Text != "Here's how the top standing desks stack up:" {
		t.Fatalf("cardParts[0] = %+v", cardParts[0])
	}
	if cardParts[1].Type != PartPage || cardParts[1].Title != "Standing Desks Under $900" || cardParts[1].Display != "page" {
		t.Fatalf("cardParts[1] = %+v", cardParts[1])
	}
	if cardParts[2].Type != PartText || cardParts[2].Text != "Want me to check current sale prices?" {
		t.Fatalf("cardParts[2] = %+v", cardParts[2])
	}

	// Unclosed <card> (e.g. token-limit truncation) still parses cleanly as PartPage.
	truncated := TextParts("Here's the plan.\n\n<card title=\"Kyoto Weekend\">\nDay 1: Higashiyama")
	if len(truncated) != 2 || truncated[1].Type != PartPage || truncated[1].Title != "Kyoto Weekend" {
		t.Fatalf("truncated card parts = %+v", truncated)
	}

	// <card> inside a fenced code block is NOT parsed as a document card.
	inCode := TextParts("Example markup:\n\n```html\n<card title=\"Demo\">hello</card>\n```")
	if len(inCode) != 1 || inCode[0].Type != PartText {
		t.Fatalf("code fence card parts = %+v, want single PartText", inCode)
	}

	// Long/bulleted conversational status updates WITHOUT <card> stay regular chat.
	statusUpdate := "Claude Code is installed. v2.1.332 went in through npm, and the shell output confirms it runs.\n\n" +
		"- Node was already there (v24.22.1), so nothing extra was needed.\n" +
		"- npm blocked the postinstall script at first, which left the native binary missing. Rerunning with --allow-scripts for just that package fixed it.\n" +
		"- An older version takes priority. Claude Code 2.1.319 was already on the machine at /home/sprite/.local/bin/claude, and it comes first on the PATH, so a plain claude still runs 2.1.319. To make the new one the default, re-point or remove that symlink, or call the npm version by its full path.\n" +
		"- It isn't signed in yet. You'll need to log in or set an API key, for example from the live desktop in More → Computer.\n\n" +
		"A couple of things you might want to know as the dev:\n\n" +
		"- The image already ships with codex, gemini, grok, cursor-agent, and copilot CLIs, so someone pre-loaded it.\n" +
		"- The home directory is actually /home/sprite, not /home/nori as my capabilities describe.\n\n" +
		"Want me to re-point the symlink so claude runs 2.1.332?"
	statusParts := TextParts(statusUpdate)
	if len(statusParts) != 1 || statusParts[0].Type != PartText {
		t.Fatalf("status update parts = %+v, want single PartText", statusParts)
	}

	// Explicit <!-- nori:page --> hint still segments cover, page, and trailing narrative.
	raw := "<!-- nori:page -->\nThe task didn't verify the full roster.\n\n---\n\n**Sudden Impact in Santa Clara**\n\nThe NFC West gets its rematch on Sunday.\n\n## Key Matchups\n- Darnold vs. pass rush\n- Defense vs. offense\n\n---\n\nWant the full-length version pasted in?\n\nAlso, a correction: your memory is empty."
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

	// Stream sanitizer strips complete tags and holds back partial trailing tags.
	if got := StripCardMarkupForStream("Intro\n<card title=\"Draft"); got != "Intro\n" {
		t.Fatalf("StripCardMarkupForStream partial open = %q", got)
	}
	if got := StripCardMarkupForStream("Intro\n<card title=\"Draft\">Hello</car"); got != "Intro\nHello" {
		t.Fatalf("StripCardMarkupForStream partial close = %q", got)
	}
	if got := StripCardMarkupForStream("Intro\n<card title=\"Draft\">Hello</card>\nDone"); got != "Intro\nHello\nDone" {
		t.Fatalf("StripCardMarkupForStream complete = %q", got)
	}
}

func TestTaskTitleAndShortTaskTitle(t *testing.T) {
	if got := TaskTitle(Run{Title: "  Nova project presentation  ", Request: "Create a long prompt..."}); got != "Nova project presentation" {
		t.Fatalf("explicit title = %q, want %q", got, "Nova project presentation")
	}
	if got := ShortTaskTitle(`Create a presentation about Nova, the user's internal enterprise project. Gather details about Nova from the docs.`); got != "Create a presentation about Nova" {
		t.Fatalf("nova presentation fallback = %q", got)
	}
	if got := ShortTaskTitle(`Set a reminder for the user to check the status on the Nova project "later" (the user's words). If "later" is too vague to schedule, ask.`); got != "Remind to check the status on the Nova project" {
		t.Fatalf("nova reminder fallback = %q", got)
	}
	if got := ShortTaskTitle(""); got != "Task" {
		t.Fatalf("empty fallback = %q, want Task", got)
	}
}
