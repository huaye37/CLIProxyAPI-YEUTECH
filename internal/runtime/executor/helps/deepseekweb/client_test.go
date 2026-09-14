package deepseekweb

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHashMatchesOmniRoute(t *testing.T) {
	for input, want := range map[string]string{"": "e594808bc5b7151ac160c6d39a02e0a8e261ed588578403099e3561dc40c26b3", "abc": "f841106c601ce9be9bc38525e90d4178d47f21dd8eb9f238fc55ffaa4ca94506", strings.Repeat("星河", 100): "3aef344a9fedf43d0328dab41047793cb7325d5eb9d8fc0c7292385512dd35cc"} {
		got := hash(input)
		if hex.EncodeToString(got[:]) != want {
			t.Fatal("hash mismatch")
		}
	}
}
func testChallenge() Challenge {
	return Challenge{Algorithm: "DeepSeekHashV1", Challenge: "320706f8fdff5633bfc54e5cf938eece586b1c6b4cba6fe9ae45ca50232f062a", Salt: "salt", Difficulty: 4, ExpireAt: 2000000000, TargetPath: "/api/v0/chat/completion"}
}
func TestProofCancellationAndBounds(t *testing.T) {
	c := testChallenge()
	if _, err := Solve(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Solve(ctx, c); err != context.Canceled {
		t.Fatal(err)
	}
	c.Difficulty = 250001
	if _, err := Solve(context.Background(), c); err == nil {
		t.Fatal("accepted excessive work")
	}
}
func TestStreamPreservesProseAndReasoning(t *testing.T) {
	s := `data: {"v":{"response":{"fragments":[{"type":"THINK","content":"思考"}]}}}

data: {"p":"response/fragments","v":[{"type":"ANSWER","content":"SEARCH FINISHED 雨"}]}

data: {"p":"response/fragments/1/content","v":"没有停。"}

data: {"p":"response/status","v":"FINISHED"}

`
	got := map[string]string{}
	err := ReadStream(strings.NewReader(s), func(k, v string) error { got[k] += v; return nil })
	if err != nil || got["content"] != "SEARCH FINISHED 雨没有停。" || got["reasoning_content"] != "思考" {
		t.Fatal(got, err)
	}
}
func TestTruncatedStreamFails(t *testing.T) {
	if err := ReadStream(strings.NewReader("data: {\"v\":\"partial\"}\n"), func(string, string) error { return nil }); err != io.ErrUnexpectedEOF {
		t.Fatal(err)
	}
}
func TestProtocolAndSessionCleanup(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		token := r.Header.Get("Authorization")
		if r.URL.Path == "/api/v0/users/current" {
			if token != "Bearer private-user" {
				t.Error("wrong credential")
			}
			io.WriteString(w, `{"data":{"biz_data":{"token":"access"}}}`)
			return
		}
		if token != "Bearer access" {
			t.Error("wrong access token")
		}
		switch r.URL.Path {
		case "/api/v0/chat_session/create":
			io.WriteString(w, `{"data":{"biz_data":{"chat_session":{"id":"isolated"}}}}`)
		case "/api/v0/chat/create_pow_challenge":
			json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"biz_data": map[string]any{"challenge": testChallenge()}}})
		case "/api/v0/chat/completion":
			var b map[string]any
			json.NewDecoder(r.Body).Decode(&b)
			if b["prompt"] != "system: 全部\n\nuser: 历史" || b["chat_session_id"] != "isolated" || b["thinking_enabled"] != true || r.Header.Get("X-Ds-Pow-Response") == "" {
				t.Error("protocol mismatch")
			}
			io.WriteString(w, "data: [DONE]\n\n")
		case "/api/v0/chat_session/delete":
			io.WriteString(w, `{"data":{"biz_data":{}}}`)
		default:
			t.Error("unexpected path")
		}
	}))
	defer server.Close()
	c := Client{HTTP: server.Client(), BaseURL: server.URL}
	body, cleanup, err := c.Open(context.Background(), "private-user", "system: 全部\n\nuser: 历史", true)
	if err != nil {
		t.Fatal(err)
	}
	body.Close()
	cleanup()
	if len(calls) != 5 || calls[4] != "/api/v0/chat_session/delete" {
		t.Fatal(calls)
	}
}
