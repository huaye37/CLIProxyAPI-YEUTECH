package executor

import (
	"bytes"
	"testing"
)

func TestClaudeStringBoundaryMatchesFirstQuoteOrEscape(t *testing.T) {
	for _, size := range []int{0, 1, 4095, 4096, 4097, 8193, 65537} {
		base := bytes.Repeat([]byte{'x'}, size)
		if got := claudeStringBoundary(base); got != -1 {
			t.Fatalf("size=%d got=%d", size, got)
		}
		for _, position := range []int{0, 1, 4095, 4096, size - 1} {
			if position < 0 || position >= size {
				continue
			}
			for _, first := range []byte{'"', '\\'} {
				input := bytes.Clone(base)
				input[position] = first
				if position+1 < size {
					input[position+1] = '"'
				}
				if got, want := claudeStringBoundary(input), bytes.IndexAny(input, "\"\\"); got != want {
					t.Fatalf("size=%d position=%d got=%d want=%d", size, position, got, want)
				}
			}
		}
	}
}
