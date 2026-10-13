package learn

import (
	"testing"

	"github.com/thinhkhuat/palate/internal/storage"
)

func TestRewriteAllowed(t *testing.T) {
	db, cfg := testDB(t)

	t.Run("STATED is never rewritten", func(t *testing.T) {
		p := Preference{ID: "p1", TrustTier: "STATED", Statement: "Use tabs for indentation."}
		if ok, reason := RewriteAllowed(db, p, "Use tabs.", false); ok {
			t.Fatalf("STATED must never be rewritten, reason=%q", reason)
		}
	})

	t.Run("a non-ASCII line must stay non-ASCII", func(t *testing.T) {
		p := Preference{ID: "p2", TrustTier: "DISTILLED", Statement: "Thích dùng tab để căn lề trong mọi tệp."}
		if ok, _ := RewriteAllowed(db, p, "Prefers tabs for indentation.", false); ok {
			t.Fatal("a rewrite must not anglicize a non-ASCII original")
		}
	})

	t.Run("a longer rewrite is refused", func(t *testing.T) {
		p := Preference{ID: "p3", TrustTier: "DISTILLED", Statement: "Use tabs."}
		if ok, _ := RewriteAllowed(db, p, "Use tabs for indentation consistently everywhere in the codebase.", false); ok {
			t.Fatal("a rewrite must not grow the text")
		}
	})

	t.Run("a shorter same-script rewrite is allowed", func(t *testing.T) {
		p := Preference{ID: "p4", TrustTier: "DISTILLED", Statement: "Prefers using tabs for indentation in all files."}
		ok, reason := RewriteAllowed(db, p, "Prefers tabs for indentation.", false)
		if !ok {
			t.Fatalf("a shorter same-script rewrite must be allowed, got refused: %s", reason)
		}
	})

	t.Run("requireNewEvidence", func(t *testing.T) {
		p, err := ApplySignal(db, cfg, Signal{Type: SignalLLMDistilled, Statement: "Prefers using tabs for indentation in all files.", Category: "Style", Weight: 0.5}, nil)
		if err != nil {
			t.Fatal(err)
		}
		row := preferenceByID(db, p.ID)
		if row == nil {
			t.Fatal("row not found after ApplySignal")
		}

		// Simulate a rewrite already having happened: no create/reinforce
		// event exists after it yet.
		if err := storage.AppendEvent(db, "consolidate_rewrite", `{"id":"`+p.ID+`"}`); err != nil {
			t.Fatal(err)
		}
		if ok, reason := RewriteAllowed(db, *row, "Prefers tabs.", true); ok {
			t.Fatal("no new evidence since the last rewrite must refuse")
		} else if reason == "" {
			t.Fatal("a refusal must carry a reason")
		}

		// A new reinforcement happens after the rewrite.
		if _, err := ApplySignal(db, cfg, Signal{Type: SignalLLMDistilled, Statement: "Prefers using tabs for indentation in all files.", Category: "Style", Weight: 0.5}, nil); err != nil {
			t.Fatal(err)
		}
		if ok, reason := RewriteAllowed(db, *row, "Prefers tabs.", true); !ok {
			t.Fatalf("a new reinforce event after the last rewrite must allow the rewrite: %s", reason)
		}
	})
}
