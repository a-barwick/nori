package state

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestWindowAndCompactionConstants(t *testing.T) {
	if HistoryTurnsExamined != 64 {
		t.Fatalf("HistoryTurnsExamined = %d, want 64", HistoryTurnsExamined)
	}
	if RecentTurnLimit != 36 {
		t.Fatalf("RecentTurnLimit = %d, want 36", RecentTurnLimit)
	}
	if RecentTurnBytes != 64<<10 {
		t.Fatalf("RecentTurnBytes = %d, want %d", RecentTurnBytes, 64<<10)
	}
	if SoftCompactUserTurns != 16 {
		t.Fatalf("SoftCompactUserTurns = %d, want 16", SoftCompactUserTurns)
	}
	if SoftCompactTurns != 28 {
		t.Fatalf("SoftCompactTurns = %d, want 28", SoftCompactTurns)
	}
	if SoftCompactBytes != 48<<10 {
		t.Fatalf("SoftCompactBytes = %d, want %d", SoftCompactBytes, 48<<10)
	}
	if CompactKeep != 14 {
		t.Fatalf("CompactKeep = %d, want 14", CompactKeep)
	}
	if CompactKeepUserTurns != 10 {
		t.Fatalf("CompactKeepUserTurns = %d, want 10", CompactKeepUserTurns)
	}
	if ExtractTurnThreshold != 3 {
		t.Fatalf("ExtractTurnThreshold = %d, want 3", ExtractTurnThreshold)
	}
	if IdleExtractDelay != 2*time.Minute {
		t.Fatalf("IdleExtractDelay = %v, want 2m", IdleExtractDelay)
	}
	if !(CompactKeepUserTurns <= CompactKeep && CompactKeep < SoftCompactUserTurns && SoftCompactUserTurns < SoftCompactTurns && SoftCompactTurns < RecentTurnLimit && RecentTurnLimit < HistoryTurnsExamined) {
		t.Fatalf("unexpected ordering of turn window constants")
	}
	if !(SoftCompactBytes < RecentTurnBytes && RecentTurnBytes < OuterInputBytes) {
		t.Fatalf("unexpected ordering of byte window constants")
	}
}

func TestHistoryWindow_WidenedTurnAndByteLimits(t *testing.T) {
	base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	// Accepts up to RecentTurnLimit (36) turns without truncating; 37th truncates.
	w := NewHistoryWindow()
	for i := range RecentTurnLimit {
		turn := ConversationTurn{
			ID:        fmt.Sprintf("turn-%d", i),
			Status:    "completed",
			StartedAt: base.Add(time.Duration(RecentTurnLimit-i) * time.Second),
			Protocol:  []string{`{"role":"user","content":"hi"}`},
		}
		if !w.Add(turn) {
			t.Fatalf("HistoryWindow.Add rejected turn %d within RecentTurnLimit=%d", i, RecentTurnLimit)
		}
	}
	if w.Truncated || len(w.Turns) != RecentTurnLimit {
		t.Fatalf("expected %d untruncated turns, got len=%d truncated=%v", RecentTurnLimit, len(w.Turns), w.Truncated)
	}
	extra := ConversationTurn{
		ID:        "turn-extra",
		Status:    "completed",
		StartedAt: base,
		Protocol:  []string{`{"role":"user","content":"overflow"}`},
	}
	if w.Add(extra) || !w.Truncated {
		t.Fatalf("expected turn beyond RecentTurnLimit (%d) to truncate", RecentTurnLimit)
	}

	// Accepts turns up to RecentTurnBytes (64 KiB) without truncating; next truncates.
	bw := NewHistoryWindow()
	chunk := strings.Repeat("a", 8<<10) // 8 KiB each -> 8 fit in 64 KiB
	for i := range 8 {
		turn := ConversationTurn{
			ID:        fmt.Sprintf("bturn-%d", i),
			Status:    "completed",
			StartedAt: base.Add(time.Duration(10-i) * time.Second),
			Protocol:  []string{chunk},
		}
		if !bw.Add(turn) {
			t.Fatalf("HistoryWindow.Add rejected byte turn %d within RecentTurnBytes=%d", i, RecentTurnBytes)
		}
	}
	if bw.Truncated || len(bw.Turns) != 8 {
		t.Fatalf("expected 8x8KiB turns to fit in 64KiB RecentTurnBytes, got len=%d truncated=%v", len(bw.Turns), bw.Truncated)
	}
	overflowTurn := ConversationTurn{
		ID:        "bturn-overflow",
		Status:    "completed",
		StartedAt: base,
		Protocol:  []string{"x"},
	}
	if bw.Add(overflowTurn) || !bw.Truncated {
		t.Fatalf("expected turn exceeding RecentTurnBytes (%d) to truncate", RecentTurnBytes)
	}
}
