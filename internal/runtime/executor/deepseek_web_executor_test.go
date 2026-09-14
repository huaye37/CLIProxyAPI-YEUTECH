package executor

import (
	"strings"
	"testing"
)

func TestDeepSeekCompleteHistory(t *testing.T) {
	raw := []byte(`{"messages":[{"role":"system","content":"设定，不删。"},{"role":"user","content":"第一轮"},{"role":"assistant","content":"之前正文"},{"role":"user","content":"继续"}]}`)
	got, err := deepSeekPrompt(raw)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"设定，不删。", "第一轮", "之前正文", "继续"} {
		if !strings.Contains(got, s) {
			t.Fatal("lost context", s)
		}
	}
}
func TestDeepSeekSinglePromptUnchanged(t *testing.T) {
	got, err := deepSeekPrompt([]byte(`{"messages":[{"role":"user","content":"  雨\n\n停了吗？  "}]}`))
	if err != nil || got != "  雨\n\n停了吗？  " {
		t.Fatal(got, err)
	}
}
func TestDeepSeekRejectsUnsupportedControls(t *testing.T) {
	for _, raw := range []string{`{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.com"}}]}]}`, `{"messages":[{"role":"user","content":"hi"}],"reasoning_effort":"high"}`, `{"messages":[{"role":"tool","content":"result"}]}`, `{"messages":[{"role":"user","content":"hi"}],"tools":[]}`} {
		if _, err := deepSeekPrompt([]byte(raw)); err == nil {
			t.Fatal("unsupported request accepted")
		}
	}
}
