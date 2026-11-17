package executor

import (
	"reflect"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestClaudeScannerShortTokenRunsMatchBytewise(t *testing.T) {
	for _, size := range []int{0, 1, 255, 256, 257, 4097} {
		input := []byte(strings.Repeat("x", size))
		var batch, bytewise claudeStreamDocScanner
		batch.resetShortToken()
		bytewise.resetShortToken()
		for _, c := range input {
			bytewise.appendShortByte(c)
		}
		middle := len(input) / 2
		batch.appendShortBytes(input[:middle])
		batch.appendShortBytes(input[middle:])
		if batch.strLen != bytewise.strLen || batch.strBuf != bytewise.strBuf || !reflect.DeepEqual(batch.strExtra, bytewise.strExtra) {
			// A spilled batch need not fill the unused stack tail; compare the actual token.
			actual := batch.strBuf[:min(batch.strLen, len(batch.strBuf))]
			expected := bytewise.strBuf[:min(bytewise.strLen, len(bytewise.strBuf))]
			if len(batch.strExtra) > 0 {
				actual = batch.strExtra
			}
			if len(bytewise.strExtra) > 0 {
				expected = bytewise.strExtra
			}
			if string(actual) != string(expected) {
				t.Fatalf("size %d: batch token differs from bytewise", size)
			}
		}
		batch.resetShortToken()
		batch.appendShortBytes([]byte("messages"))
		if got := batch.decodeShortToken(); got != "messages" {
			t.Fatalf("size %d: reset token = %q", size, got)
		}
	}
}

func TestClaudeScannerTokenRunsPreserveFragmentedEscapes(t *testing.T) {
	raw := []byte(`{"` + strings.Repeat("ignored", 50) + `":"ignored","system":[{"type":"text","text":"sys","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"\u0075ser","content":[{"type":"text","text":"hello\\\"world","\u0063ache_control":{"type":"ephemeral","ttl":"1h"}}]},{"role":"assistant","content":[{"type":"redacted_\u0074hinking","data":"x"}]}]}`)
	var whole claudeStreamDocScanner
	whole.scan(ojson.NewDoc(raw), true, false)
	if len(whole.sysCC) != 1 || len(whole.msgCC) != 1 || whole.firstUserIdx != 0 || whole.messagesLen != 2 {
		t.Fatalf("unexpected whole scanner result: system=%d message=%d first=%d length=%d", len(whole.sysCC), len(whole.msgCC), whole.firstUserIdx, whole.messagesLen)
	}
	for _, chunkSize := range []int{1, 2, 3, 7, 255, 256, 257} {
		var chunked claudeStreamDocScanner
		chunked.scan(ojson.NewDoc(nil), true, false)
		for start := 0; start < len(raw); start += chunkSize {
			if _, err := chunked.Write(raw[start:min(start+chunkSize, len(raw))]); err != nil {
				t.Fatal(err)
			}
		}
		if !reflect.DeepEqual(chunked.sysCC, whole.sysCC) || !reflect.DeepEqual(chunked.msgCC, whole.msgCC) || chunked.firstUserIdx != whole.firstUserIdx || chunked.lastEligibleIndex != whole.lastEligibleIndex || chunked.lastEligibleTargetBlockIndex != whole.lastEligibleTargetBlockIndex || chunked.messagesLen != whole.messagesLen {
			t.Fatalf("chunk size %d changed scanner semantics", chunkSize)
		}
	}
}
