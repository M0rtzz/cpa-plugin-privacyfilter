# CPA Chinese Input Guard

English | [简体中文](README.zh-CN.md)

This branch replaces the original privacy filter with a language check for
[CLIProxyAPI](https://github.com/M0rtzz/CLIProxyAPI). After a user submits a
request, the native plugin rejects it before a model call if Han characters
exceed 20% of the latest manually entered instruction.

The plugin ID and library name remain `privacyfilter`. **This branch no longer
detects secrets or redacts private information.** Allowed input is neither
rewritten nor translated.

For installation, local key generation, CPA and Codex configuration, and testing,
see the [complete local testing guide (Chinese)](docs/local-testing.zh-CN.md).

For production deployment, systemd operation, acceptance checks, and rollback,
see the [deployment guide (Chinese)](docs/deployment.zh-CN.md).

## Policy and explicit exemption

The ratio is `Unicode Han characters / all Unicode letters`, counted by code
point. Spaces, numbers, punctuation, emoji, and combining marks are excluded.
Exactly 20% passes; strictly more than 20% is rejected. Text without letters
passes. For example, `中abcd` passes and `中abc` is rejected. This measures Han
characters rather than identifying English: Japanese kanji count as Han, and
other languages are not automatically rejected.

By default, **input whose first non-whitespace character is `>` is explicitly
exempt**. This supports quoting a Chinese Codex question and answering it:

```text
> 是否先使用内置 GITHUB_TOKEN？
先使用内置 GITHUB_TOKEN，权限不足时报告失败。
```

The exemption covers the entire latest instruction, including the answer, and
does not remove the prefix before forwarding. This is a deliberate user
override, not proof that Codex generated the quoted text. Source metadata is
still required. Set `allow_quoted_input: false` to disable it.

## Identifying the latest instruction

Checks run in CPA's before-auth interception hook on the original request,
before protocol translation. The after-auth hook does not repeat the check.

- Accept Responses `input` arrays or Chat Completions `messages` arrays.
- Traverse newest to oldest and select the latest manual user submission;
  do not aggregate every message sharing its `turn_id`.
- Require `role: "user"` and align `content` items with
  `internal_chat_message_metadata_passthrough.content_item_kinds` by index.
- Count `input_text` / `text` items marked `user.text`. Chat Completions string
  `content` is supported only with one corresponding source marker.
- Skip known automatic kinds: `agents_md.instructions`,
  `environments.environment_context`, `skills.selected_skill_instructions`, and
  `goal.internal_context`.
- Ignore `user.image` items and the verified Codex text/image/text wrapper around
  local images. Ordinary user-written tags, code, and quotes still count.

Example request:

```json
{
  "model": "YOUR_CPA_MODEL_ALIAS",
  "input": [{
    "role": "user",
    "content": [{"type": "input_text", "text": "Please reply in Chinese."}],
    "internal_chat_message_metadata_passthrough": {
      "content_item_kinds": ["user.text"]
    }
  }]
}
```

Missing or misaligned metadata, unknown source kinds, ambiguous request bodies,
and string-only Responses input return `input_attribution_unavailable`, rather
than silently checking an older instruction. Tool continuations with full
history can recheck the same instruction; incremental requests without an
attributable user submission are unsupported. Session JSONL files are never
read at runtime.

## Rejection response

CPA returns HTTP `400` with `Content-Type: application/json; charset=utf-8`
before an upstream request or SSE output begins:

```json
{
  "error": {
    "type": "invalid_request_error",
    "code": "chinese_ratio_exceeded",
    "message": "本轮指令因汉字占比超过 20% 被拦截。为避免模型降智，请自行将指令翻译成英语后重新发送，并明确要求模型必须用英语回复。\n提示词示例：\nWrite all conversational replies in English, including explanations, questions that require my answer, and their answer options.\n\nWhen creating or editing files, Chinese may be used where appropriate, including documentation, code comments, text displayed in frontend pages, etc. Follow the language requirements of the task and the repository for those files."
  }
}
```

The percentage follows the configured threshold. Attribution errors have a
separate Chinese message about preserving source metadata. Responses are API
errors, not fabricated model answers. The plugin does not log user text.

## Build and installation

Requirements: Go 1.26+, a C compiler with CGO enabled, `make`, and a CPA host
supporting native plugin schema version 2 and request termination. The plugin
is pinned to CPA SDK `v7.3.8`; unsupported older hosts fail plugin negotiation.

```bash
git clone git@github.com:M0rtzz/cpa-plugin-privacyfilter.git
cd cpa-plugin-privacyfilter
git switch feat/chinese-input-guard
make build BUILD_DIR=dist
```

Output is `dist/privacyfilter.so` on Linux, `.dylib` on macOS, or `.dll` on
Windows. Cross-compilation also requires a matching C cross-compiler. No Gitleaks
rules or sidecar files are needed.

Place the library directly in CPA's configured plugin directory:

```text
plugins/
└── privacyfilter.so
```

The platform layout `plugins/linux/amd64/privacyfilter.so` is also supported.
Do not add an intermediate `plugins/privacyfilter/` directory.

Merge [examples/cpa-language-guard.yaml](examples/cpa-language-guard.yaml) into
your CPA configuration, retaining its existing authentication and model routing:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    privacyfilter:
      enabled: true
      max_han_percent: 20
      allow_quoted_input: true
      skip_models: []
      skip_formats: []
```

| Field | Default | Meaning |
|---|---|---|
| `max_han_percent` | `20` | Integer 0–100; only a strictly higher ratio is blocked. |
| `allow_quoted_input` | `true` | Exempt the whole attributable instruction when its first non-whitespace character is `>`. |
| `skip_models` | `[]` | Exempt matching requested or resolved model names. |
| `skip_formats` | `[]` | Exempt matching CPA source-format names. |

Explicit model/format exemptions skip attribution and counting; matches are
case-insensitive. `enabled` and `priority` are CPA-owned fields. Invalid or
unknown configuration fields fail initialization. Legacy `gitleaks_toml` only
emits a migration warning and has no filtering effect.
Write these settings directly; YAML null values and merge keys (`<<`) are not
supported.

## Codex configuration

JSONL source markers do not establish that they reach CPA. Codex `0.154.0`
removes internal metadata for provider names other than `OpenAI`. The independent
[Codex example](examples/cpa-language-guard.config.toml) enables
`features.content_item_kinds`, uses the `OpenAI` provider name while retaining
the local CPA URL and API-key authentication, and disables WebSocket transport
so requests use HTTP Responses and SSE.

Set `CPA_LANGUAGE_GUARD_API_KEY` in your shell to an existing CPA client API key
and replace `YOUR_CPA_MODEL_ALIAS` with a configured CPA model alias. Provider
`name = "OpenAI"` affects client behavior but does not change `base_url`.
Revalidate provenance after upgrading Codex.

The local simulation has verified this behavior with Codex `0.154.0`: the
`OpenAI` name preserves attribution for an allowed English instruction and a
blocked Chinese instruction; a `Custom` provider name produces an attribution
error even for English input. All rejected cases make zero mock-model calls.

After isolated testing, the example can be saved as
`$CODEX_HOME/cpa-language-guard.config.toml` (normally
`~/.codex/cpa-language-guard.config.toml`) and selected with
`codex --profile cpa-language-guard`. Codex 0.154.0 layers this separate file over
base user configuration; it is not a `[profiles]` table and does not overwrite
the base file. Do not replace your usual configuration before testing.

## Tests and local simulation

```bash
go test ./...
go vet ./...
make build BUILD_DIR=dist
(cd ../CLIProxyAPI && go build -o dist/cli-proxy-api-language-guard ./cmd/server)
python3 scripts/simulate-cpa.py --cpa ../CLIProxyAPI/dist/cli-proxy-api-language-guard --plugin dist/privacyfilter.so
```

Add `--codex /path/to/codex` to exercise the real client. It uses
`--ignore-user-config`, an ephemeral session, synthetic prompts, a local mock
provider, and no real model credentials. It does not modify normal Codex
configuration. Diagnostics and results go to `dist/simulation/`.

The simulation starts the compiled CPA and loads the actual native plugin. It
covers Responses and Chat Completions, streaming and non-streaming, the 20%
boundary, source isolation, missing metadata, image wrappers, tool continuation,
explicit exemptions, and the exact rejection message. Rejected requests must
make zero upstream calls.

## Daily upstream synchronization

Each fork's `main` contains upstream code and its synchronization workflow;
custom functionality stays on this separate feature branch. The workflow
merges upstream `main` directly at **12:26 UTC daily** (20:26 Asia/Shanghai),
with a manual `workflow_dispatch` trigger too. Scheduled runs may be delayed.

| Fork | Upstream |
|---|---|
| `M0rtzz/cpa-plugin-privacyfilter` | `rheodev/cpa-plugin-privacyfilter` |
| `M0rtzz/CLIProxyAPI` | `router-for-me/CLIProxyAPI` |

The workflow uses built-in `GITHUB_TOKEN` with `contents: write`; no
`UPSTREAM_SYNC_TOKEN` secret is needed. Permission failures, workflow-file
restrictions, branch protection, and conflicts visibly fail the job. It does
not force-push, resolve conflicts, create PRs, run builds or custom-feature
tests, publish releases, deploy, or update feature branches.

Enable Actions in each fork and place the workflow on the default `main`
branch for schedules to run. Repository policies must allow token writes and
direct updates; the workflow does not weaken branch protection.
