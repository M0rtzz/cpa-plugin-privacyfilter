# 从零开始测试 CPA 中文输入检查插件

本文以 Ubuntu 24.04、Go 1.26、Codex CLI 0.154.0 为基线。
其他 Linux 发行版需要调整系统软件包安装命令；配置生成与校验使用 Python 3.11 或更高版本。
CPA 使用 M0rtzz/CLIProxyAPI 的 main 分支，插件使用 feat/chinese-input-guard 分支。
手动测试端口为 **18316**。

文档中的命令使用 Bash。即使平时使用 Zsh 或 Fish，也请先在测试终端执行：

~~~bash
bash
~~~

下文的源码复现路线按顺序完成“编译 → 自动模拟 → 生成密钥与配置 → 配置真实上游 → 启动 CPA → 配置 Codex → 验收”。
自动模拟不需要真实模型凭证；连接真实模型时需要自己的可用账号或上游 API Key。
文中的密钥均由你在本机生成，不需要复用任何聊天记录中的密钥。

## CPA 安装方式选择

本方案没有修改 CPA 的业务源码，**可以直接使用官方安装器安装 CPA 发行版**。
下文编译 CPA 的步骤用于复现构建和自动模拟，是可选路线，不是使用插件的前提；插件仍需使用本功能分支构建的共享库。

- 安装器默认目录为 `~/cliproxyapi`。自定义目录时使用
  `INSTALL_DIR="$HOME/Programs/cliproxyapi"`，不要写 `INSTALL_DIR="~/Programs/cliproxyapi"`，双引号中的 `~` 不会展开。
- 在 glibc 系统上选择支持插件的 `default` 包，`no-plugin` 包不支持加载本插件。
- 安装器会生成客户端 Key，但当前脚本可能残留 `your-api-key-3`。
  检查实际 `config.yaml` 的 `api-keys`，完整替换所有占位 Key，不要只修改第一项。
- 已安装 CPA 时，在其实际 `config.yaml` 中启用 `privacyfilter`，并把 `.so` 放入配置的插件目录即可。
  运行目录由安装器生成的服务配置决定（例如 systemd 的 `WorkingDirectory`）；相对路径应结合该目录核对，不要默认使用源码仓库目录。

## 1. 先分清三种凭证

| 凭证 | 如何获得 | 填在哪里 | 用途 |
|---|---|---|---|
| CPA 客户端 API Key | 本地随机生成 | CPA 的 api-keys、Codex 的 CPA_LANGUAGE_GUARD_API_KEY | Codex / curl 访问本地 CPA |
| CPA 管理密钥 | 另行随机生成 | remote-management.secret-key | 登录管理页面、调用管理 API |
| 真实模型的上游凭证 | 在 CPA 中完成 OAuth 登录，或由上游服务商签发 API Key | auth-dir 中的认证文件，或上游配置 | CPA 访问真实模型 |

**本地生成的随机 Key 不能作为 OpenAI 或其他服务商的上游 Key。**
管理密钥也不能代替客户端 Key 调用 /v1/responses。

## 2. 安装依赖

### 2.1 系统工具

~~~bash
sudo apt-get update
sudo apt-get install -y \
  git openssh-client curl ca-certificates build-essential make \
  python3 python3-yaml xz-utils

gcc --version
make --version
python3 --version
~~~

配置生成步骤使用系统 Python /usr/bin/python3 及其 PyYAML。
如果终端启用了 Conda，仍使用本文中的 /usr/bin/python3 运行配置生成命令。

### 2.2 安装 Go 1.26

先检查已有版本：

~~~bash
go version
~~~

如果已经是 Go 1.26 或更高版本，可以跳过下面的安装步骤。
以下命令将 Go 1.26.0 安装在用户目录，不覆盖系统 Go：

~~~bash
case "$(uname -m)" in
  x86_64) GUARD_GO_ARCH=amd64 ;;
  aarch64|arm64) GUARD_GO_ARCH=arm64 ;;
  *) echo "请从 https://go.dev/dl/ 选择适合本机的 Go 包"; exit 1 ;;
esac

export GUARD_GO_FILE="go1.26.0.linux-$GUARD_GO_ARCH.tar.gz"
export GUARD_GO_DOWNLOAD_DIR
GUARD_GO_DOWNLOAD_DIR="$(mktemp -d)"

curl -fL "https://go.dev/dl/$GUARD_GO_FILE" \
  -o "$GUARD_GO_DOWNLOAD_DIR/$GUARD_GO_FILE"
curl -fsSL 'https://go.dev/dl/?mode=json&include=all' \
  -o "$GUARD_GO_DOWNLOAD_DIR/releases.json"

/usr/bin/python3 - <<'PY'
import hashlib
import json
import os
from pathlib import Path

root = Path(os.environ["GUARD_GO_DOWNLOAD_DIR"])
name = os.environ["GUARD_GO_FILE"]
releases = json.loads((root / "releases.json").read_text())
expected = next(
    item["sha256"]
    for release in releases
    for item in release["files"]
    if item["filename"] == name
)
with (root / name).open("rb") as stream:
    actual = hashlib.file_digest(stream, "sha256").hexdigest()
if actual != expected:
    raise SystemExit("Go 安装包 SHA256 校验失败，请勿继续安装")
print("Go 安装包 SHA256 校验通过")
PY
~~~

看到校验通过后，再执行：

~~~bash
if [ -e "$HOME/.local/opt/go-1.26.0" ]; then
  echo "Go 目录已存在，直接复用；不会覆盖解压"
else
  mkdir -p "$HOME/.local/opt/go-1.26.0"
  tar -xzf "$GUARD_GO_DOWNLOAD_DIR/$GUARD_GO_FILE" \
    -C "$HOME/.local/opt/go-1.26.0"
fi
export PATH="$HOME/.local/opt/go-1.26.0/go/bin:$PATH"

go version
~~~

之后新开终端编译时，也要设置这条 PATH，或自行添加到 Shell 启动配置。
Go 1.26.0 官方下载清单来自 https://go.dev/dl/。

### 2.3 安装 Codex CLI

先检查 Node.js 和 npm：

~~~bash
node --version
npm --version
~~~

Codex 0.154.0 的 npm 包要求 Node.js 16 或更高版本。已有符合要求的 Node.js（例如你的 22.16.0）时，
保留现有安装；缺少 Node.js / npm 时，Ubuntu 24.04 可以执行：

~~~bash
sudo apt-get install -y nodejs npm
~~~

然后检查 Codex：

~~~bash
codex --version
~~~

如果已经是 0.154.0，可以直接继续。需要安装该版本时，使用独立的用户目录：

~~~bash
npm install --prefix "$HOME/.local/share/codex-0.154.0" @openai/codex@0.154.0
export PATH="$HOME/.local/share/codex-0.154.0/node_modules/.bin:$PATH"
codex --version
~~~

新终端也需要设置这条 PATH。本文的来源标记和 profile 行为已在 0.154.0 验证；
升级 Codex 后，应重新执行包含真实客户端的模拟测试。

## 3. 获取仓库并编译

在当前 Bash 终端定义路径：

这些变量只对当前终端及其子进程生效；新开终端后需要重新执行 export。
Python 通过 os.environ 读取的变量必须经过 export，仅写 GUARD_RUN_DIR=... 不足以传给 Python。

~~~bash
export GUARD_ROOT="$HOME/Workspaces/Misc"
export GUARD_CPA_DIR="$GUARD_ROOT/CLIProxyAPI"
export GUARD_PLUGIN_DIR="$GUARD_ROOT/cpa-plugin-privacyfilter"
export GUARD_RUN_DIR="$GUARD_CPA_DIR/dist/language-guard-test"
mkdir -p "$GUARD_ROOT"
~~~

第一次下载：

~~~bash
if [ ! -d "$GUARD_CPA_DIR/.git" ]; then
  git clone git@github.com:M0rtzz/CLIProxyAPI.git "$GUARD_CPA_DIR"
fi
if [ ! -d "$GUARD_PLUGIN_DIR/.git" ]; then
  git clone git@github.com:M0rtzz/cpa-plugin-privacyfilter.git "$GUARD_PLUGIN_DIR"
fi
~~~

如果未配置 GitHub SSH Key，可将两个 clone 地址分别换为：

~~~text
https://github.com/M0rtzz/CLIProxyAPI.git
https://github.com/M0rtzz/cpa-plugin-privacyfilter.git
~~~

已有仓库时，上面的命令会跳过下载。切换分支前检查工作区，保留已有本地修改：

~~~bash
git -C "$GUARD_CPA_DIR" status --short --branch
git -C "$GUARD_CPA_DIR" switch main
git -C "$GUARD_PLUGIN_DIR" status --short --branch
git -C "$GUARD_PLUGIN_DIR" switch feat/chinese-input-guard
~~~

如果本地尚无插件功能分支，先执行下面两条，再继续：

~~~bash
git -C "$GUARD_PLUGIN_DIR" fetch origin feat/chinese-input-guard
git -C "$GUARD_PLUGIN_DIR" switch --track origin/feat/chinese-input-guard
~~~

为 CPA 的构建和测试目录增加本地 Git 忽略规则（不修改仓库中的 .gitignore）：

~~~bash
/usr/bin/python3 - <<'PY'
import os
from pathlib import Path
import subprocess

repo = Path(os.environ["GUARD_CPA_DIR"])
name = subprocess.check_output(
    ["git", "rev-parse", "--git-path", "info/exclude"], cwd=repo, text=True
).strip()
path = Path(name)
if not path.is_absolute():
    path = repo / path
path.parent.mkdir(parents=True, exist_ok=True)
text = path.read_text() if path.exists() else ""
if "/dist/" not in text.splitlines():
    path.write_text(text.rstrip("\n") + "\n/dist/\n")
print("CPA 的 dist/ 已加入本地 Git 忽略规则")
PY
~~~

编译：

~~~bash
cd "$GUARD_CPA_DIR"
mkdir -p dist
CGO_ENABLED=1 go build -o dist/cli-proxy-api-language-guard ./cmd/server

cd "$GUARD_PLUGIN_DIR"
go test ./...
make build BUILD_DIR=dist
~~~

CPA 和插件都必须支持 CGO。生成文件为：

~~~text
CLIProxyAPI/dist/cli-proxy-api-language-guard
cpa-plugin-privacyfilter/dist/privacyfilter.so
~~~

## 4. 先运行自动模拟

~~~bash
cd "$GUARD_PLUGIN_DIR"
python3 scripts/simulate-cpa.py \
  --cpa "$GUARD_CPA_DIR/dist/cli-proxy-api-language-guard" \
  --plugin "$GUARD_PLUGIN_DIR/dist/privacyfilter.so" \
  --codex "$(command -v codex)"
~~~

预期最后出现：

~~~text
PASS: 31 scenarios; artifacts: .../dist/simulation
~~~

说明：

- 前 27 项是 CPA 加载真实原生插件后的 HTTP 接口模拟。
- 后 4 项调用真实 Codex CLI。每项退出后才打印结果，单项超时为 60 秒；短时间没有输出并不表示失败。
- 不带 --codex 参数时只执行 27 项接口模拟。
- 预期拦截的场景应返回 HTTP 400，且 upstream_calls 为 0。
- 放行场景应调用本地模拟上游；这里不会访问真实模型。
- 测试用 --ignore-user-config、--ephemeral 和临时工作目录。客户端仍可能初始化本机安装的技能或连接器，相关启动信息可出现在日志里。

查看结果：

~~~bash
python3 -m json.tool "$GUARD_PLUGIN_DIR/dist/simulation/results.json"
~~~

日志位于同目录的 cpa.log 和 codex-*.log。
每次运行使用相同输出目录，因此检查文件时间，避免把上次的日志当作本次结果。
手动测试使用下面新建的目录，不复用模拟脚本的 config.yaml。

## 5. 创建手动测试目录并生成 Key

目录结构：

~~~text
CLIProxyAPI/dist/
├── cli-proxy-api-language-guard
└── language-guard-test/
    ├── config.yaml
    ├── secrets.env
    ├── auth/
    └── plugins/
        └── privacyfilter.so
~~~

下面整段可在新开的 Bash 终端执行：恢复路径、创建目录、安装插件并生成两个独立的随机 Key。
默认目录与第 3 节相同；已有变量会继续使用。如果之前使用了自定义目录，先恢复你的自定义路径，
然后复制执行完整代码块，包括开头的 export 命令。
secrets.env 已存在时会保留原有 Key。

~~~bash
export GUARD_ROOT="${GUARD_ROOT:-$HOME/Workspaces/Misc}"
export GUARD_CPA_DIR="${GUARD_CPA_DIR:-$GUARD_ROOT/CLIProxyAPI}"
export GUARD_PLUGIN_DIR="${GUARD_PLUGIN_DIR:-$GUARD_ROOT/cpa-plugin-privacyfilter}"
export GUARD_RUN_DIR="${GUARD_RUN_DIR:-$GUARD_CPA_DIR/dist/language-guard-test}"

printf '测试目录：%s\n' "$GUARD_RUN_DIR"
mkdir -p "$GUARD_RUN_DIR/auth" "$GUARD_RUN_DIR/plugins"
cp "$GUARD_PLUGIN_DIR/dist/privacyfilter.so" \
  "$GUARD_RUN_DIR/plugins/privacyfilter.so"
chmod 700 "$GUARD_RUN_DIR" "$GUARD_RUN_DIR/auth"

/usr/bin/python3 - <<'PY'
import os
import secrets
from pathlib import Path

run_dir = os.environ.get("GUARD_RUN_DIR", "")
if not run_dir.strip():
    raise SystemExit("缺少 GUARD_RUN_DIR，请先执行本代码块开头的 export 命令")
path = Path(run_dir) / "secrets.env"
if path.exists():
    print("secrets.env 已存在，继续使用原有 Key")
else:
    if (path.parent / "config.yaml").exists():
        raise SystemExit("已有 config.yaml 但缺少 secrets.env，请恢复原密钥文件或使用新的测试目录")
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, "w") as stream:
        stream.write(
            "export CPA_LANGUAGE_GUARD_API_KEY='sk-local-"
            + secrets.token_hex(32) + "'\n"
        )
        stream.write(
            "export CPA_LANGUAGE_GUARD_MANAGEMENT_KEY='"
            + secrets.token_hex(32) + "'\n"
        )
    print("已生成客户端 Key 和管理密钥，保存到 secrets.env")
PY

source "$GUARD_RUN_DIR/secrets.env"
~~~

管理密钥为 64 个 ASCII 字符，符合 bcrypt 的长度限制。
Key 不会写入本文或打印到上述命令的输出中。
secrets.env 保存的是原始值；需要登录管理页面时，在本机打开该文件复制管理密钥。

## 6. 生成完整 CPA 配置

这里读取 CPA 自带的 config.example.yaml，保留默认配置结构，再设置测试所需字段。
因此从远程刚克隆的 CPA 模板即使尚未包含 privacyfilter，也能生成正确配置。

在执行本节前恢复路径并载入第 5 节生成的 Key，尤其是在新开的终端中：

~~~bash
export GUARD_ROOT="${GUARD_ROOT:-$HOME/Workspaces/Misc}"
export GUARD_CPA_DIR="${GUARD_CPA_DIR:-$GUARD_ROOT/CLIProxyAPI}"
export GUARD_PLUGIN_DIR="${GUARD_PLUGIN_DIR:-$GUARD_ROOT/cpa-plugin-privacyfilter}"
export GUARD_RUN_DIR="${GUARD_RUN_DIR:-$GUARD_CPA_DIR/dist/language-guard-test}"
printf '测试目录：%s\n' "$GUARD_RUN_DIR"
source "$GUARD_RUN_DIR/secrets.env"
~~~

如果 secrets.env 不存在，先完成第 5 节；已有自定义目录时应继续使用同一目录。

如果上游必须通过你提供的本地代理访问，先执行：

~~~bash
export GUARD_UPSTREAM_PROXY_URL='http://127.0.0.1:49872'
~~~

不需要代理则执行：

~~~bash
export GUARD_UPSTREAM_PROXY_URL=''
~~~

代理地址指的是 **CPA 所在机器**，应当确实存在。然后生成配置：

~~~bash
/usr/bin/python3 - <<'PY'
import os
from pathlib import Path
import yaml

required = ("GUARD_CPA_DIR", "GUARD_RUN_DIR", "CPA_LANGUAGE_GUARD_API_KEY", "CPA_LANGUAGE_GUARD_MANAGEMENT_KEY")
missing = [name for name in required if not os.environ.get(name, "").strip()]
if missing:
    raise SystemExit("缺少环境变量：" + ", ".join(missing) + "；请先执行本节开头的 export 和 source 命令")
root = Path(os.environ["GUARD_CPA_DIR"])
run = Path(os.environ["GUARD_RUN_DIR"])
path = run / "config.yaml"
if path.exists():
    raise SystemExit("config.yaml 已存在，请编辑现有文件；本命令不会覆盖")

cfg = yaml.safe_load((root / "config.example.yaml").read_text())
cfg.update({
    "host": "127.0.0.1",
    "port": 18316,
    "tls": {"enable": False, "cert": "", "key": ""},
    "auth-dir": str(run / "auth"),
    "api-keys": [os.environ["CPA_LANGUAGE_GUARD_API_KEY"]],
    "debug": True,
    "logging-to-file": True,
    "usage-statistics-enabled": False,
    "proxy-url": os.environ.get("GUARD_UPSTREAM_PROXY_URL", ""),
    "request-retry": 0,
    "ws-auth": True,
    "plugins": {
        "enabled": True,
        "dir": str(run / "plugins"),
        "configs": {
            "privacyfilter": {
                "enabled": True,
                "max_han_percent": 20,
                "allow_quoted_input": True,
                "skip_models": [],
                "skip_formats": [],
            }
        },
    },
})
cfg.setdefault("remote-management", {}).update({
    "allow-remote": False,
    "secret-key": os.environ["CPA_LANGUAGE_GUARD_MANAGEMENT_KEY"],
    "disable-control-panel": False,
    "disable-auto-update-panel": True,
})
cfg.setdefault("pprof", {})["enable"] = False
cfg.setdefault("discovery", {})["enabled"] = False
fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, "w") as stream:
    yaml.safe_dump(cfg, stream, allow_unicode=True, sort_keys=False)
print("已生成独立测试配置，端口 18316，privacyfilter 已启用")
PY
~~~

这会生成完整 YAML，但 PyYAML 不保留原模板注释；字段说明仍可查阅 config.example.yaml。
安装路径使用绝对路径，避免从其他目录启动时找不到插件和认证目录。

两处开关都已启用：

~~~yaml
plugins:
  enabled: true
  configs:
    privacyfilter:
      enabled: true
~~~

管理密钥最初写入明文。CPA **首次加载配置时**会转换成 bcrypt 哈希并尝试回写；
执行下一节的登录命令也可能触发这个过程。管理页面仍使用 secrets.env 中的原始密钥，
不能使用 config.yaml 中以 $2 开头的哈希登录。

从零测试默认只加载本插件。如果要接入你已有的计费等插件，请看第 11 节。

## 7. 配置真实上游：任选一种方式

### 7.1 方式 A：通过 CPA 登录 Codex OAuth

需要自己的、具有可用模型权限的 OpenAI 账号。
在当前终端中执行：

~~~bash
cd "$GUARD_RUN_DIR"
"$GUARD_CPA_DIR/dist/cli-proxy-api-language-guard" \
  --config "$GUARD_RUN_DIR/config.yaml" \
  --codex-login \
  --local-model
~~~

按终端提示完成浏览器登录。OAuth 回调端口为 1455；
如果该端口被占用，请先停止占用该端口的程序，或使用下面的设备登录。

无图形界面或浏览器回调不方便时，可改用设备登录：

~~~bash
cd "$GUARD_RUN_DIR"
"$GUARD_CPA_DIR/dist/cli-proxy-api-language-guard" \
  --config "$GUARD_RUN_DIR/config.yaml" \
  --codex-device-login \
  --no-browser \
  --local-model
~~~

打开终端给出的设备登录页面，在有效期内输入显示的设备码并授权。
两种登录方式选一种即可。

成功后应看到 Authentication saved to ... 及登录成功信息，
凭证保存在本次测试的 auth/ 目录。不要只根据命令退出码判断成功。
仅安装或登录 Codex CLI，不等于 CPA 已取得上游凭证。

### 7.2 方式 B：使用服务商提供的 OpenAI 兼容 API Key

如果没有 OAuth 账号，可在自己的上游服务商控制台创建 API Key。
本地随机生成的客户端 Key 不能用于这里。

下面的交互式命令会询问上游地址、模型和真实 Key，并更新测试配置：

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
cfg["openai-compatibility"] = [{
    "name": "test-upstream",
    "base-url": base_url.rstrip("/"),
    "api-key-entries": [{"api-key": key}],
    "models": [{"name": model, "alias": "guard-model"}],
}]
path.write_text(yaml.safe_dump(cfg, allow_unicode=True, sort_keys=False))
path.chmod(0o600)
print("上游已配置，客户端模型别名为 guard-model")
PY
~~~

若上游本身是已有 CPA，则填写它的地址、有效客户端 Key 和它实际提供的模型名称。
本次测试服务在 18316；上游地址不能指回本次测试服务自身。
这是有意选择的 API 接入方式，OAuth 方式不需要这一段配置。

## 8. 启动 CPA 并检查管理页面

在当前终端（终端 A）前台运行：

~~~bash
cd "$GUARD_RUN_DIR"
"$GUARD_CPA_DIR/dist/cli-proxy-api-language-guard" \
  --config "$GUARD_RUN_DIR/config.yaml" \
  --local-model
~~~

保持该进程运行。另开终端 B，执行 Bash 并恢复路径和客户端 Key：

~~~bash
bash
export GUARD_ROOT="$HOME/Workspaces/Misc"
export GUARD_CPA_DIR="$GUARD_ROOT/CLIProxyAPI"
export GUARD_PLUGIN_DIR="$GUARD_ROOT/cpa-plugin-privacyfilter"
export GUARD_RUN_DIR="$GUARD_CPA_DIR/dist/language-guard-test"
source "$GUARD_RUN_DIR/secrets.env"
~~~

如果使用本文的独立 Codex 安装，再设置：

~~~bash
export PATH="$HOME/.local/share/codex-0.154.0/node_modules/.bin:$PATH"
~~~

检查终端 A 的输出或测试目录下 logs/ 中的日志，确认：

~~~text
pluginhost: plugin loaded plugin_id=privacyfilter
~~~

通过本机浏览器访问：

~~~text
http://127.0.0.1:18316/management.html
~~~

管理页面使用 CPA_LANGUAGE_GUARD_MANAGEMENT_KEY 的**原始值**登录。
首次访问可能下载管理页面资源；下载失败导致页面 404 时，先检查代理或 GitHub 连通性。
这不等于插件加载失败。--local-model 只关闭远程模型目录更新，不禁止首次下载管理页面。

查询模型列表：

~~~bash
curl --noproxy 127.0.0.1 -fsS \
  -H "Authorization: Bearer $CPA_LANGUAGE_GUARD_API_KEY" \
  http://127.0.0.1:18316/v1/models \
  -o "$GUARD_RUN_DIR/models.json"

/usr/bin/python3 - <<'PY'
import json
import os
from pathlib import Path

path = Path(os.environ["GUARD_RUN_DIR"]) / "models.json"
data = json.loads(path.read_text())
models = data.get("data", [])
if not models:
    raise SystemExit("模型列表为空，请先完成上游登录或配置")
for model in models:
    print(model["id"])
PY
~~~

这里使用的是**客户端 Key**。看到模型列表只证明认证和模型注册可用；
下一步的实际请求才验证完整上游链路。

## 9. 配置独立 Codex profile

从上一步的输出中选择模型名称，设置下面的变量：

~~~bash
export GUARD_MODEL='替换为模型列表中的实际名称'
~~~

采用方式 B 且保留本文别名时，可以直接设置：

~~~bash
export GUARD_MODEL='guard-model'
~~~

生成独立 profile，文件存在时不会覆盖：

~~~bash
/usr/bin/python3 - <<'PY'
import json
import os
from pathlib import Path

root = Path(os.environ.get("CODEX_HOME") or str(Path.home() / ".codex"))
root.mkdir(parents=True, exist_ok=True)
path = root / "cpa-language-guard-test.config.toml"
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
    'base_url = "http://127.0.0.1:18316/v1"\n'
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

默认文件位置为 ~/.codex/cpa-language-guard-test.config.toml。
若设置了 CODEX_HOME，则使用该目录。Codex 0.154.0 的 --profile 读取独立文件，
不是基础 config.toml 中的 profiles 表。

**env_key 填写环境变量名，不填写 API Key 的实际值。** 保持下面这一行不变：

~~~toml
env_key = "CPA_LANGUAGE_GUARD_API_KEY"
~~~

实际客户端 Key 保存在 secrets.env 中，由启动 Codex 的终端加载。若把 Key 值填入 env_key，
Codex 会将整个 Key 当作环境变量名查找，进而报 Missing environment variable。

以下设置是来源识别所需：

~~~toml
[features]
content_item_kinds = true

[model_providers.cpa_language_guard]
name = "OpenAI"
wire_api = "responses"
supports_websockets = false
~~~

提供商名称 OpenAI 在这个版本中用于保留内容来源元数据；
实际目标仍由 base_url 指定为本地 CPA。
如果改成 Custom 等其他名称，英文请求也可能返回 input_attribution_unavailable。

在启动 Codex 的同一个终端加载 Key，并检查变量非空。以下检查不打印密钥：

~~~bash
source "$GUARD_RUN_DIR/secrets.env"
test -n "$CPA_LANGUAGE_GUARD_API_KEY" && printf '%s\n' '客户端 Key 环境变量已加载'
~~~

如果检查未成功，先核对测试目录和 secrets.env；成功后启动交互测试：

~~~bash
mkdir -p "$GUARD_RUN_DIR/codex-workspace"
codex --version
codex --profile cpa-language-guard-test \
  --sandbox read-only \
  --cd "$GUARD_RUN_DIR/codex-workspace"
~~~

这个 profile 会叠加基础配置。环境变量 CPA_LANGUAGE_GUARD_API_KEY 必须在启动 Codex 的终端中存在。
无需把本地 Key 写入全局 Codex 登录文件。

## 10. 验收

在 Codex 中分别提交以下输入：

| 输入 | 预期 |
|---|---|
| Reply only in English. Say hello. | 放行，获得真实模型响应 |
| 请解释这个项目的作用 | HTTP 400，显示中文超标提示 |
| 中abcd | 恰好 20%，放行 |
| 中abc | 25%，拦截 |
| > 请解释这个项目的作用 | 整条输入显式豁免 |

输入占比按 Unicode Han / 全部 Unicode 文字字符计算。数字、空白、标点、emoji 不计入分母。
工具结果、已识别的自动上下文与历史用户消息不用于稀释本轮比例。
引用豁免仍需要来源标记。

当前完整超标提示：

~~~text
本轮指令因汉字占比超过 20% 被拦截。为避免模型降智，请自行将指令翻译成英语后重新发送，并明确要求模型必须用英语回复。
提示词示例：
Write all conversational replies in English, including explanations, questions that require my answer, and their answer options.

When creating or editing files, Chinese may be used where appropriate, including documentation, code comments, text displayed in frontend pages, etc. Follow the language requirements of the task and the repository for those files.
~~~

这是错误响应中的提示词示例。插件本身不把它注入放行请求，也不检查模型输出语言；
需要你将希望采用的回复要求写进实际指令。

如果要绕过 Codex 单独检查 HTTP 拦截，可从终端 B 执行：

~~~bash
curl --noproxy 127.0.0.1 -sS -i \
  -H "Authorization: Bearer $CPA_LANGUAGE_GUARD_API_KEY" \
  -H "Content-Type: application/json" \
  --data-binary @- \
  http://127.0.0.1:18316/v1/responses <<EOF
{
  "model": "$GUARD_MODEL",
  "input": [{
    "type": "message",
    "role": "user",
    "content": [{"type": "input_text", "text": "请解释这个项目"}],
    "internal_chat_message_metadata_passthrough": {
      "content_item_kinds": ["user.text"]
    }
  }]
}
EOF
~~~

预期 HTTP 400，error.code 为 chinese_ratio_exceeded。
手动构造的元数据只是检查接口；真实客户端来源标记由第 4 节模拟和 Codex 测试验证。
自动模拟中的 upstream_calls 是判断“拦截后零上游调用”的直接证据，
单看真实服务返回 HTTP 400 不足以证明这一点。

## 11. 迁移到你已有的实际配置

独立测试通过后，可以将插件复制到现有 CPA 的 plugins.dir，并在现有 plugins.configs 下增加：

~~~yaml
privacyfilter:
  enabled: true
  max_han_percent: 20
  allow_quoted_input: true
  skip_models: []
  skip_formats: []
~~~

将全局 plugins.enabled 设为 true，并保留原有管理设置、API Key、计费插件、认证目录和模型路由。
例如你已有的 auth-dir 可以继续使用 /data/collab/.cli-proxy-api，
前提是运行 CPA 的本机确实有该目录和可用凭证。

若启用 cpa-key-billing 等计费插件，新增客户端 Key 除了加入 CPA 的 api-keys，
还应按该插件的要求配置订阅、额度和模型权限；本地随机生成 Key 本身不创建计费记录。

如果需要保持你提供的对外监听行为，可在实际配置中使用：

~~~yaml
host: ""
port: 18316
remote-management:
  allow-remote: true
~~~

将这些字段合并进原节点，保留其中已有 secret-key 等字段。
其他机器访问时，Codex 的 base_url 应改成 CPA 机器的实际地址；
127.0.0.1 永远表示运行 Codex 的那台机器自身。

CPA/config.example.yaml 是模板；修改它不会自动修改正在运行的 config.yaml。
同一端口只能运行一个 CPA 实例。

## 12. 常见问题与重启

| 现象 | 检查项 |
|---|---|
| 安装器只打印 Selected asset variant 便退出 | 检查是否遇到 GitHub 匿名 API 的 HTTP 403 限流；先用 gh auth status 确认已登录，再用 gh api repos/router-for-me/CLIProxyAPI/releases/latest 获取发行信息，无需把令牌写入命令或配置 |
| KeyError: 'GUARD_RUN_DIR' 或提示缺少该变量 | 当前终端未导出路径；完整执行第 5 节代码块，或先恢复并 export 自定义路径 |
| Missing environment variable | profile 的 env_key 必须是变量名 CPA_LANGUAGE_GUARD_API_KEY，不能是 Key 值；在启动 Codex 的同一终端执行 source "$GUARD_RUN_DIR/secrets.env"，再用 test -n "$CPA_LANGUAGE_GUARD_API_KEY" 检查，不打印密钥 |
| Model metadata 或 Skill descriptions 警告 | 分别涉及模型元数据和技能说明，不是 Missing environment variable 的原因；先按上一项修复环境变量，不因此自动更换模型或禁用技能 |
| Go 版本不足或 C 编译器缺失 | go version、gcc --version；CPA 使用 CGO_ENABLED=1，插件使用 make build |
| 没有 plugin loaded 日志 | 两处 enabled、共享库是否安装到实际 plugins.dir、平台和架构是否匹配 |
| 中文完全不拦截 | 是否加载功能分支的插件；是否连接到了另一个 CPA；是否以 > 开头或命中 skip 配置 |
| 英文也返回来源识别错误 | Codex 版本、name = "OpenAI"、content_item_kinds = true、HTTP Responses 配置 |
| /v1/models 返回 401 | 是否使用客户端 Key，是否已 source secrets.env |
| 管理登录失败 | 使用原始管理密钥，不能使用客户端 Key 或 bcrypt 哈希 |
| 管理页面 404 | secret-key 是否设置、面板是否启用、首次 GitHub 资源下载是否成功 |
| 模型列表为空 | OAuth 登录是否成功，auth-dir 是否正确，上游模型映射是否填写 |
| 模型能列出但调用失败 | 上游凭证有效性、账号额度、实际模型权限及 CPA 出站代理 |
| 27 项之后暂时无输出 | 真实 Codex 测试串行运行并捕获输出；等待单项结束 |
| 模拟出现 TimeoutExpired | 查看当次进程/日志、Codex 版本和本机连接器启动情况；用不带 --codex 的命令区分接口与客户端问题 |
| 新终端中找不到 Key / Codex | 重新 source secrets.env，并恢复独立安装目录的 PATH |
| 端口占用 | 停止已有测试实例，或同步修改 CPA port、Codex base_url 和 curl 地址 |

结束手动测试时，在终端 A 按 Ctrl+C。
重新编译插件后，先停止测试 CPA，再复制新的共享库并重新启动：

~~~bash
cd "$GUARD_PLUGIN_DIR"
make build BUILD_DIR=dist
cp "$GUARD_PLUGIN_DIR/dist/privacyfilter.so" \
  "$GUARD_RUN_DIR/plugins/privacyfilter.so"

cd "$GUARD_RUN_DIR"
"$GUARD_CPA_DIR/dist/cli-proxy-api-language-guard" \
  --config "$GUARD_RUN_DIR/config.yaml" \
  --local-model
~~~

secrets.env、config.yaml、auth/ 都位于独立测试目录，保留它们即可下次继续使用。
本文不要求提交这些运行文件，也不要求更改日常 Codex 基础配置。

## 13. 参考资料

- [OpenAI Docs：Codex CLI 安装](https://learn.chatgpt.com/docs/codex/cli.md)
- [OpenAI Docs：独立配置 profile](https://learn.chatgpt.com/docs/config-file/config-advanced.md#profiles)
- [Go 官方安装说明](https://go.dev/doc/install)
- [Go 官方发行包及校验和](https://go.dev/dl/)
- [插件规则和配置说明](../README.zh-CN.md)

安装与 profile 用法已结合以上官方资料和本机 Codex 0.154.0 的帮助信息核对；
来源标记的保留行为以本仓库的真实客户端模拟结果为依据。
