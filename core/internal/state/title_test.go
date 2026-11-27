package state

import "testing"

func TestTaskTitlePrefersExplicitTitle(t *testing.T) {
	got := TaskTitle(Run{Title: "Nova presentation", Request: "Create a presentation about Nova..."})
	if got != "Nova presentation" {
		t.Fatalf("TaskTitle = %q, want Nova presentation", got)
	}
}

func TestTitleFromRequestStripsPromptLeakageAndSecondSentences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "vague reminder brief",
			in:   `Set a reminder for the user to check the status on the Nova project "later" (the user's words). If "later" is too vague to schedule, ask the user when they'd like to be reminded.`,
			want: "Remind to check the status on the Nova project",
		},
		{
			name: "presentation brief",
			in:   "Create a presentation about Nova, the user's internal enterprise project. Gather details about Nova from the user's connected apps (e.g., docs, drive, email) if available.",
			want: "Create a presentation about Nova",
		},
		{
			name: "routine brief",
			in:   "Create a recurring Monday 8:00 AM Pacific routine to email the user a summary of their upcoming week from Google Calendar.",
			want: "Create a recurring Monday 8:00 AM Pacific routine to email a summary of…",
		},
		{
			name: "empty request",
			in:   "   ",
			want: "Task",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TitleFromRequest(tc.in)
			if got != tc.want {
				t.Fatalf("TitleFromRequest = %q, want %q", got, tc.want)
			}
			if len(got) > maxTaskTitleLen+len("…") {
				t.Fatalf("title too long (%d bytes): %q", len(got), got)
			}
		})
	}
}
