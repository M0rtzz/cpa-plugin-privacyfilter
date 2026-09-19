package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func message(text, kind string) map[string]any {
	return map[string]any{
		"type": "message", "role": "user",
		"content": []any{map[string]any{"type": "input_text", "text": text}},
		"internal_chat_message_metadata_passthrough": map[string]any{
			"turn_id": "same-turn", "content_item_kinds": []string{kind},
		},
	}
}

func requestBody(t *testing.T, items ...any) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"model": "test-model", "input": items})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func rejectionCode(t *testing.T, resp pluginapi.RequestInterceptResponse) string {
	t.Helper()
	if !resp.Terminate {
		if resp.Body != nil || resp.ResponseBody != nil {
			t.Fatal("pass-through must leave body unchanged")
		}
		return ""
	}
	if resp.StatusCode != http.StatusBadRequest || !strings.HasPrefix(resp.ResponseHeaders.Get("Content-Type"), "application/json") {
		t.Fatalf("invalid rejection status/headers: %+v", resp)
	}
	var v struct {
		Error policyError `json:"error"`
	}
	if err := json.Unmarshal(resp.ResponseBody, &v); err != nil {
		t.Fatal(err)
	}
	if v.Error.Type != "invalid_request_error" {
		t.Fatalf("error type = %s", v.Error.Type)
	}
	return v.Error.Code
}

func TestLanguagePolicy(t *testing.T) {
	cases := []struct {
		name, text string
		reject     bool
	}{
		{"english", "Please reply in Chinese.", false},
		{"chinese", "请用中文回答", true},
		{"boundary", "中abcd", false},
		{"above", "中abc", true},
		{"below", "中abcde", false},
		{"ignored symbols", "中abc 1234567890，。🙂🚀!!!", true},
		{"empty", "", false},
		{"no letters", "12345，。🙂", false},
		{"traditional", "請回答", true},
		{"extension B", "𠀀abcd", false},
		{"other languages", "中文абвгдежз", false},
		{"combining marks", "中abc\u0301\u0301", true},
		{"full width letters", "中ＡＢＣＤ", false},
		{"japanese kanji", "日本語", true},
		{"email not redacted", "contact person@example.com", false},
	}
	p := &privacyFilterPlugin{cfg: defaultConfig()}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			body := requestBody(t, message(tt.text, "user.text"))
			before := bytes.Clone(body)
			resp, err := p.InterceptRequestBeforeAuth(context.Background(), pluginapi.RequestInterceptRequest{Body: body})
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, body) {
				t.Fatal("input was mutated")
			}
			code := rejectionCode(t, resp)
			wantCode := ""
			if tt.reject {
				wantCode = "chinese_ratio_exceeded"
			}
			if code != wantCode {
				t.Fatalf("code=%q, reject=%v", code, tt.reject)
			}
		})
	}
	resp, _ := p.interceptRequest(pluginapi.RequestInterceptRequest{Body: requestBody(t, message("中文", "user.text"))})
	var body struct {
		Error policyError `json:"error"`
	}
	if err := json.Unmarshal(resp.ResponseBody, &body); err != nil {
		t.Fatal(err)
	}
	wantMessage := "本轮指令的汉字占比超过 20%，请翻译成英语后重新发送。可以用英语要求模型用中文回复。\nPlease reply in Chinese, but write all questions that require my answer and their answer options in English."
	if body.Error.Message != wantMessage {
		t.Fatalf("unexpected message: %s", resp.ResponseBody)
	}
}

func TestQuotedInputExemption(t *testing.T) {
	p := &privacyFilterPlugin{cfg: defaultConfig()}
	for _, text := range []string{"> 中文题目\n中文回答", " \t\n\u3000> 中文题目\n中文回答", ">"} {
		resp, err := p.interceptRequest(pluginapi.RequestInterceptRequest{Body: requestBody(t, message(text, "user.text"))})
		if err != nil || rejectionCode(t, resp) != "" {
			t.Fatalf("quoted input rejected: %q, %v", text, err)
		}
	}
	for _, text := range []string{"中文指令\n> 中文引用", "```\n> 中文引用\n```"} {
		resp, _ := p.interceptRequest(pluginapi.RequestInterceptRequest{Body: requestBody(t, message(text, "user.text"))})
		if rejectionCode(t, resp) != "chinese_ratio_exceeded" {
			t.Fatalf("non-leading quote was exempted: %q", text)
		}
	}
	p.cfg.AllowQuotedInput = false
	resp, _ := p.interceptRequest(pluginapi.RequestInterceptRequest{Body: requestBody(t, message("> 中文", "user.text"))})
	if rejectionCode(t, resp) != "chinese_ratio_exceeded" {
		t.Fatal("disabled quote exemption still applied")
	}
	p.cfg.AllowQuotedInput = true
	resp, _ = p.interceptRequest(pluginapi.RequestInterceptRequest{Body: []byte(`{"input":[{"role":"user","content":"> 中文"}]}`)})
	if rejectionCode(t, resp) != "input_attribution_unavailable" {
		t.Fatal("quote exemption bypassed provenance")
	}
}

func TestAttributionScope(t *testing.T) {
	cases := []struct {
		name  string
		items []any
		want  string
	}{
		{"latest only", []any{message("中文历史", "user.text"), message("English", "user.text")}, "English"},
		{"same turn not aggregated", []any{message(strings.Repeat("English ", 100), "user.text"), message("中文", "user.text")}, "中文"},
		{"later automatic context", []any{message("English", "user.text"), message("中文规则", "agents_md.instructions"), message("中文环境", "environments.environment_context"), message("中文技能", "skills.selected_skill_instructions"), message("中文目标", "goal.internal_context")}, "English"},
		{"tool continuation", []any{message("English", "user.text"), map[string]any{"type": "function_call_output", "call_id": "call-1", "output": "中文工具结果"}}, "English"},
		{"assistant ignored", []any{message("English", "user.text"), map[string]any{"role": "assistant", "content": "中文回复"}}, "English"},
		{"system ignored", []any{map[string]any{"role": "system", "content": "中文系统"}, message("English", "user.text")}, "English"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got, err := latestUserText(requestBody(t, tt.items...))
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestMultipartAndImages(t *testing.T) {
	m := message("unused", "user.text")
	m["content"] = []any{
		map[string]any{"type": "input_text", "text": `<image name=[Image #1] path="/tmp/中文-image.png">`},
		map[string]any{"type": "input_image", "image_url": "data:image/png;base64,AA=="},
		map[string]any{"type": "input_text", "text": "</image>"},
		map[string]any{"type": "input_text", "text": "中"},
		map[string]any{"type": "input_text", "text": "abcd"},
	}
	m["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []string{"user.text", "user.image", "user.text", "user.text", "user.text"}}
	got, err := latestUserText(requestBody(t, m))
	if err != nil || got != "中\nabcd" {
		t.Fatalf("got %q, %v", got, err)
	}
	literal := `Please inspect <image name=[Image #1] path="/tmp/pic.png"> and </image>.`
	got, err = latestUserText(requestBody(t, message(literal, "user.text")))
	if err != nil || got != literal {
		t.Fatal("literal user text must not be stripped")
	}
	m["content"] = []any{map[string]any{"type": "input_image", "image_url": "test"}}
	m["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []string{"user.image"}}
	got, err = latestUserText(requestBody(t, message("中文历史", "user.text"), m))
	if err != nil || got != "" {
		t.Fatalf("image-only submit must not fall back to history: %q, %v", got, err)
	}
}

func TestChatCompletions(t *testing.T) {
	m := message("unused", "user.text")
	m["content"] = "中abcd"
	raw, _ := json.Marshal(map[string]any{"messages": []any{m}})
	got, err := latestUserText(raw)
	if err != nil || got != "中abcd" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestUnattributableRequests(t *testing.T) {
	valid := message("old English", "user.text")
	missing := map[string]any{"role": "user", "content": "中文"}
	mismatch := message("中文", "user.text")
	mismatch["internal_chat_message_metadata_passthrough"] = map[string]any{"content_item_kinds": []string{"user.text", "user.text"}}
	wrongType := message("中文", "user.image")
	emptyText := message("中文", "user.text")
	emptyText["content"] = []any{map[string]any{"type": "input_text"}}
	cases := [][]byte{
		nil, []byte(`null`), []byte(`[]`), []byte(`{`), []byte(`{}`),
		[]byte(`{"input":"English"}`), []byte(`{"input":[]}`), []byte(`{"input":[null]}`),
		[]byte(`{"input":[],"messages":[]}`), []byte(`{"input":[{}]}`),
		requestBody(t, valid, missing), requestBody(t, mismatch), requestBody(t, wrongType), requestBody(t, emptyText),
		requestBody(t, valid, map[string]any{"role": "usr", "content": "中文"}),
		requestBody(t, valid, map[string]any{"type": "future_user_message", "content": "中文"}),
		requestBody(t, message("context", "future.context")), requestBody(t, message("context", "agents_md.instructions")),
		[]byte(`{"previous_response_id":"response-1","input":[{"type":"function_call_output","output":"text"}]}`),
	}
	p := &privacyFilterPlugin{cfg: defaultConfig()}
	for i, body := range cases {
		resp, err := p.interceptRequest(pluginapi.RequestInterceptRequest{Body: body})
		if err != nil || rejectionCode(t, resp) != "input_attribution_unavailable" {
			t.Fatalf("case %d: %+v %v", i, resp, err)
		}
	}
}

func TestSkipAndAfterAuth(t *testing.T) {
	p := &privacyFilterPlugin{cfg: privacyFilterConfig{MaxHanPercent: 20, SkipModels: []string{" GPT-4 "}, SkipFormats: []string{" openai "}}}
	for _, req := range []pluginapi.RequestInterceptRequest{{Model: "gpt-4"}, {RequestedModel: "GPT-4"}, {SourceFormat: "OpenAI"}} {
		resp, err := p.interceptRequest(req)
		if err != nil || resp.Terminate {
			t.Fatalf("skip failed: %+v %v", resp, err)
		}
	}
	resp, err := p.InterceptRequestAfterAuth(nil, pluginapi.RequestInterceptRequest{Body: []byte(`invalid translated body`)})
	if err != nil || resp.Terminate || resp.Body != nil {
		t.Fatal("after-auth must not recheck translated content")
	}
	p.cfg.SkipModels = []string{" "}
	p.cfg.SkipFormats = []string{" "}
	if p.cfg.shouldSkip("", "", "") {
		t.Fatal("blank skip entries must not bypass policy")
	}
}

func TestConfig(t *testing.T) {
	for _, raw := range []string{"", "max_han_percent: 0", "max_han_percent: 20", "max_han_percent: 100", "enabled: true\npriority: 1\nallow_quoted_input: false", "gitleaks_toml: ''\nskip_models: [gpt-4]"} {
		cfg, err := parseConfig([]byte(raw))
		if err != nil {
			t.Fatalf("%q: %v", raw, err)
		}
		if raw == "" && (cfg.MaxHanPercent != 20 || !cfg.AllowQuotedInput) {
			t.Fatal("default is not 20")
		}
	}
	for _, raw := range []string{"max_han_percent: -1", "max_han_percent: 101", "max_han_percent: 20.5", "max_han_percent: '20'", "max_han_percent: null", "max_han_percent:", "allow_quoted_input: null", "allow_quoted_input: 'true'", "max_han_percent: twenty", "unknown: true", "---\nmax_han_percent: 20\n---\nmax_han_percent: 30"} {
		if _, err := parseConfig([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid config %q", raw)
		}
	}
	for _, pct := range []int{0, 100} {
		p := &privacyFilterPlugin{cfg: privacyFilterConfig{MaxHanPercent: percentage(pct)}}
		resp, _ := p.interceptRequest(pluginapi.RequestInterceptRequest{Body: requestBody(t, message("中文", "user.text"))})
		if resp.Terminate != (pct == 0) {
			t.Fatalf("threshold %d: terminate=%v", pct, resp.Terminate)
		}
	}
}

func TestABIRegistrationAndRejection(t *testing.T) {
	for _, schema := range []uint32{0, 1} {
		raw, _ := json.Marshal(abiLifecycleRequest{SchemaVersion: schema})
		if _, err := handlePrivacyFilterRegister(raw); err == nil {
			t.Fatalf("accepted host schema %d", schema)
		}
	}
	raw, _ := json.Marshal(abiLifecycleRequest{SchemaVersion: pluginabi.SchemaVersion})
	registration, err := handlePrivacyFilterABIMethod(context.Background(), pluginabi.MethodPluginRegister, raw)
	if err != nil {
		t.Fatal(err)
	}
	var envelope abiEnvelope
	if err := json.Unmarshal(registration, &envelope); err != nil {
		t.Fatal(err)
	}
	var result abiRegistration
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK || result.SchemaVersion != 2 || !result.Capabilities.RequestInterceptor {
		t.Fatalf("registration=%s", registration)
	}
	req, _ := json.Marshal(abiRequestInterceptRequest{RequestInterceptRequest: pluginapi.RequestInterceptRequest{Body: requestBody(t, message("中文", "user.text"))}})
	raw, err = handlePrivacyFilterABIMethod(context.Background(), pluginabi.MethodRequestInterceptBefore, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var response pluginapi.RequestInterceptResponse
	if err := json.Unmarshal(envelope.Result, &response); err != nil {
		t.Fatal(err)
	}
	if !envelope.OK || rejectionCode(t, response) != "chinese_ratio_exceeded" {
		t.Fatalf("ABI rejection=%s", raw)
	}
}

func FuzzAttribution(f *testing.F) {
	f.Add([]byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"中abcd"}],"internal_chat_message_metadata_passthrough":{"content_item_kinds":["user.text"]}}]}`))
	f.Add([]byte(`{"input":[null]}`))
	f.Fuzz(func(t *testing.T, body []byte) {
		text, err := latestUserText(body)
		if err == nil {
			c := countLetters(text)
			if c.Han < 0 || c.Han > c.Total {
				t.Fatal("invalid character counts")
			}
		}
	})
}
