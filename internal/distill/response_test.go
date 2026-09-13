package distill

import "testing"

func TestParseResponseShapes(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		wantErr   bool
		wantCount int
	}{
		{
			name:      "envelope with one statement",
			raw:       `{"preferences":[{"statement":"Prefers table-driven tests.","category":"Testing","confidence":0.9,"evidence":"said so"}]}`,
			wantCount: 1,
		},
		{
			name:      "bare array fallback",
			raw:       `[{"statement":"Uses tabs.","category":"General","confidence":0.7,"evidence":"observed"}]`,
			wantCount: 1,
		},
		{
			name:      "fenced envelope",
			raw:       "```json\n{\"preferences\":[{\"statement\":\"Uses tabs.\",\"category\":\"General\",\"confidence\":0.7,\"evidence\":\"observed\"}]}\n```",
			wantCount: 1,
		},
		{
			name:      "fenced with no language tag",
			raw:       "```\n{\"preferences\":[]}\n```",
			wantCount: 0,
		},
		{
			name:      "empty envelope",
			raw:       `{"preferences": []}`,
			wantCount: 0,
		},
		{
			name:      "bare empty array",
			raw:       `[]`,
			wantCount: 0,
		},
		{
			name:      "no changes literal",
			raw:       `no changes`,
			wantCount: 0,
		},
		{
			name:      "quoted no changes",
			raw:       `"no changes"`,
			wantCount: 0,
		},
		{
			name:      "empty string",
			raw:       "",
			wantCount: 0,
		},
		{
			name:      "whitespace only",
			raw:       "   \n\t  ",
			wantCount: 0,
		},
		{
			name:    "malformed json",
			raw:     `{not json`,
			wantErr: true,
		},
		{
			name:    "prose wrapped json",
			raw:     `Here you go: {"preferences": []}`,
			wantErr: true,
		},
		{
			name:    "truncated envelope",
			raw:     `{"preferences":[{"statement":"Uses tabs`,
			wantErr: true,
		},
		{
			name:      "confidence clamps above 1",
			raw:       `{"preferences":[{"statement":"x","category":"General","confidence":5.0,"evidence":"y"}]}`,
			wantCount: 1,
		},
		{
			name:      "confidence clamps below 0",
			raw:       `{"preferences":[{"statement":"x","category":"General","confidence":-3.0,"evidence":"y"}]}`,
			wantCount: 1,
		},
		{
			name:      "empty statement dropped",
			raw:       `{"preferences":[{"statement":"   ","category":"General","confidence":0.5,"evidence":"y"}]}`,
			wantCount: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseResponse(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseResponse(%q): expected error, got %v", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseResponse(%q): unexpected error: %v", tc.raw, err)
			}
			if len(got) != tc.wantCount {
				t.Fatalf("ParseResponse(%q): len = %d, want %d (%v)", tc.raw, len(got), tc.wantCount, got)
			}
		})
	}
}

func TestParseResponseConfidenceClampBounds(t *testing.T) {
	got, err := ParseResponse(`{"preferences":[{"statement":"x","category":"General","confidence":5.0,"evidence":"y"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Confidence != 1.0 {
		t.Fatalf("confidence not clamped to 1.0: %v", got)
	}

	got, err = ParseResponse(`{"preferences":[{"statement":"x","category":"General","confidence":-3.0,"evidence":"y"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Confidence != 0.0 {
		t.Fatalf("confidence not clamped to 0.0: %v", got)
	}
}

// TestParseResponseNeverPanics feeds ParseResponse a wide variety of
// malformed, truncated, and adversarial inputs and asserts none of them
// panic — the only contract this function is allowed to break on bad
// input is returning a non-nil error.
func TestParseResponseNeverPanics(t *testing.T) {
	inputs := []string{
		"",
		"{",
		"}",
		"[",
		"]",
		"null",
		"true",
		"42",
		`{"preferences":`,
		`{"preferences":null}`,
		`{"preferences":"not-an-array"}`,
		`{"preferences":[null]}`,
		`{"preferences":[{"statement":123}]}`,
		`{"preferences":[{"confidence":"high"}]}`,
		`[{"statement":null}]`,
		"```",
		"```json",
		"```json\n```",
		"\x00\x01\x02",
		"{{{{{{{{{{{{{{{{",
		"[[[[[[[[[[[[[[[[",
		string(make([]byte, 10000)),
		`{"preferences":[{"statement":"` + string(make([]byte, 5000)) + `"}]}`,
		"not json at all, just prose the model added despite instructions",
		`{"preferences": [{"statement": "ok", "category": "General", "confidence": 0.5, "evidence": "e"}], "extra": {"nested": {"deeply": [1,2,3]}}}`,
	}
	for i, in := range inputs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("input #%d panicked: %v (input=%q)", i, r, in)
				}
			}()
			_, _ = ParseResponse(in)
		}()
	}
}
