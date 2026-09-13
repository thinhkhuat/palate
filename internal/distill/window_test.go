package distill

import "testing"

func makeMessages(n int) []Message {
	out := make([]Message, n)
	for i := 0; i < n; i++ {
		out[i] = Message{Role: "user", Content: "msg", Index: i}
	}
	return out
}

func TestWindowNoCursorYet(t *testing.T) {
	all := makeMessages(5)
	prev, newMsgs := Window(all, -1)
	if len(prev) != 0 {
		t.Fatalf("previouslyAnalyzed = %d messages, want 0 (no cursor yet)", len(prev))
	}
	if len(newMsgs) != 5 {
		t.Fatalf("new = %d messages, want 5 (every message is new)", len(newMsgs))
	}
}

func TestWindowAtCursorEndReturnsNoNew(t *testing.T) {
	all := makeMessages(10)
	prev, newMsgs := Window(all, 9)
	if len(newMsgs) != 0 {
		t.Fatalf("new = %d messages, want 0 (cursor already at the end)", len(newMsgs))
	}
	if len(prev) != 10 {
		t.Fatalf("previouslyAnalyzed = %d messages, want 10 (max(20,0) clamped to transcript start)", len(prev))
	}
}

func TestWindowSplitsByCursorWithMax20Rule(t *testing.T) {
	all := makeMessages(50)

	// 3 new messages (index 47,48,49): previouslyAnalyzed should be
	// max(20,3)=20 messages immediately before the split.
	prev, newMsgs := Window(all, 46)
	if len(newMsgs) != 3 {
		t.Fatalf("new = %d messages, want 3", len(newMsgs))
	}
	if len(prev) != 20 {
		t.Fatalf("previouslyAnalyzed = %d messages, want 20 (max(20, len(new)) rule)", len(prev))
	}
	if prev[len(prev)-1].Index != 46 {
		t.Fatalf("previouslyAnalyzed ends at Index %d, want 46 (immediately before the first new message)", prev[len(prev)-1].Index)
	}
	if newMsgs[0].Index != 47 {
		t.Fatalf("new starts at Index %d, want 47", newMsgs[0].Index)
	}

	// 25 new messages: previouslyAnalyzed should be max(20,25)=25.
	prev, newMsgs = Window(all, 24)
	if len(newMsgs) != 25 {
		t.Fatalf("new = %d messages, want 25", len(newMsgs))
	}
	if len(prev) != 25 {
		t.Fatalf("previouslyAnalyzed = %d messages, want 25 (max(20, len(new)) rule)", len(prev))
	}
}

func TestWindowClampsToTranscriptStart(t *testing.T) {
	all := makeMessages(5)
	prev, newMsgs := Window(all, -1)
	if len(prev) != 0 {
		t.Fatalf("previouslyAnalyzed = %d messages, want 0 (fewer than 20 messages exist before the split)", len(prev))
	}
	if len(newMsgs) != 5 {
		t.Fatalf("new = %d messages, want 5", len(newMsgs))
	}
}
