package orchestration

import "testing"

func TestQuestionParser(t *testing.T) {
	tests := []struct {
		name            string
		chunks          []string
		wantQuestion    string
		wantVisibleText string
	}{
		{
			name:            "well formed block",
			chunks:          []string{"before\nQUESTION:\nWhich database should this use?\nEND QUESTION\nafter"},
			wantQuestion:    "Which database should this use?",
			wantVisibleText: "before\n",
		},
		{
			name:         "question text can contain the word QUESTION",
			chunks:       []string{"QUESTION:\nShould the QUESTION marker remain in the explanation?\nEND QUESTION"},
			wantQuestion: "Should the QUESTION marker remain in the explanation?",
		},
		{
			name:            "block split across stream chunks",
			chunks:          []string{"before\nQUEST", "ION:\nWhich option?\nEND QU", "ESTION\nafter"},
			wantQuestion:    "Which option?",
			wantVisibleText: "before\n",
		},
		{
			name:            "no block",
			chunks:          []string{"ordinary output\n", "with no blocker\n"},
			wantVisibleText: "ordinary output\nwith no blocker\n",
		},
		{
			name:            "block in the middle ignores the rest of its message",
			chunks:          []string{"before\nQUESTION:\nWhich option?\nEND QUESTION\nignored after the block"},
			wantQuestion:    "Which option?",
			wantVisibleText: "before\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parser := newQuestionParser()
			var visible string
			var question string
			for _, chunk := range tt.chunks {
				result := parser.Feed(chunk)
				visible += result.VisibleText
				if result.Found {
					question = result.Question
				}
			}
			visible += parser.Flush()

			if question != tt.wantQuestion {
				t.Fatalf("question = %q, want %q", question, tt.wantQuestion)
			}
			if visible != tt.wantVisibleText {
				t.Fatalf("visible text = %q, want %q", visible, tt.wantVisibleText)
			}
		})
	}
}
