# CPA 中文输入检查插件

[English](README.md) | 简体中文

本分支将原来的隐私脱敏插件替换为 [CLIProxyAPI](https://github.com/M0rtzz/CLIProxyAPI)
语言检查插件：用户发送请求后，转发给模型前，检查最新一次手动指令；汉字占比超过 20% 时拒绝请求。

插件标识和共享库名称仍为 `privacyfilter`。**本分支不再检测密钥或脱敏隐私信息。**
放行时不改写、不翻译用户输入。

## 检查规则与显式豁免

计算方式为 `Unicode Han 字符数 ÷ 全部 Unicode 文字字符数`，按字符而非字节或 token 计数。
空格、数字、标点、emoji 和组合附加符号不计数。严格超过 20% 才拦截，恰好 20% 放行；没有文字字符时放行。
例如 `中abcd` 放行，`中abc` 拦截。这是汉字比例检查，不是英语识别；日语中的汉字也会计入，其他外语不会自动被拒绝。

默认允许**以 `>` 开头的输入整条豁免**，判断前忽略开头的空白字符。适用于引用 Codex 生成的中文问题后作答：

```text
> 是否先使用内置 GITHUB_TOKEN？
先使用内置 GITHUB_TOKEN，权限不足时报告失败。
```

豁免范围包含引用和后面的回答，转发时保留 `>`。这是用户主动选择的绕过方式，不能证明引用由 Codex 生成。
豁免仍要求输入具有可靠的来源标记。设置 `allow_quoted_input: false` 可关闭此功能。

## 如何确定本轮手动输入

插件在 CPA 的 before-auth 阶段读取原始请求，检查发生在协议转换前；after-auth 阶段不重复检查。

- 支持 Responses 的 `input` 数组和 Chat Completions 的 `messages` 数组。
- 从后往前寻找最新一次手动用户提交，不合并同一 `turn_id` 下的其他提交。
- 要求 `role: "user"`，并将 `content` 与
  `internal_chat_message_metadata_passthrough.content_item_kinds` 按下标对应。
- 只统计标为 `user.text` 的 `input_text` / `text` 项。Chat Completions 的字符串 `content`
  仅在附有一个对应来源标记时支持。
- 排除已知自动来源：`agents_md.instructions`、`environments.environment_context`、
  `skills.selected_skill_instructions` 和 `goal.internal_context`。
- 忽略 `user.image` 图片项，以及经过验证的 Codex 本地图片“文字／图片／文字”包装。
  用户自行输入的标签、代码和引用正常参与计数。

请求结构示例：

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

来源标记缺失、数组错位、未知来源、请求结构有歧义或 Responses 使用字符串 `input` 时，返回
`input_attribution_unavailable`，不会悄悄回退检查旧指令。携带完整历史的工具续传可以重复检查同一条指令；
不支持无法定位手动输入的增量请求。运行时不读取 JSONL 会话日志。

## 拦截提示

在调用上游、开始 SSE 输出前，CPA 返回 HTTP `400`，响应头为
`Content-Type: application/json; charset=utf-8`：

```json
{
  "error": {
    "type": "invalid_request_error",
    "code": "chinese_ratio_exceeded",
    "message": "本轮指令的汉字占比超过 20%，请翻译成英语后重新发送。可以用英语要求模型用中文回复。\nPlease reply in Chinese, but write all questions that require my answer and their answer options in English."
  }
}
```

提示中的百分比随配置变化。来源识别失败使用独立的中文配置提示，不误报为中文超标。
响应由客户端作为 API 错误展示，不伪造模型回答。插件日志不记录用户输入原文。

## 构建与安装

需要 Go 1.26+、可用的 C 编译器、启用 CGO 和 `make`，以及支持原生插件 schema 2 与请求终止的 CPA。
插件固定依赖 CPA SDK `v7.3.8`；旧宿主不支持终止能力时，插件协商失败，不会静默失效。

```bash
git clone git@github.com:M0rtzz/cpa-plugin-privacyfilter.git
cd cpa-plugin-privacyfilter
git switch feat/chinese-input-guard
make build BUILD_DIR=dist
```

Linux 输出为 `dist/privacyfilter.so`，macOS 为 `.dylib`，Windows 为 `.dll`。
跨平台编译还需要对应的 C 交叉编译器。无需额外 Gitleaks 规则或附带文件。

将共享库直接放在 CPA 的插件目录：

```text
plugins/
└── privacyfilter.so
```

也可使用平台目录 `plugins/linux/amd64/privacyfilter.so`。
不要额外添加 `plugins/privacyfilter/` 中间目录。

把 [examples/cpa-language-guard.yaml](examples/cpa-language-guard.yaml) 合并进现有 CPA 配置，保留原有认证和模型路由：

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

| 配置项 | 默认值 | 说明 |
|---|---|---|
| `max_han_percent` | `20` | 0–100 的整数，严格超过才拒绝。 |
| `allow_quoted_input` | `true` | 忽略开头空白后，以 `>` 开头的可靠手动输入整条豁免。 |
| `skip_models` | `[]` | 匹配请求模型名或解析后的模型名时豁免。 |
| `skip_formats` | `[]` | 匹配 CPA 来源格式名称时豁免。 |

模型和格式豁免会跳过来源识别及计数，匹配不区分大小写。`enabled` 和 `priority` 由 CPA 处理。
非法或未知配置项导致初始化失败。旧 `gitleaks_toml` 只用于输出迁移警告，不再产生过滤效果。
`max_han_percent` 与 `allow_quoted_input` 不接受 YAML 空值；不支持合并键（`<<`）。

## Codex 配置

JSONL 日志含有来源标记，并不代表网络请求也保留它们。Codex `0.154.0` 会在提供商名称不是 `OpenAI` 时移除内部元数据。
独立的 [Codex 配置示例](examples/cpa-language-guard.config.toml) 开启
`features.content_item_kinds`，使用提供商名称 `OpenAI`，同时保留本地 CPA 地址和 API Key 认证；
关闭 WebSocket 后使用 HTTP Responses 与 SSE。

在 shell 中将 `CPA_LANGUAGE_GUARD_API_KEY` 设为已有的 CPA 客户端 API Key，并把
`YOUR_CPA_MODEL_ALIAS` 换成 CPA 中配置的模型别名。`name = "OpenAI"` 会影响客户端行为，
但不会改变明确配置的 `base_url`。升级 Codex 后应重新验证来源标记。

已用真实 Codex `0.154.0` 完成本地模拟：提供商名称 `OpenAI` 能保留来源标记，英文输入正常放行，
中文输入按比例拒绝；名称改为 `Custom` 后，英文输入也因缺少标记而返回来源识别错误。
上述拒绝场景均未调用模拟模型。

独立测试通过后，可将示例保存到 `$CODEX_HOME/cpa-language-guard.config.toml`，默认即
`~/.codex/cpa-language-guard.config.toml`，通过 `codex --profile cpa-language-guard` 选择。
Codex 0.154.0 将这一独立文件叠加到用户基础配置上，不使用 `[profiles]` 表，也不覆盖基础配置。
测试通过前，不替换日常配置。

## 测试与本地模拟

```bash
go test ./...
go vet ./...
make build BUILD_DIR=dist
(cd ../CLIProxyAPI && go build -o dist/cli-proxy-api-language-guard ./cmd/server)
python3 scripts/simulate-cpa.py --cpa ../CLIProxyAPI/dist/cli-proxy-api-language-guard --plugin dist/privacyfilter.so
```

加上 `--codex /path/to/codex` 可测试真实 Codex 客户端。测试使用 `--ignore-user-config`、临时会话、
合成指令和本地模拟上游，不使用真实模型凭证，也不修改日常 Codex 配置。
诊断信息和结果保存在 `dist/simulation/`。

模拟会启动编译后的 CPA，加载真正的原生插件，覆盖 Responses 与 Chat Completions 的普通、流式请求。
被拒绝的请求必须产生零次上游调用。场景包括 20% 边界、来源隔离、标记缺失、图片包装、工具续传、
显式豁免和完整提示文案。

## 每日同步上游

两个 fork 的 `main` 只保存上游代码和同步工作流，定制功能保留在独立分支。
同步工作流每天 **UTC 12:26**（北京时间 20:26）直接合并上游 `main`，也可通过 `workflow_dispatch`
手动触发。GitHub 定时运行可能排队延迟。

| Fork | 上游 |
|---|---|
| `M0rtzz/cpa-plugin-privacyfilter` | `rheodev/cpa-plugin-privacyfilter` |
| `M0rtzz/CLIProxyAPI` | `router-for-me/CLIProxyAPI` |

使用内置 `GITHUB_TOKEN` 和 `contents: write`，无需配置 `UPSTREAM_SYNC_TOKEN`。
权限不足、上游工作流文件的写入限制、分支保护或合并冲突均使任务明确失败；不强推，不自动解决冲突，
不创建 PR，不运行构建或定制功能测试，不发布、不部署，也不自动更新功能分支。

需在两个 fork 中启用 Actions，并让工作流位于默认分支 `main`，定时触发才会生效。
仓库策略须允许令牌写入和直接更新；同步工作流不会自动放宽分支保护。
