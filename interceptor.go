package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

type privacyFilterPlugin struct{ cfg privacyFilterConfig }

var _ pluginapi.RequestInterceptor = (*privacyFilterPlugin)(nil)

func (p *privacyFilterPlugin) Identifier() string { return privacyFilterProvider }

func (p *privacyFilterPlugin) InterceptRequestBeforeAuth(_ context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	return p.interceptRequest(req)
}

func (p *privacyFilterPlugin) InterceptRequestAfterAuth(_ context.Context, _ pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	// Attribution must use the original client payload, before protocol translation.
	return pluginapi.RequestInterceptResponse{}, nil
}

func (p *privacyFilterPlugin) interceptRequest(req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	if p.cfg.shouldSkip(req.Model, req.RequestedModel, req.SourceFormat) {
		return pluginapi.RequestInterceptResponse{}, nil
	}
	text, err := latestUserText(req.Body)
	if err != nil {
		return rejectRequest("input_attribution_unavailable", "无法可靠识别本次手动输入，请检查 Codex 是否保留内容来源标记，并使用包含完整输入的请求。"), nil
	}
	// This is an explicit user-controlled exemption, not inferred authorship.
	if p.cfg.AllowQuotedInput && strings.HasPrefix(strings.TrimLeftFunc(text, unicode.IsSpace), ">") {
		return pluginapi.RequestInterceptResponse{}, nil
	}
	counts := countLetters(text)
	if counts.Han*100 > counts.Total*int64(p.cfg.MaxHanPercent) {
		message := fmt.Sprintf("本轮指令的汉字占比超过 %d%%，请翻译成英语后重新发送，并明确要求模型必须用英语回复。\n提示词示例：\nReply only in English, including all questions that require my answer and their answer options.", p.cfg.MaxHanPercent)
		return rejectRequest("chinese_ratio_exceeded", message), nil
	}
	return pluginapi.RequestInterceptResponse{}, nil
}

type policyError struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func rejectRequest(code, message string) pluginapi.RequestInterceptResponse {
	// Marshaling this string-only structure cannot fail.
	body, _ := json.Marshal(struct {
		Error policyError `json:"error"`
	}{
		Error: policyError{Type: "invalid_request_error", Code: code, Message: message},
	})
	return pluginapi.RequestInterceptResponse{
		Terminate:       true,
		StatusCode:      http.StatusBadRequest,
		ResponseHeaders: http.Header{"Content-Type": {"application/json; charset=utf-8"}},
		ResponseBody:    body,
	}
}
