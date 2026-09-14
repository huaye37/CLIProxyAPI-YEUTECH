package deepseekweb

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string   { return e.Message }
func (e *Error) StatusCode() int { return e.Code }

type Client struct {
	HTTP    *http.Client
	BaseURL string
}

func (c Client) call(ctx context.Context, token, path string, body any, pow string) (*http.Response, error) {
	var data io.Reader
	method := "GET"
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		data = bytes.NewReader(b)
		method = "POST"
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, data)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Origin", "https://chat.deepseek.com")
	req.Header.Set("Referer", "https://chat.deepseek.com/")
	req.Header.Set("X-App-Version", "20241129.1")
	req.Header.Set("X-Client-Platform", "web")
	req.Header.Set("X-Client-Version", "2.0.0")
	if pow != "" {
		req.Header.Set("X-Ds-Pow-Response", pow)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("DeepSeek transport failed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		_ = resp.Body.Close()
		return nil, &Error{resp.StatusCode, fmt.Sprintf("DeepSeek returned HTTP %d", resp.StatusCode)}
	}
	return resp, nil
}
func (c Client) jsonCall(ctx context.Context, token, path string, body any) (json.RawMessage, error) {
	resp, err := c.call(ctx, token, path, body, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var env struct {
		Code int `json:"code"`
		Data struct {
			Code  int             `json:"biz_code"`
			Value json.RawMessage `json:"biz_data"`
		} `json:"data"`
		Value json.RawMessage `json:"biz_data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&env); err != nil {
		return nil, &Error{502, "Invalid DeepSeek response"}
	}
	if env.Code != 0 || env.Data.Code != 0 {
		return nil, &Error{502, "DeepSeek rejected request"}
	}
	if len(env.Data.Value) > 0 {
		return env.Data.Value, nil
	}
	return env.Value, nil
}
func (c Client) Open(ctx context.Context, userToken, prompt string, thinking bool) (io.ReadCloser, func(), error) {
	raw, err := c.jsonCall(ctx, userToken, "/api/v0/users/current", nil)
	if err != nil {
		return nil, nil, err
	}
	var user struct {
		Token string `json:"token"`
	}
	if json.Unmarshal(raw, &user) != nil || user.Token == "" {
		return nil, nil, &Error{401, "DeepSeek credential is invalid or expired"}
	}
	raw, err = c.jsonCall(ctx, user.Token, "/api/v0/chat_session/create", map[string]any{})
	if err != nil {
		return nil, nil, err
	}
	var session struct {
		Session struct {
			ID string `json:"id"`
		} `json:"chat_session"`
	}
	if json.Unmarshal(raw, &session) != nil || session.Session.ID == "" {
		return nil, nil, &Error{502, "DeepSeek did not create a session"}
	}
	cleanup := func() {
		_, _ = c.jsonCall(ctx, user.Token, "/api/v0/chat_session/delete", map[string]any{"chat_session_id": session.Session.ID})
	}
	fail := func(err error) (io.ReadCloser, func(), error) { cleanup(); return nil, nil, err }
	raw, err = c.jsonCall(ctx, user.Token, "/api/v0/chat/create_pow_challenge", map[string]any{"target_path": "/api/v0/chat/completion"})
	if err != nil {
		return fail(err)
	}
	var envelope struct {
		Challenge Challenge `json:"challenge"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return fail(&Error{502, "Invalid DeepSeek challenge"})
	}
	proof, err := Solve(ctx, envelope.Challenge)
	if err != nil {
		return fail(err)
	}
	resp, err := c.call(ctx, user.Token, "/api/v0/chat/completion", map[string]any{"chat_session_id": session.Session.ID, "parent_message_id": nil, "model_type": "default", "prompt": prompt, "ref_file_ids": []string{}, "thinking_enabled": thinking, "search_enabled": false, "preempt": false}, proof)
	if err != nil {
		return fail(err)
	}
	return resp.Body, cleanup, nil
}

// ReadStream retains content verbatim and does not treat words inside prose as control messages.
func ReadStream(r io.Reader, emit func(string, string) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	kind := "content"
	path := ""
	finished := false
	var visit func(map[string]any, string) error
	visit = func(m map[string]any, parent string) error {
		p, _ := m["p"].(string)
		if p == "" {
			p = parent
		}
		v := m["v"]
		if n, ok := m["code"].(float64); ok && n != 0 {
			return &Error{502, "DeepSeek stream error"}
		}
		if m["error"] != nil {
			return &Error{502, "DeepSeek stream error"}
		}
		if p != "" {
			path = p
		}
		if s, ok := v.(string); ok {
			if strings.HasSuffix(p, "status") {
				if s == "FINISHED" {
					finished = true
				}
				return nil
			}
			if strings.Contains(p, "search") || strings.HasSuffix(p, "type") {
				return nil
			}
			if p == "" || strings.HasSuffix(p, "content") {
				if strings.Contains(path, "thinking") {
					kind = "reasoning_content"
				}
				return emit(kind, s)
			}
			return nil
		}
		fragment := func(f map[string]any) error {
			if t, ok := f["type"].(string); ok {
				switch t {
				case "THINK":
					kind = "reasoning_content"
				case "ANSWER", "RESPONSE":
					kind = "content"
				default:
					return &Error{502, "Unsupported DeepSeek fragment"}
				}
			}
			if s, ok := f["content"].(string); ok {
				return emit(kind, s)
			}
			return nil
		}
		if obj, ok := v.(map[string]any); ok {
			if response, ok := obj["response"].(map[string]any); ok {
				if response["thinking_enabled"] == true {
					kind = "reasoning_content"
				}
				if frags, ok := response["fragments"].([]any); ok {
					for _, f := range frags {
						if f, ok := f.(map[string]any); ok {
							if err := fragment(f); err != nil {
								return err
							}
						}
					}
				}
			}
			if strings.HasSuffix(p, "fragments") {
				return fragment(obj)
			}
		}
		if arr, ok := v.([]any); ok {
			for _, item := range arr {
				if obj, ok := item.(map[string]any); ok {
					var err error
					if strings.HasSuffix(p, "fragments") {
						err = fragment(obj)
					} else {
						err = visit(obj, p)
					}
					if err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		s := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if s == "[DONE]" {
			return nil
		}
		var m map[string]any
		if json.Unmarshal([]byte(s), &m) != nil {
			return &Error{502, "Malformed DeepSeek stream"}
		}
		if err := visit(m, ""); err != nil {
			return err
		}
		if finished {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return io.ErrUnexpectedEOF
}
