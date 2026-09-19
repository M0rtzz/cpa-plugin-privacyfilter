# CPA 中文输入检查插件正式部署指南

本文部署一套由 systemd 长期托管的 CLIProxyAPI（CPA）及中文输入检查插件，包含安装、凭证、客户端接入、上线验收、升级和回滚。
CPA 使用官方安装器安装到 `~/Programs/cliproxyapi`；仅插件需要编译，不需要下载 CPA 源码。
本地开发和接口模拟的详细说明见[测试指南](local-testing.zh-CN.md)。

## 1. 部署约定与适用范围

| 项目 | 本文约定 |
|---|---|
| 系统 | Ubuntu 24.04 / 兼容的 glibc Linux，普通用户部署 |
| CPA | 官方支持动态库插件的 `default` 发行包，已验证基线为 7.3.8 |
| 插件 | 本仓库 `feat/chinese-input-guard` 分支，原生插件 schema 2 |
| 构建环境 | Go 1.26+、C 编译器、make |
| Codex | 已验证 0.154.0；更新客户端后需重新验证来源标记 |
| 服务 | 用户级 `cliproxyapi.service`，工作目录为安装目录 |
| CPA 监听 | `127.0.0.1:18316` |
| Codex 接口 | `http://127.0.0.1:18316/v1`，HTTP Responses / SSE |
| 运行配置 | `~/Programs/cliproxyapi/config.yaml` |
| 默认插件路径 | `~/Programs/cliproxyapi/plugins/privacyfilter.so` |
| 认证目录 | 以实际 `auth-dir` 为准，保留已有目录 |

请求路径为：Codex → CPA → 插件检查 → 真实模型上游。
插件检查最新一次可靠标记的手动输入，按 Unicode Han 字符数除以全部 Unicode 文字字符数计算，严格超过 20% 才拒绝。
本分支已替换原隐私脱敏功能，放行时不修改文本。

部署前确认以下行为符合使用方式：

- 输入来源标记缺失或有歧义时返回 `input_attribution_unavailable`，普通 OpenAI 兼容客户端不一定会发送这些标记。
- 默认允许忽略开头空白后以 `>` 开头的输入整条豁免。这是显式绕过规则，不证明文字来自 Codex。
- 日语汉字也计入比例，其他非英语文字不会因此自动被拒绝。
- 本插件依赖客户端来源元数据，不用于阻止恶意客户端伪造输入来源。
- HTTP 400 拦截提示包含英文回复要求示例；插件不向放行请求注入提示词，也不检查模型输出语言。

## 2. 准备环境与路径

所有部署命令均在 **CPA 所在机器、同一个普通用户的 Bash 终端**执行；不要使用 sudo 运行安装器或 CPA。
若要从当前前台测试进程迁移，先完成准备和构建，再在第 5 节停服切换。

~~~bash
bash
~~~

~~~bash
export GUARD_CPA_DIR="$HOME/Programs/cliproxyapi"
export GUARD_RUN_DIR="$GUARD_CPA_DIR"
export GUARD_PLUGIN_DIR="$HOME/Workspaces/Misc/cpa-plugin-privacyfilter"
export GUARD_PORT=18316
export GUARD_API_BASE="http://127.0.0.1:$GUARD_PORT/v1"
~~~

新终端需要重新执行这些 export。改变端口时，应同步重新生成客户端 profile；配置文件不会自动跟随 Shell 变量变化。

~~~bash
sudo apt-get update
sudo apt-get install -y \
  git openssh-client curl ca-certificates build-essential make \
  python3 python3-yaml iproute2

go version
gcc --version
/usr/bin/python3 --version
~~~

Go 需达到 1.26，配置脚本使用系统 `/usr/bin/python3` 和 PyYAML。
Go 或 Codex 尚未安装时，按[依赖安装步骤](local-testing.zh-CN.md#2-安装依赖)准备。
不要用 Ubuntu 默认的旧 Go 版本直接构建，也不要把 Conda 的 Python 与系统 PyYAML 混用。
Codex 可以只安装在客户端机器；包含真实 Codex 的模拟测试则需要部署机器也能执行 `codex`。

## 3. 使用官方安装器安装 CPA

首次安装按下面完整代码块操作。安装器会创建同名用户服务，首次安装不自动启动；升级时可能停止其他匹配的 CPA 进程。
已有同名服务或多个实例时，先核对目标。**已有安装且本轮只安装插件时跳过安装器**；升级 CPA 按第 11 节先备份、停服。

~~~bash
(
set -e
curl -fsSL \
  https://raw.githubusercontent.com/router-for-me/cliproxyapi-installer/refs/heads/master/cliproxyapi-installer \
  -o /tmp/cliproxyapi-installer

sed -i 's|INSTALL_DIR="$HOME/cliproxyapi"|INSTALL_DIR="$HOME/Programs/cliproxyapi"|' \
  /tmp/cliproxyapi-installer

bash /tmp/cliproxyapi-installer

test -x "$GUARD_CPA_DIR/cli-proxy-api"
test -f "$GUARD_CPA_DIR/config.yaml"
test "$(cat "$GUARD_CPA_DIR/asset-variant.txt")" = default
cat "$GUARD_CPA_DIR/version.txt"
)
~~~

安装目录必须使用 `$HOME/Programs/cliproxyapi`，双引号中的 `~` 不会展开。
`no-plugin` 包不能加载本插件。安装器下载的版本可能更新，必须完成后续模拟与实际验收。

如果只打印 `Selected asset variant` 就退出，不要当作安装成功。
已出现过 GitHub 匿名 API 返回 403 限流的情况；可按[安装器限流处理](local-testing.zh-CN.md#32-仅在-github-匿名-api-限流时修复并重跑)
使用已登录的 `gh api` 获取发行信息。无需把 GitHub 登录令牌写入脚本。

## 4. 构建插件并验证候选版本

首次获取插件仓库：

~~~bash
if [ ! -d "$GUARD_PLUGIN_DIR/.git" ]; then
  mkdir -p "$(dirname "$GUARD_PLUGIN_DIR")"
  git clone git@github.com:M0rtzz/cpa-plugin-privacyfilter.git "$GUARD_PLUGIN_DIR"
fi
git -C "$GUARD_PLUGIN_DIR" status --short --branch
~~~

没有 GitHub SSH Key 时，将地址换为 `https://github.com/M0rtzz/cpa-plugin-privacyfilter.git`。
工作区有修改时先保留自己的修改；确认分支和工作区后执行：

~~~bash
(
set -e
if git -C "$GUARD_PLUGIN_DIR" show-ref --verify --quiet refs/heads/feat/chinese-input-guard; then
  git -C "$GUARD_PLUGIN_DIR" switch feat/chinese-input-guard
else
  git -C "$GUARD_PLUGIN_DIR" fetch origin feat/chinese-input-guard
  git -C "$GUARD_PLUGIN_DIR" switch --track origin/feat/chinese-input-guard
fi
cd "$GUARD_PLUGIN_DIR"
go mod download
go test ./...
go vet ./...
make build BUILD_DIR=dist
{
  git rev-parse HEAD
  sha256sum dist/privacyfilter.so
} > dist/deployment-build.txt
)
~~~

正式部署使用实际验证过的提交和产物，`dist/deployment-build.txt` 用于留档。
不要从 `main` 构建中文检查功能：该分支用于跟随原上游。

使用将要部署的 CPA 二进制运行模拟：

~~~bash
cd "$GUARD_PLUGIN_DIR"
python3 scripts/simulate-cpa.py \
  --cpa "$GUARD_CPA_DIR/cli-proxy-api" \
  --plugin "$GUARD_PLUGIN_DIR/dist/privacyfilter.so" \
  --codex "$(command -v codex)"
~~~

成功输出 `PASS: 31 scenarios`，其中所有预期拒绝项均须 `upstream_calls: 0`。
没有安装 Codex 时去掉 `--codex` 整行，执行 27 项接口模拟，并在客户端机器补做第 10 节的真实 Codex 验收。
结果保存在 `dist/simulation/`；模拟使用自己的临时配置和本地模拟上游，不调用真实模型、不修改正式配置。

## 5. 停止旧实例并建立部署快照

安装器已创建用户服务时，停止服务：

~~~bash
systemctl --user stop cliproxyapi.service
ss -ltnp "( sport = :$GUARD_PORT )"
~~~

若端口仍有 CPA 监听，它可能是前台进程；在原终端按 Ctrl+C，确认已停止后再继续。
不要同时运行前台 CPA 和同端口的 systemd 服务。

**首次部署、更新插件、更新 CPA 和大幅修改配置前，都执行下面的快照步骤。**
备份目录在运行目录之外；同时备份实际插件目录和认证目录，且不输出密钥：

~~~bash
mkdir -p "$HOME/.local/state/cpa-language-guard/backups"
chmod 700 "$HOME/.local/state/cpa-language-guard/backups"
export GUARD_BACKUP
GUARD_BACKUP="$(mktemp -d "$HOME/.local/state/cpa-language-guard/backups/snapshot.XXXXXXXX")"

/usr/bin/python3 - <<'PY'
import json
import os
import shutil
from pathlib import Path
import yaml

run = Path(os.environ["GUARD_RUN_DIR"])
backup = Path(os.environ["GUARD_BACKUP"])
cfg = yaml.safe_load((run / "config.yaml").read_text())
def resolve(value):
    path = Path(value).expanduser()
    return (path if path.is_absolute() else run / path).resolve()

plugins = cfg.get("plugins") or {}
plugin_dir = resolve(plugins.get("dir") or "plugins")
auth_dir = resolve(cfg.get("auth-dir") or "~/.cli-proxy-api")
if run.resolve().is_relative_to(plugin_dir) or auth_dir.is_relative_to(plugin_dir):
    raise SystemExit("插件目录不能包含安装根目录或认证目录，请先分离目录")
if backup.resolve().is_relative_to(plugin_dir) or backup.resolve().is_relative_to(auth_dir):
    raise SystemExit("备份目录不能位于待备份的插件或认证目录内")
for name in ("cli-proxy-api", "config.yaml", "version.txt", "asset-variant.txt", "secrets.env", "deployment-build.txt"):
    path = run / name
    if path.exists():
        shutil.copy2(path, backup / name)
        (backup / name).chmod(0o600)
if plugin_dir.is_dir():
    shutil.copytree(plugin_dir, backup / "plugins")
if auth_dir.is_dir():
    shutil.copytree(auth_dir, backup / "auth")
(backup / "paths.json").write_text(json.dumps({
    "run_dir": str(run), "plugin_dir": str(plugin_dir), "auth_dir": str(auth_dir),
}, indent=2))
unit = Path.home() / ".config/systemd/user/cliproxyapi.service"
if unit.exists():
    shutil.copy2(unit, backup / "cliproxyapi.service")
dropins = unit.with_name("cliproxyapi.service.d")
if dropins.is_dir():
    shutil.copytree(dropins, backup / "cliproxyapi.service.d")
print("部署快照：" + str(backup))
PY
~~~

记录输出路径，回滚时需要它。备份包含凭证，留在受限目录；不要把它提交到 Git。
认证备份用于灾难恢复，常规代码回滚保留当前 OAuth 认证文件，避免回退已刷新的令牌。

若已有通过插件商店安装的同 ID `privacyfilter`，**完成上面的快照后**，临时启动原服务，
在管理页面卸载旧版，然后再次停服：

~~~bash
systemctl --user start cliproxyapi.service
~~~

管理页面卸载完成后：

~~~bash
systemctl --user stop cliproxyapi.service
ss -ltnp "( sport = :$GUARD_PORT )"
~~~

此段仅供迁移商店版时执行。先备份再卸载，才能回滚到原插件版本；
旧商店元数据和版本目录可能使 CPA 优先加载旧文件，第 6 节会拒绝直接覆盖这种配置。

## 6. 合并正式配置、准备密钥并安装插件

### 6.1 三种凭证

| 凭证 | 来源与用途 |
|---|---|
| 客户端 Key | 复用安装器生成的有效值；没有时随机生成，用于 Codex → CPA |
| 管理密钥 | 保留已有密码；未设置时生成独立随机值，用于管理页面 |
| 上游凭证 | OAuth 登录取得或由上游服务商签发，用于 CPA → 真实模型 |

本地随机 Key 不能代替真实模型凭证。`secrets.env` 保存客户端 Key 和可取得的原始管理密码，权限为 600。
已有 bcrypt 管理哈希时保持原值，不会重置；没有原始密码时管理变量可为空，客户端仍可正常使用。

### 6.2 出站代理

这里的 `proxy-url` 指向 **CPA 所在机器可用的出站代理**，与 Codex 的 `base_url` 无关。
不要照抄其他机器的代理端口。systemd 不会自动加载交互式 Shell 中的代理配置。

下面三种选择只执行一种。保留现有值：

~~~bash
unset GUARD_UPSTREAM_PROXY_URL
~~~

明确清除 YAML 中的代理配置：

~~~bash
export GUARD_UPSTREAM_PROXY_URL=''
~~~

使用本机确实在监听的 HTTP 代理（示例端口 7890，需改为实际值）：

~~~bash
ss -ltnp '( sport = :7890 )'
export GUARD_UPSTREAM_PROXY_URL='http://127.0.0.1:7890'
~~~

空配置不保证覆盖所有提供商/凭证级代理或进程环境中的代理；已有这些设置时应一起核对。
本机回环代理若依赖桌面应用，退出桌面后可能停止，不能据此保证无人登录时的服务可用性。

### 6.3 写入配置

下面会备份原 YAML 后原子替换，保留真实 Key、上游、路由、认证目录和其他插件。
生产设置使用本机监听、关闭调试和全量请求日志、启用总量 200 MB 的文件日志、最多保留 10 个错误日志。
HTTP 400 等错误日志仍可能包含请求和响应正文，中文拦截内容也可能被宿主记录；不要将这些日志直接公开。
如需外部访问，按第 10 节设置。

~~~bash
/usr/bin/python3 - <<'PY'
import os
import secrets
import shlex
import tempfile
from pathlib import Path
import yaml

run_dir = os.environ.get("GUARD_RUN_DIR", "")
if not run_dir:
    raise SystemExit("缺少 GUARD_RUN_DIR，请先完整执行本节开头的 export 命令")
run = Path(run_dir)
path = run / "config.yaml"
if not path.is_file():
    raise SystemExit("找不到安装器生成的 config.yaml，请先完成 CPA 安装")
cfg = yaml.safe_load(path.read_text())
if not isinstance(cfg, dict):
    raise SystemExit("config.yaml 必须是 YAML 映射")

def mapping(parent, name):
    if parent.get(name) is None:
        parent[name] = {}
    if not isinstance(parent[name], dict):
        raise SystemExit(name + " 必须是 YAML 映射")
    return parent[name]

raw_keys = cfg.get("api-keys") or []
if not isinstance(raw_keys, list):
    raise SystemExit("api-keys 必须是列表")
plugins = mapping(cfg, "plugins")
privacyfilter = mapping(mapping(plugins, "configs"), "privacyfilter")
if privacyfilter.get("store"):
    raise SystemExit("检测到旧 privacyfilter 商店配置，请先在管理页面卸载旧版，再停止 CPA 并重试")
placeholders = {"your-api-key-1", "your-api-key-2", "your-api-key-3"}
keys = [key for key in raw_keys
        if isinstance(key, str) and key.strip() and key.strip() not in placeholders]
management = mapping(cfg, "remote-management")
existing_management = management.get("secret-key") or ""
if not isinstance(existing_management, str):
    raise SystemExit("remote-management.secret-key 必须是字符串")
secret_path = run / "secrets.env"
if secret_path.exists():
    saved = {}
    for line in secret_path.read_text().splitlines():
        fields = shlex.split(line, comments=True)
        if not fields:
            continue
        if len(fields) != 2 or fields[0] != "export" or "=" not in fields[1]:
            raise SystemExit("secrets.env 格式异常，请保留 export NAME=value 格式")
        name, value = fields[1].split("=", 1)
        saved[name] = value
    client_key = saved.get("CPA_LANGUAGE_GUARD_API_KEY", "")
    admin_key = saved.get("CPA_LANGUAGE_GUARD_MANAGEMENT_KEY", "")
    if not client_key or client_key not in keys:
        raise SystemExit("secrets.env 的客户端 Key 不在当前配置中，请在本机核对并更新 secrets.env 后重试")
    if not existing_management and not admin_key:
        raise SystemExit("配置和 secrets.env 均缺少管理密钥，请在本机补齐管理密钥后重试")
else:
    client_key = keys[0] if keys else "sk-local-" + secrets.token_hex(32)
    admin_key = ("" if existing_management.startswith("$2") else existing_management) if existing_management else secrets.token_hex(32)

if client_key not in keys:
    keys.append(client_key)
cfg["api-keys"] = keys
port = int(os.environ.get("GUARD_PORT", "18316"))
if not 1 <= port <= 65535:
    raise SystemExit("GUARD_PORT 必须在 1–65535 之间")
cfg.update({
    "host": "127.0.0.1", "port": port,
    "debug": False, "request-log": False,
    "logging-to-file": True, "logs-max-total-size-mb": 200,
    "error-logs-max-files": 10, "ws-auth": True,
})
mapping(cfg, "tls")["enable"] = False
if not cfg.get("auth-dir"):
    cfg["auth-dir"] = str(run / "auth")
if "GUARD_UPSTREAM_PROXY_URL" in os.environ:
    cfg["proxy-url"] = os.environ["GUARD_UPSTREAM_PROXY_URL"]
management.update({"allow-remote": False, "disable-control-panel": False})
if not existing_management:
    management["secret-key"] = admin_key
plugins["enabled"] = True
if not plugins.get("dir"):
    plugins["dir"] = "plugins"
privacyfilter.update({
    "enabled": True,
    "max_han_percent": 20,
    "allow_quoted_input": True,
    "skip_models": [],
    "skip_formats": [],
})
mapping(cfg, "pprof")["enable"] = False
mapping(cfg, "discovery")["enabled"] = False

backup_fd, backup_name = tempfile.mkstemp(prefix="config.yaml.before-language-guard-", suffix=".bak", dir=run)
with os.fdopen(backup_fd, "wb") as stream:
    stream.write(path.read_bytes())
config_fd, config_name = tempfile.mkstemp(prefix=".config-", suffix=".yaml", dir=run)
with os.fdopen(config_fd, "w") as stream:
    yaml.safe_dump(cfg, stream, allow_unicode=True, sort_keys=False)
os.replace(config_name, path)
if not secret_path.exists():
    fd = os.open(secret_path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as stream:
        stream.write("export CPA_LANGUAGE_GUARD_API_KEY=" + shlex.quote(client_key) + "\n")
        stream.write("export CPA_LANGUAGE_GUARD_MANAGEMENT_KEY=" + shlex.quote(admin_key) + "\n")
secret_path.chmod(0o600)
print("配置已备份：" + backup_name)
print(f"配置和密钥已准备，privacyfilter 已启用，监听 127.0.0.1:{port}")
if existing_management.startswith("$2"):
    print("已有管理密钥哈希保持不变，登录管理页面需使用原始管理密码")
PY
~~~

命令成功后加载客户端凭证：

~~~bash
source "$GUARD_RUN_DIR/secrets.env"
test -n "$CPA_LANGUAGE_GUARD_API_KEY" && printf '%s\n' '客户端 Key 已加载'
~~~

脚本清除确切的示例 Key `your-api-key-1/2/3`，不会撤销其他真实 Key。
已有 secrets.env 的客户端 Key 若不在配置中，会停止，避免恢复已撤销的凭证。
YAML 注释保存在原文件备份中；PyYAML 保留值但不保留注释。
管理密钥最初可为明文，CPA 加载后会转为 bcrypt 并尝试回写；管理登录始终使用原始密码，不使用 `$2` 开头的哈希。

插件关键配置如下，脚本已经合并，无需重复添加顶层 `plugins`：

~~~yaml
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
~~~

关闭引用豁免时设 `allow_quoted_input: false`。保持两个 skip 列表为空可避免额外模型/协议豁免。
若有计费插件，还需按该插件的管理流程为客户端 Key 配置订阅、额度和模型权限。

### 6.4 安装插件文件

确认 CPA 仍已停止后执行。此命令按实际 `plugins.dir` 安装，支持相对路径和 `~`：

~~~bash
/usr/bin/python3 - <<'PY'
import os
import shutil
from pathlib import Path
import yaml

run = Path(os.environ["GUARD_RUN_DIR"])
cfg = yaml.safe_load((run / "config.yaml").read_text())
plugin_dir = Path(cfg["plugins"]["dir"]).expanduser()
if not plugin_dir.is_absolute():
    plugin_dir = run / plugin_dir
plugin_dir.mkdir(parents=True, exist_ok=True)
source = Path(os.environ["GUARD_PLUGIN_DIR"]) / "dist/privacyfilter.so"
temporary = plugin_dir / "privacyfilter.so.new"
shutil.copy2(source, temporary)
os.replace(temporary, plugin_dir / "privacyfilter.so")
build_record = source.parent / "deployment-build.txt"
if build_record.is_file():
    shutil.copy2(build_record, run / "deployment-build.txt")
print("插件已安装到：" + str(plugin_dir / "privacyfilter.so"))
PY
~~~

共享库直接放在插件目录中，不增加 `plugins/privacyfilter/` 中间目录。
不能覆盖仍被运行中的 CPA 加载的 `.so`；每次更换插件均按停服、替换、启动顺序操作。

## 7. 配置真实模型上游

已有可用上游时跳过本节，不要重复登录或新增重复提供商。
否则二选一。

### 7.1 OAuth 登录

在部署用户的终端使用自己的有效账号登录：

~~~bash
cd "$GUARD_RUN_DIR"
"$GUARD_CPA_DIR/cli-proxy-api" \
  --config "$GUARD_RUN_DIR/config.yaml" \
  --codex-login --local-model
~~~

无图形界面时可改用设备登录：

~~~bash
cd "$GUARD_RUN_DIR"
"$GUARD_CPA_DIR/cli-proxy-api" \
  --config "$GUARD_RUN_DIR/config.yaml" \
  --codex-device-login --no-browser --local-model
~~~

确认终端输出登录成功和认证保存位置，且与实际 `auth-dir` 一致。
浏览器回调默认使用 1455 端口；冲突时关闭占用程序或改用设备登录。
登录 Codex CLI 自身不等于 CPA 已获得上游凭证。

### 7.2 OpenAI 兼容 API

以下交互命令追加独立提供商，保留已有提供商；上游 Key 输入不回显。
地址需包含服务商要求的 API 基础路径，例如 `https://example.com/v1`，不能指回当前 CPA 自身。

~~~bash
/usr/bin/python3 - <<'PY'
import getpass
import os
from pathlib import Path
import yaml

path = Path(os.environ["GUARD_RUN_DIR"]) / "config.yaml"
cfg = yaml.safe_load(path.read_text())
with open("/dev/tty", "r") as tty_in, open("/dev/tty", "w") as tty_out:
    tty_out.write("上游 API 基础地址，例如 https://example.com/v1: ")
    tty_out.flush()
    base_url = tty_in.readline().strip()
    tty_out.write("上游真实模型名称: ")
    tty_out.flush()
    model = tty_in.readline().strip()
    key = getpass.getpass("上游 API Key（输入不回显）: ", stream=tty_out).strip()
if not base_url.startswith(("https://", "http://")) or not model or not key:
    raise SystemExit("地址、模型或 Key 不完整")
if cfg.get("openai-compatibility") is None:
    cfg["openai-compatibility"] = []
providers = cfg["openai-compatibility"]
if not isinstance(providers, list) or any(not isinstance(item, dict) for item in providers):
    raise SystemExit("openai-compatibility 必须是提供商配置列表")
if any(item.get("name") == "test-upstream" for item in providers):
    raise SystemExit("test-upstream 已存在，请编辑现有条目，避免重复添加")
providers.append({
    "name": "test-upstream",
    "base-url": base_url.rstrip("/"),
    "api-key-entries": [{"api-key": key}],
    "models": [{"name": model, "alias": "guard-model"}],
})
path.write_text(yaml.safe_dump(cfg, allow_unicode=True, sort_keys=False))
path.chmod(0o600)
print("上游已配置，客户端模型别名为 guard-model")
PY
~~~

这里生成的客户端模型别名为 `guard-model`。模型已有其他别名时，可直接使用实际 `/v1/models` 返回的名称。

## 8. 由 systemd 长期托管

用服务 drop-in 显式指定程序、工作目录和配置路径。安装器更新主 unit 时，drop-in 会保留。
以下命令会写入本部署使用的 `20-language-guard.conf`；已有同名自定义覆盖时先核对并备份。

~~~bash
mkdir -p "$HOME/.config/systemd/user/cliproxyapi.service.d"
cat > "$HOME/.config/systemd/user/cliproxyapi.service.d/20-language-guard.conf" <<EOF
[Service]
WorkingDirectory=$GUARD_CPA_DIR
ExecStart=
ExecStart="$GUARD_CPA_DIR/cli-proxy-api" --config "$GUARD_RUN_DIR/config.yaml" --local-model
UMask=0077
Restart=on-failure
RestartSec=5s
EOF

systemctl --user daemon-reload
systemctl --user cat cliproxyapi.service
systemctl --user enable --now cliproxyapi.service
systemctl --user status cliproxyapi.service --no-pager
~~~

确认 ExecStart 与 WorkingDirectory 指向本次安装路径，没有旧的源码仓库路径。
`--local-model` 关闭远程模型目录更新；不禁止首次下载管理页面资源。
CPA 从 YAML/认证文件读取凭证，服务不需要 `source secrets.env`，也不需要把客户端 Key 写入 unit。

需要开机后无需登录也运行，执行一次：

~~~bash
sudo loginctl enable-linger "$(id -un)"
loginctl show-user "$(id -un)" -p Linger
~~~

预期 `Linger=yes`。只 enable 用户服务不能保证用户未登录时自动运行。

常用运维命令：

~~~bash
systemctl --user status cliproxyapi.service --no-pager
journalctl --user -u cliproxyapi.service -n 80 --no-pager
ss -ltnp "( sport = :$GUARD_PORT )"
~~~

业务日志写入安装目录的 `logs/`。在日志中确认 `pluginhost: plugin loaded plugin_id=privacyfilter`。
服务 active 只说明进程在运行。CPA 遇到插件加载失败可能记录警告后继续提供接口，
必须完成下一节的中文拒绝验收后再投入使用；每次重启、升级和回滚都要重复验证。

## 9. 上线验收

### 9.1 管理页面和模型列表

本机管理页面默认是 `http://127.0.0.1:18316/management.html`，使用原始管理密码。
页面首次下载失败导致 404 时，检查 GitHub 网络与出站代理；这与模型接口是否可用分别验证。

在部署终端加载客户端 Key，查询模型：

~~~bash
source "$GUARD_RUN_DIR/secrets.env"
curl --noproxy 127.0.0.1 -fsS \
  -H "Authorization: Bearer $CPA_LANGUAGE_GUARD_API_KEY" \
  "$GUARD_API_BASE/models" \
  -o "$GUARD_RUN_DIR/models.json"

/usr/bin/python3 - <<'PY'
import json
import os
from pathlib import Path
models = json.loads((Path(os.environ["GUARD_RUN_DIR"]) / "models.json").read_text()).get("data", [])
if not models:
    raise SystemExit("模型列表为空，请检查上游凭证和模型映射")
for model in models:
    print(model["id"])
PY
~~~

从输出中选一个自己有权限使用的模型，不要凭名称猜测可用性：

~~~bash
export GUARD_MODEL='替换为模型列表中的实际名称'
~~~

使用第 7.2 节新建的别名时，可设 `export GUARD_MODEL='guard-model'`。

### 9.2 拒绝和真实上游请求

下面先要求中文请求得到插件的指定错误，再发送一条英文短请求确认真实模型链路；后者会产生一次真实模型调用。
脚本绕过本地 HTTP 代理，避免把访问 CPA 的请求发到出站代理。

~~~bash
/usr/bin/python3 - <<'PY'
import json
import os
import urllib.error
import urllib.request

model = os.environ["GUARD_MODEL"]
if model == "替换为模型列表中的实际名称":
    raise SystemExit("请先选择真实模型名称")
opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
for text, expected in (("请解释这个项目", 400), ("Reply only in English. Say hello.", 200)):
    body = {"model": model, "stream": False, "input": [{
        "type": "message", "role": "user",
        "content": [{"type": "input_text", "text": text}],
        "internal_chat_message_metadata_passthrough": {"content_item_kinds": ["user.text"]},
    }]}
    request = urllib.request.Request(os.environ["GUARD_API_BASE"] + "/responses",
        data=json.dumps(body).encode(), headers={
            "Authorization": "Bearer " + os.environ["CPA_LANGUAGE_GUARD_API_KEY"],
            "Content-Type": "application/json",
        })
    try:
        with opener.open(request, timeout=120) as response:
            status, raw = response.status, response.read()
    except urllib.error.HTTPError as error:
        status, raw = error.code, error.read()
    if status != expected:
        raise SystemExit(f"验收失败：预期 HTTP {expected}，实际 HTTP {status}，请检查 CPA 日志")
    data = json.loads(raw)
    if expected == 400:
        if data.get("error", {}).get("code") != "chinese_ratio_exceeded":
            raise SystemExit("HTTP 400 不是中文比例拒绝，请检查插件与来源标记")
        print("PASS：中文输入被插件拒绝")
    else:
        if data.get("error") or data.get("object") != "response" or data.get("status") != "completed" or not data.get("output"):
            raise SystemExit("上游未成功完成响应，请检查日志")
        print("PASS：英文输入获得真实上游响应")
PY
~~~

任一验收失败时，停止正式服务，再排查或按第 11 节回滚：

~~~bash
systemctl --user stop cliproxyapi.service
~~~

手动填写的来源标记仅用于接口验收，还要用第 10 节的真实 Codex 输入验证客户端确实保留元数据。
零上游调用的证据来自第 4 节模拟的 `upstream_calls: 0`，单凭真实接口 HTTP 400 不能证明该指标。

## 10. 接入 Codex 与远程客户端

### 10.1 配置正式 profile

Codex 端需要自己的客户端 Key 环境变量。与 CPA 同机时，沿用上面已加载的 `secrets.env`。
不同机器时，在客户端本地保存对应的客户端 Key 即可，无需复制管理密码或 OAuth 认证文件。

在 **运行 Codex 的终端**设置访问地址和实际模型名称：

~~~bash
export GUARD_API_BASE='http://127.0.0.1:18316/v1'
export GUARD_MODEL='替换为模型列表中的实际名称'
~~~

远程客户端需要录入客户端 Key 时，用下面方式避免写入命令历史：

~~~bash
read -r -s -p 'CPA 客户端 Key: ' CPA_LANGUAGE_GUARD_API_KEY
printf '\n'
export CPA_LANGUAGE_GUARD_API_KEY
~~~

生成独立正式 profile（已有文件不会覆盖）：

~~~bash
/usr/bin/python3 - <<'PY'
import json
import os
from pathlib import Path

root = Path(os.environ.get("CODEX_HOME") or str(Path.home() / ".codex"))
root.mkdir(parents=True, exist_ok=True)
path = root / "cpa-language-guard.config.toml"
model = os.environ["GUARD_MODEL"]
if model == "替换为模型列表中的实际名称":
    raise SystemExit("请先将 GUARD_MODEL 替换为真实模型名称")
text = (
    "model = " + json.dumps(model) + "\n"
    'model_provider = "cpa_language_guard"\n\n'
    '[features]\n'
    'content_item_kinds = true\n\n'
    '[model_providers.cpa_language_guard]\n'
    'name = "OpenAI"\n'
    'base_url = ' + json.dumps(os.environ['GUARD_API_BASE']) + '\n'
    'env_key = "CPA_LANGUAGE_GUARD_API_KEY"\n'
    'wire_api = "responses"\n'
    'requires_openai_auth = false\n'
    'supports_websockets = false\n'
)
if path.exists():
    raise SystemExit("profile 已存在，请编辑现有文件：" + str(path))
path.write_text(text)
print("已生成 profile：" + str(path))
PY
~~~

关键内容是：

~~~toml
[features]
content_item_kinds = true

[model_providers.cpa_language_guard]
name = "OpenAI"
base_url = "http://127.0.0.1:18316/v1"
env_key = "CPA_LANGUAGE_GUARD_API_KEY"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = false
~~~

`env_key` 填变量名，不能填真实 Key。`name = "OpenAI"` 是已验证客户端保留来源标记的必要配置，实际连接地址由 `base_url` 决定。
默认 profile 为 `~/.codex/cpa-language-guard.config.toml`；设置 CODEX_HOME 时位于对应目录。
已有测试 profile `cpa-language-guard-test` 不会自动变为正式 profile，启动时应明确选择：

~~~bash
test -n "$CPA_LANGUAGE_GUARD_API_KEY" && printf '%s\n' '客户端 Key 已加载'
codex --profile cpa-language-guard
~~~

在真实 Codex 会话分别输入：

| 输入 | 预期 |
|---|---|
| `Reply only in English. Say hello.` | 正常获得英文响应 |
| `请解释这个项目的作用` | HTTP 400，中文超标提示及英文提示词示例 |
| `中abcd` | 恰好 20%，放行 |
| `中abc` | 25%，拒绝 |
| `> 请解释这个项目的作用` | 默认整条豁免；关闭引用豁免时拒绝 |

英文提示词示例允许在文件内容中按任务要求使用中文，包括文档、代码注释和前端页面文字等；对话回复要求英文。
这些要求需由用户写入实际指令，插件不自动改变模型回复语言。

### 10.2 远程访问

个人使用可以保持 CPA 只监听回环地址，在客户端建立 SSH 隧道：

~~~bash
ssh -N -L 18316:127.0.0.1:18316 DEPLOY_USER@CPA_HOST
~~~

保持该终端运行，Codex 仍连接客户端的 `http://127.0.0.1:18316/v1`。
本地端口被占用时，修改 `-L` 最左侧端口和客户端 profile，右侧端口保持为远端 CPA 的实际监听端口。

直接提供局域网或公网服务时，将 CPA 放在配置好 HTTPS 的反向代理后，或启用 CPA 的 TLS 配置。
Codex 的地址改为实际 HTTPS API 地址；反向代理需允许 SSE 流式响应、关闭响应缓冲，并给模型生成足够的读取超时。
保留客户端 Key 认证；管理接口继续限制在本机或通过 SSH 访问。`127.0.0.1` 始终指客户端自身，不是远程服务器。

## 11. 更新与回滚

### 11.1 更新插件

1. 获取并审阅功能分支的新提交，选定部署版本；不要把 `main` 当作中文检查插件发布分支。
2. 按第 4 节构建并模拟，通过后再安排切换。
3. 按第 5 节停服、备份；按第 6.4 节安装共享库。
4. 启动并重新完成第 9、10 节验收：

~~~bash
systemctl --user start cliproxyapi.service
systemctl --user status cliproxyapi.service --no-pager
~~~

需要快进本地功能分支时，在工作区干净且确认远端变更后执行：

~~~bash
git -C "$GUARD_PLUGIN_DIR" fetch origin feat/chinese-input-guard
git -C "$GUARD_PLUGIN_DIR" merge --ff-only origin/feat/chinese-input-guard
~~~

### 11.2 更新 CPA

先按第 5 节停服并备份，再运行第 3 节安装器。安装器只保留有限旧版本且不会完整备份插件/认证，不能替代部署快照。
停服后再调用安装器也可避免它自动重启原来处于运行状态的服务。

更新完成后先用新二进制执行第 4 节模拟，再 `systemctl --user daemon-reload`、核对 drop-in 并启动。
模拟或实际验收不通过时回滚。不要在无验证的情况下把 CPA、插件和 Codex 同时更新。
每日 GitHub Action 只同步两个仓库的 `main`，不会部署服务器，也不会更新插件功能分支或本机发行包。

### 11.3 回滚

先停止用户服务和任何前台实例。选择第 5 节记录的快照：

~~~bash
systemctl --user stop cliproxyapi.service
export GUARD_BACKUP='/替换为实际的/snapshot.XXXXXXXX'
~~~

下面恢复程序、插件、配置及安装器版本信息。现有插件目录和即将覆盖的文件会留存在快照里的 `before-rollback.*` 目录。
**不恢复 auth 目录**，以保留当前 OAuth 刷新状态。若更换过 auth-dir，先核对快照记录的路径与当前凭证位置。

~~~bash
/usr/bin/python3 - <<'PY'
import json
import os
import shutil
import tempfile
from pathlib import Path

backup = Path(os.environ["GUARD_BACKUP"])
paths = json.loads((backup / "paths.json").read_text())
run = Path(os.environ["GUARD_RUN_DIR"])
if run != Path(paths["run_dir"]):
    raise SystemExit("快照属于另一安装目录，停止回滚")
for name in ("cli-proxy-api", "config.yaml"):
    if not (backup / name).is_file():
        raise SystemExit("快照缺少 " + name)
plugin_dir = Path(paths["plugin_dir"]).resolve()
auth_dir = Path(paths["auth_dir"]).resolve()
if (run.resolve().is_relative_to(plugin_dir) or auth_dir.is_relative_to(plugin_dir)
        or backup.resolve().is_relative_to(plugin_dir)):
    raise SystemExit("快照中的插件目录与程序、认证或备份目录重叠，停止自动回滚")
recovery = Path(tempfile.mkdtemp(prefix="before-rollback.", dir=backup))
if plugin_dir.exists():
    shutil.move(str(plugin_dir), str(recovery / "plugins"))
if (backup / "plugins").is_dir():
    shutil.copytree(backup / "plugins", plugin_dir)
for name in ("cli-proxy-api", "config.yaml", "version.txt", "asset-variant.txt", "secrets.env", "deployment-build.txt"):
    target = run / name
    if target.exists():
        shutil.copy2(target, recovery / name)
    saved = backup / name
    if saved.exists():
        temporary = run / (name + ".rollback")
        shutil.copy2(saved, temporary)
        temporary.chmod(0o700 if name == "cli-proxy-api" else 0o600)
        os.replace(temporary, target)
print("代码、插件及配置已回滚，当前认证文件未恢复")
print("回滚前文件保存在：" + str(recovery))
PY
~~~

快照中没有 secrets.env 时，原配置可能不接受当前客户端 Key，需按恢复后的 `api-keys` 重新配置客户端。
上述回滚针对同一部署目录的版本更新；如果此次也修改了 systemd unit/drop-in，请对照快照内的副本恢复服务配置，之后执行：

~~~bash
systemctl --user daemon-reload
systemctl --user start cliproxyapi.service
systemctl --user status cliproxyapi.service --no-pager
~~~

重新加载对应的客户端 Key，按第 9、10 节验收后再恢复使用。
认证目录损坏或丢失时才考虑 auth 备份恢复；备份 OAuth 令牌已失效时需要重新登录。

## 12. 故障定位

| 现象 | 优先检查 |
|---|---|
| `502 Bad Gateway` / `Reconnecting` | 实际 profile 的 base_url、CPA 是否监听该端口、CPA 出站代理是否运行、上游错误日志 |
| 报错 URL 是 `8317`，CPA 在 `18316` | 正在使用旧 profile 或旧 base_url；退出会话，修正对应 profile 后重新启动 |
| 没有监听却得到 502 | 检查客户端进程使用的 HTTP 代理；仅凭 URL 无法判断 502 是 CPA 还是其他代理生成 |
| 模型列表正常、实际请求失败 | 列表只证明认证和模型注册；检查上游凭证/权限/额度以及出站代理 |
| 配置了 `127.0.0.1:7897` 等代理但端口未监听 | 修正为部署机器的实际代理，或按访问条件清除配置，同时检查提供商级代理与服务环境 |
| `Missing environment variable` | env_key 必须是 CPA_LANGUAGE_GUARD_API_KEY；同一客户端终端加载 Key 后启动 |
| `input_attribution_unavailable` | 来源标记未保留；核对 Codex 版本、提供商名称 OpenAI、content_item_kinds 和 HTTP Responses |
| `chinese_ratio_exceeded` | 正常规则拒绝，按中文提示翻译后重新发送，或按既定规则显式豁免 |
| 服务 active 但中文未拦截 | 核对插件加载日志、同 ID 的商店版本、两个 enabled 和 skip 配置；实际发送中文验收 |
| 服务启动失败 / 端口被占用 | 是否还有前台 CPA，ExecStart/WorkingDirectory 是否正确 |
| 退出登录后服务停止 | 检查 Linger、用户服务 enable 状态；也检查出站代理是否随桌面退出 |
| 管理登录失败 | 用原始管理密码，不能用客户端 Key 或 bcrypt 哈希 |
| Model metadata / Skill descriptions 警告 | 与端口、认证和502分别排查，不因警告自动更换模型 |

先确认请求地址和监听端口：

~~~bash
ss -ltnp '( sport = :18316 or sport = :8317 )'
systemctl --user status cliproxyapi.service --no-pager
journalctl --user -u cliproxyapi.service -n 80 --no-pager
~~~

日志可能包含上游错误详情，排查时只提取所需的时间、错误码和错误原因。
中文比例拦截应为 **HTTP 400 + chinese_ratio_exceeded**，502 不属于预期拦截结果，反复重试不能修复错误端口或失效代理。

## 13. 参考

- [官方 CPA 安装器](https://github.com/router-for-me/cliproxyapi-installer)
- [插件规则与配置](../README.zh-CN.md)
- [完整本地测试指南](local-testing.zh-CN.md)
- [OpenAI Docs：Codex 独立 profile、提供商及环境变量名](https://learn.chatgpt.com/docs/config-file/config-advanced.md)

profile 的独立文件用法已与 OpenAI 官方文档核对；来源标记配置以本仓库对 Codex 0.154.0 的真实客户端模拟结果为依据。
