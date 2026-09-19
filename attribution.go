package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var errAttribution = errors.New("current user input attribution is unavailable")

type attributedMessage struct {
	Type     string          `json:"type"`
	Role     string          `json:"role"`
	Content  json.RawMessage `json:"content"`
	Metadata *struct {
		Kinds []string `json:"content_item_kinds"`
	} `json:"internal_chat_message_metadata_passthrough"`
}

type textPart struct {
	Type string  `json:"type"`
	Text *string `json:"text"`
}

// These kinds have been observed in Codex 0.154.0. Unknown kinds are not
// silently discarded: an upgraded client must be verified before accepting them.
func isAutomaticKind(kind string) bool {
	switch kind {
	case "agents_md.instructions", "environments.environment_context",
		"skills.selected_skill_instructions", "goal.internal_context":
		return true
	default:
		return false
	}
}

func latestUserText(body []byte) (string, error) {
	if !utf8.Valid(body) {
		return "", errAttribution
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(body, &payload); err != nil || payload == nil {
		return "", errAttribution
	}
	input, hasInput := payload["input"]
	messages, hasMessages := payload["messages"]
	if hasInput == hasMessages {
		return "", errAttribution
	}
	if hasMessages {
		input = messages
	}
	// String input has no per-item provenance, so it cannot satisfy this policy.
	var items []json.RawMessage
	if err := json.Unmarshal(input, &items); err != nil || len(items) == 0 {
		return "", errAttribution
	}
	for i := len(items) - 1; i >= 0; i-- {
		var item attributedMessage
		if err := json.Unmarshal(items[i], &item); err != nil || bytes.Equal(bytes.TrimSpace(items[i]), []byte("null")) {
			return "", errAttribution
		}
		if item.Role != "user" {
			if !isNonUserItem(item) {
				return "", errAttribution
			}
			continue
		}
		if item.Type != "" && item.Type != "message" {
			return "", errAttribution
		}
		text, manual, err := extractUserText(item)
		if err != nil {
			return "", err
		}
		if manual {
			return text, nil
		}
	}
	return "", errAttribution
}

func isNonUserItem(item attributedMessage) bool {
	if item.Type == "" || item.Type == "message" {
		switch item.Role {
		case "assistant", "system", "developer", "tool":
			return true
		default:
			return false
		}
	}
	if item.Role != "" {
		return false
	}
	switch item.Type {
	case "function_call", "function_call_output", "custom_tool_call", "custom_tool_call_output",
		"reasoning", "local_shell_call", "web_search_call", "tool_search_call", "tool_search_output",
		"image_generation_call", "compaction", "compaction_summary":
		return true
	default:
		return false
	}
}

func extractUserText(item attributedMessage) (string, bool, error) {
	if item.Metadata == nil || len(item.Metadata.Kinds) == 0 {
		return "", false, errAttribution
	}
	var parts []textPart
	if err := json.Unmarshal(item.Content, &parts); err != nil {
		// Chat Completions may carry a single string with one provenance entry.
		var text string
		if err := json.Unmarshal(item.Content, &text); err != nil {
			return "", false, errAttribution
		}
		parts = []textPart{{Type: "text", Text: &text}}
	}
	kinds := item.Metadata.Kinds
	if len(parts) != len(kinds) {
		return "", false, errAttribution
	}
	manual := false
	for i, part := range parts {
		switch kinds[i] {
		case "user.text":
			if (part.Type != "input_text" && part.Type != "text") || part.Text == nil {
				return "", false, errAttribution
			}
			manual = true
		case "user.image":
			if part.Type != "input_image" && part.Type != "image_url" {
				return "", false, errAttribution
			}
			manual = true
		default:
			if !isAutomaticKind(kinds[i]) || (part.Type != "input_text" && part.Type != "text") || part.Text == nil {
				return "", false, errAttribution
			}
		}
	}
	var texts []string
	for i := 0; i < len(parts); i++ {
		// Only discard the complete text/image/text wrapper emitted by Codex.
		// Tags in ordinary prose and image references typed by the user stay intact.
		if i+2 < len(parts) && kinds[i] == "user.text" && kinds[i+1] == "user.image" && kinds[i+2] == "user.text" &&
			parts[i].Text != nil && localImageOpen.MatchString(*parts[i].Text) &&
			parts[i+2].Text != nil && *parts[i+2].Text == "</image>" {
			i += 2
			continue
		}
		if kinds[i] == "user.text" {
			texts = append(texts, *parts[i].Text)
		}
	}
	return strings.Join(texts, "\n"), manual, nil
}

var localImageOpen = regexp.MustCompile(`^<image name=\[Image #[1-9][0-9]*\] path="[^\r\n]*">$`)

type letterCounts struct{ Han, Total int64 }

func countLetters(text string) letterCounts {
	var counts letterCounts
	for _, r := range text {
		if unicode.Is(unicode.Han, r) {
			counts.Han++
			counts.Total++
		} else if unicode.IsLetter(r) {
			counts.Total++
		}
	}
	return counts
}
