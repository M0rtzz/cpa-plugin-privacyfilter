# 从零开始测试 CPA 中文输入检查插件

本文以 Ubuntu 24.04、Go 1.26、Codex CLI 0.154.0 为基线。
其他 Linux 发行版需要调整系统软件包安装命令；配置生成与校验使用 Python 3.11 或更高版本。
CPA 使用官方安装器下载的最新版发行包，插件使用 feat/chinese-input-guard 分支。
CPA 7.3.8 的原生插件接口已验证；安装器下载更新版本后，请先运行第 4 节的模拟测试。
手动测试端口为 **18316**。

文档中的命令使用 Bash。即使平时使用 Zsh 或 Fish，也请先在测试终端执行：

~~~bash
bash
~~~

下文按顺序完成“安装 CPA → 编译插件 → 自动模拟 → 准备密钥与配置 → 安装插件 → 配置真实上游 → 启动 CPA → 配置 Codex → 验收”。
自动模拟不需要真实模型凭证；连接真实模型时需要自己的可用账号或上游 API Key。
客户端 Key 使用安装器在本机生成的值，缺少时才生成；管理密钥沿用已有值或另行生成。

本方案使用 CPA 已有的原生插件接口，无需克隆或编译 CPA 源码。
安装目录统一为 **~/Programs/cliproxyapi**，实际配置为该目录下的 **config.yaml**。
Go 和 C 编译器仅用于构建插件。CPA 必须使用支持动态库插件的发行包。

## 1. 先分清三种凭证

| 凭证 | 如何获得 | 填在哪里 | 用途 |
|---|---|---|---|
| CPA 客户端 API Key | 复用安装器生成的 Key，缺少时本地随机生成 | CPA 的 api-keys、Codex 的 CPA_LANGUAGE_GUARD_API_KEY | Codex / curl 访问本地 CPA |
| CPA 管理密钥 | 复用已有管理密码，未设置时随机生成 | remote-management.secret-key | 登录管理页面、调用管理 API |
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

## 3. 安装 CPA 并编译插件

本节直接使用官方安装器下载 CPA 发行版，**不克隆、不编译 CPA 源码**；只克隆并编译插件。
在当前 Bash 终端定义路径：

~~~bash
export GUARD_ROOT="$HOME/Workspaces/Misc"
export GUARD_CPA_DIR="$HOME/Programs/cliproxyapi"
export GUARD_PLUGIN_DIR="$GUARD_ROOT/cpa-plugin-privacyfilter"
export GUARD_RUN_DIR="$GUARD_CPA_DIR"
mkdir -p "$GUARD_ROOT"
~~~

这些变量只对当前终端及其子进程生效；新开终端后需要重新执行 export。
Python 通过 os.environ 读取的变量必须经过 export，仅写 GUARD_RUN_DIR=... 不足以传给 Python。

### 3.1 使用安装器安装 CPA

安装器会创建或覆盖 `~/.config/systemd/user/cliproxyapi.service`，其 WorkingDirectory 指向安装目录。
**已有同名服务或 CPA 实例时，先确认它们是否就是本次要安装或升级的目标**；不同安装目录并不隔离这个服务名。
升级过程可能停止现有 CPA 进程，并在原服务运行时重启它；首次安装只创建服务文件，不自动启动服务。
本文稍后再配置并启动 CPA。

按你指定的方式下载脚本、修改安装目录并运行。请复制执行完整代码块；子 Shell 中任一步失败都会停止，
此时不要继续后续检查，更不能以目录里残留的旧二进制判断本次安装成功：

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
if [ "$(cat "$GUARD_CPA_DIR/asset-variant.txt")" != "default" ]; then
  printf '%s\n' '当前 CPA 发行包不支持插件，停止后续步骤。' >&2
  exit 1
fi
printf '%s\n' 'CPA 安装成功，版本为：'
cat "$GUARD_CPA_DIR/version.txt"
)
~~~

安装路径中的 `$HOME` 会在脚本运行时展开；不要改成 `INSTALL_DIR="~/Programs/cliproxyapi"`，双引号内的 `~` 不会展开。

Ubuntu 等 glibc 系统应看到 `Selected asset variant: default`，表示支持动态库插件。
`no-plugin` 是不支持插件的发行包，不能用于本方案；不要把文件名改成 default 来绕过检查。
安装器首次生成客户端 Key 时可能残留 `your-api-key-3`，后面的配置步骤会检查并替换占位 Key。

### 3.2 仅在 GitHub 匿名 API 限流时修复并重跑

如果安装器只打印 `Selected asset variant` 就退出，尚不能视为安装成功。
已遇到的原因是 GitHub 匿名 API 返回 HTTP 403 限流，当前脚本未能清晰显示该错误。
可复用已登录的 GitHub CLI 查询发行信息，不需要复制或手写登录令牌。

如果尚未安装或登录 gh，先按需执行以下命令；已有有效登录时跳过：

~~~bash
sudo apt-get install -y gh
gh auth login
~~~

下面仅修改刚下载的 `/tmp/cliproxyapi-installer`，把发行信息查询改为 `gh api`，然后重跑。
匹配不到已核对的脚本结构、登录无效、查询失败或安装失败时，整块立即停止：

~~~bash
(
set -e
gh auth status
gh api repos/router-for-me/CLIProxyAPI/releases/latest --jq '.tag_name'

/usr/bin/python3 - <<'PY'
from pathlib import Path

path = Path('/tmp/cliproxyapi-installer')
text = path.read_text()
old = '\n    release_info=$(fetch_release_info)'
new = '\n    release_info=$(gh api "repos/${REPO_OWNER}/${REPO_NAME}/releases/latest")'
if text.count(old) == 1 and new not in text:
    path.write_text(text.replace(old, new, 1))
    print('已改为使用 gh 获取发行信息；未写入登录令牌')
elif old not in text and text.count(new) == 1:
    print('发行信息查询已修复，继续使用当前脚本')
else:
    raise SystemExit('安装器结构已变化，停止修改，请先核对新版脚本')
PY

bash -n /tmp/cliproxyapi-installer
bash /tmp/cliproxyapi-installer

test -x "$GUARD_CPA_DIR/cli-proxy-api"
test -f "$GUARD_CPA_DIR/config.yaml"
if [ "$(cat "$GUARD_CPA_DIR/asset-variant.txt")" != "default" ]; then
  printf '%s\n' '当前 CPA 发行包不支持插件，停止后续步骤。' >&2
  exit 1
fi
printf '%s\n' 'CPA 安装成功，版本为：'
cat "$GUARD_CPA_DIR/version.txt"
)
~~~

修复后不要再运行 3.1 的 curl 下载命令，否则会覆盖临时修复；需要重新下载新版安装器时，重新核对其行为。
此修复仅替换 GitHub 发行信息的查询方式，不更换安装目录、下载目标或 CPA 功能。

### 3.3 获取插件功能分支

第一次下载插件仓库；已有仓库时跳过下载：

~~~bash
if [ ! -d "$GUARD_PLUGIN_DIR/.git" ]; then
  git clone git@github.com:M0rtzz/cpa-plugin-privacyfilter.git "$GUARD_PLUGIN_DIR"
fi
git -C "$GUARD_PLUGIN_DIR" status --short --branch
~~~

未配置 GitHub SSH Key 时，将 clone 地址改为 `https://github.com/M0rtzz/cpa-plugin-privacyfilter.git`。
目标目录若已存在但不是该仓库，不覆盖目录，应先确认正确位置。
切换分支前保留已有本地修改；工作区确认干净后执行：

~~~bash
(
set -e
if git -C "$GUARD_PLUGIN_DIR" show-ref --verify --quiet refs/heads/feat/chinese-input-guard; then
  git -C "$GUARD_PLUGIN_DIR" switch feat/chinese-input-guard
else
  git -C "$GUARD_PLUGIN_DIR" fetch origin feat/chinese-input-guard
  git -C "$GUARD_PLUGIN_DIR" switch --track origin/feat/chinese-input-guard
fi
)
~~~

### 3.4 下载依赖、检查并编译插件

使用第 2 节准备好的 Go 1.26 和 C 编译器；此处 CGO 是编译原生插件所需，CPA 已由安装器提供。
依次执行，失败时停止，不继续使用旧共享库：

~~~bash
(
set -e
cd "$GUARD_PLUGIN_DIR"
go version
gcc --version
go mod download
go test ./...
go vet ./...
make build BUILD_DIR=dist
test -f "$GUARD_PLUGIN_DIR/dist/privacyfilter.so"
)
~~~

后续模拟和安装使用这两个文件：

~~~text
~/Programs/cliproxyapi/cli-proxy-api
~/Workspaces/Misc/cpa-plugin-privacyfilter/dist/privacyfilter.so
~~~

## 4. 先运行自动模拟

~~~bash
cd "$GUARD_PLUGIN_DIR"
python3 scripts/simulate-cpa.py \
  --cpa "$GUARD_CPA_DIR/cli-proxy-api" \
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
模拟脚本使用临时配置和本地端口，不修改安装目录的 config.yaml。
手动测试使用安装目录的实际配置。

## 5. 准备密钥并修改安装目录中的 config.yaml

下面操作针对安装器生成的实际配置。新安装的默认结构如下；已有自定义 auth-dir 或 plugins.dir 时继续使用原路径：

~~~text
~/Programs/cliproxyapi/
├── cli-proxy-api
├── version.txt
├── <版本号>/config.example.yaml
├── config.yaml
├── config.yaml.before-language-guard-<随机后缀>.bak
├── secrets.env
└── plugins/
    └── privacyfilter.so
~~~

若之前通过插件商店安装过 privacyfilter，请先在仍运行的管理页面卸载旧版，再继续本节。
商店版本可能固定版本号并优先加载，直接复制新 .so 不一定生效；下面的脚本检测到旧商店元数据会停止。

先在 Bash 终端恢复路径。**这些命令会替换旧教程留下的目录变量**；如果你修改了安装器目标目录，
请同步修改下方所有 GUARD_CPA_DIR 赋值。Python 需要变量经过 export：

~~~bash
export GUARD_ROOT="$HOME/Workspaces/Misc"
export GUARD_CPA_DIR="$HOME/Programs/cliproxyapi"
export GUARD_PLUGIN_DIR="$GUARD_ROOT/cpa-plugin-privacyfilter"
export GUARD_RUN_DIR="$GUARD_CPA_DIR"
printf 'CPA 配置目录：%s\n' "$GUARD_RUN_DIR"
test -x "$GUARD_CPA_DIR/cli-proxy-api" && test -f "$GUARD_RUN_DIR/config.yaml"
~~~

最后一条命令未成功时，请先完成安装。修改配置或替换共享库前，停止这个安装实例。
安装器创建的用户服务默认不会在首次安装后自动启动；若已启动，执行：

~~~bash
if systemctl --user is-active --quiet cliproxyapi.service; then
  systemctl --user stop cliproxyapi.service
fi
~~~

前台启动的 CPA 请在对应终端按 Ctrl+C。如果已有同名服务指向其他安装目录，
先用 `systemctl --user cat cliproxyapi.service` 核对 WorkingDirectory 和 ExecStart。

本节将监听地址设为 127.0.0.1、端口设为 18316、关闭 TLS，并只允许本机管理访问，便于本地测试。
保留已有真实客户端 Key、认证目录、上游、路由和其他插件配置；仅清除模板中的 your-api-key-1/2/3。

如需设置 CPA 的出站代理，先执行（地址应为 CPA 所在机器实际可用的代理）：

~~~bash
export GUARD_UPSTREAM_PROXY_URL='http://127.0.0.1:49872'
~~~

明确禁用出站代理时设置空字符串；保持现有配置时取消覆盖变量，二选一：

~~~bash
export GUARD_UPSTREAM_PROXY_URL=''
~~~

~~~bash
unset GUARD_UPSTREAM_PROXY_URL
~~~

以下命令同时准备密钥和合并配置。会先备份 config.yaml，再原子替换；不打印密钥。
secrets.env 已存在时保留其内容，客户端 Key 若已不在配置中则停止，避免恢复已撤销的 Key。

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
cfg.update({"host": "127.0.0.1", "port": 18316})
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
print("客户端 Key 已保存到 secrets.env；privacyfilter 已启用，监听 127.0.0.1:18316")
if existing_management.startswith("$2"):
    print("已有管理密钥哈希保持不变，登录管理页面需使用原始管理密码")
PY
~~~

确认命令成功后加载环境变量：

~~~bash
source "$GUARD_RUN_DIR/secrets.env"
test -n "$CPA_LANGUAGE_GUARD_API_KEY" && printf '%s\n' '客户端 Key 已加载'
~~~

生成的管理密钥为 64 个 ASCII 字符。配置、备份和 secrets.env 权限均为 600。
原来已设置 bcrypt 管理哈希但没有原始密码时，脚本不会重置管理密码，secrets.env 中的管理变量可以为空；
请使用之前的管理密码登录。若以后另行更换管理密码，请同步更新本机保存的原始值。
CPA 首次加载明文管理密钥时会转为 bcrypt 并尝试回写 YAML；以 $2 开头的哈希不能直接用于登录。

PyYAML 会保留配置值，但不保留 YAML 注释。原文件注释可在备份中查看；
发行包自带的 config.example.yaml 通常在安装目录的版本子目录里，它不是运行配置。

## 6. 安装编译好的插件

确认第 5 节成功且 CPA 仍已停止。下面读取实际 plugins.dir，保留原有目录设置，并复制插件：

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
shutil.copy2(source, plugin_dir / "privacyfilter.so")
print("插件已安装到：" + str(plugin_dir / "privacyfilter.so"))
PY
~~~

默认位置为 ~/Programs/cliproxyapi/plugins/privacyfilter.so。前台启动和 systemd 服务均以安装目录作为工作目录，
因此相对 plugins.dir、auth-dir 的解析保持一致；以 ~ 开头的路径仍指向当前用户目录。

配置中的关键字段已由第 5 节合并：

~~~yaml
plugins:
  enabled: true
  dir: "plugins" # 已配置其他目录时保留原值
  configs:
    privacyfilter:
      enabled: true
      max_han_percent: 20
      allow_quoted_input: true
      skip_models: []
      skip_formats: []
~~~

其他插件配置会保留。若存在计费插件，客户端 Key 还需要对应的订阅、额度和模型权限，见第 11 节。

## 7. 配置真实上游：已有可用上游可跳过

### 7.1 方式 A：通过 CPA 登录 Codex OAuth

需要自己的、具有可用模型权限的 OpenAI 账号。
在当前终端中执行：

~~~bash
cd "$GUARD_RUN_DIR"
"$GUARD_CPA_DIR/cli-proxy-api" \
  --config "$GUARD_RUN_DIR/config.yaml" \
  --codex-login \
  --local-model
~~~

按终端提示完成浏览器登录。OAuth 回调端口为 1455；
如果该端口被占用，请先停止占用该端口的程序，或使用下面的设备登录。

无图形界面或浏览器回调不方便时，可改用设备登录：

~~~bash
cd "$GUARD_RUN_DIR"
"$GUARD_CPA_DIR/cli-proxy-api" \
  --config "$GUARD_RUN_DIR/config.yaml" \
  --codex-device-login \
  --no-browser \
  --local-model
~~~

打开终端给出的设备登录页面，在有效期内输入显示的设备码并授权。
两种登录方式选一种即可。

成功后应看到 Authentication saved to ... 及登录成功信息，
凭证保存在 config.yaml 的 auth-dir 指定目录；安装器模板通常使用 ~/.cli-proxy-api。
请以实际配置和登录输出为准，不要只根据命令退出码判断成功。
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

若上游本身是已有 CPA，则填写它的地址、有效客户端 Key 和它实际提供的模型名称。
本次测试服务在 18316；上游地址不能指回本次测试服务自身。
这是有意选择的 API 接入方式，OAuth 方式不需要这一段配置。

## 8. 启动 CPA 并检查管理页面

在当前终端（终端 A）前台运行：

~~~bash
cd "$GUARD_RUN_DIR"
"$GUARD_CPA_DIR/cli-proxy-api" \
  --config "$GUARD_RUN_DIR/config.yaml" \
  --local-model
~~~

保持该进程运行。另开终端 B，执行 Bash 并恢复路径和客户端 Key：

~~~bash
bash
export GUARD_ROOT="$HOME/Workspaces/Misc"
export GUARD_CPA_DIR="$HOME/Programs/cliproxyapi"
export GUARD_PLUGIN_DIR="$GUARD_ROOT/cpa-plugin-privacyfilter"
export GUARD_RUN_DIR="$GUARD_CPA_DIR"
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

新生成的管理密钥使用 secrets.env 中 CPA_LANGUAGE_GUARD_MANAGEMENT_KEY 的**原始值**登录。
沿用已有 bcrypt 管理哈希时，使用你原来的管理密码；管理变量为空不影响客户端请求。
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

## 11. 与已有配置、其他插件及远程访问配合

本文已直接修改安装目录的实际 config.yaml，无需再从源码测试目录迁移。
若要给另一台 CPA 安装，复制插件到该实例的 plugins.dir，并在现有 plugins.configs 下增加：

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

config.example.yaml 是模板；修改它不会自动修改安装目录的 config.yaml。
同一端口只能运行一个 CPA 实例。

## 12. 常见问题与重启

| 现象 | 检查项 |
|---|---|
| 安装器只打印 Selected asset variant 便退出 | 按第 3 节的 GitHub API 403 处理步骤，用已登录的 gh 获取发行信息后重跑安装器 |
| KeyError: 'GUARD_RUN_DIR' 或提示缺少该变量 | 当前终端未导出路径；完整执行第 5 节代码块，或先恢复并 export 自定义路径 |
| Missing environment variable | profile 的 env_key 必须是变量名 CPA_LANGUAGE_GUARD_API_KEY，不能是 Key 值；在启动 Codex 的同一终端执行 source "$GUARD_RUN_DIR/secrets.env"，再用 test -n "$CPA_LANGUAGE_GUARD_API_KEY" 检查，不打印密钥 |
| Model metadata 或 Skill descriptions 警告 | 分别涉及模型元数据和技能说明，不是 Missing environment variable 的原因；先按上一项修复环境变量，不因此自动更换模型或禁用技能 |
| Go 版本不足或 C 编译器缺失 | go version、gcc --version；这里只编译插件，CPA 使用安装器下载的支持原生插件的 default 包 |
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
重新编译插件后，先停止前台进程或用户服务，再按第 6 节复制共享库：

~~~bash
cd "$GUARD_PLUGIN_DIR"
make build BUILD_DIR=dist
~~~

执行第 6 节的安装代码块后，前台启动：

~~~bash
cd "$GUARD_RUN_DIR"
"$GUARD_CPA_DIR/cli-proxy-api" \
  --config "$GUARD_RUN_DIR/config.yaml" \
  --local-model
~~~

也可以改为安装器创建的用户服务。先结束前台实例，核对服务路径，再启动：

~~~bash
systemctl --user cat cliproxyapi.service
systemctl --user daemon-reload
systemctl --user enable --now cliproxyapi.service
systemctl --user status cliproxyapi.service --no-pager
~~~

服务中的 WorkingDirectory 应为 ~/Programs/cliproxyapi 展开后的绝对路径，
ExecStart 应指向该目录下的 cli-proxy-api。该服务默认启动时会读取安装目录的 config.yaml。
以后通过 systemd 运行时，替换插件前先 stop，复制后再 start，避免覆盖运行中加载的共享库。

secrets.env 和 config.yaml 均位于 ~/Programs/cliproxyapi，认证文件位于实际 auth-dir。
保留这些文件即可下次继续使用。升级 CPA 可重新执行第 3 节的安装器流程，
随后重新运行第 4 节模拟测试确认插件兼容性；本机对临时安装脚本的修改在重新下载后需要重新应用。

## 13. 参考资料

- [CPA 官方安装器](https://github.com/router-for-me/cliproxyapi-installer)
- [OpenAI Docs：Codex CLI 安装](https://learn.chatgpt.com/docs/codex/cli.md)
- [OpenAI Docs：独立配置 profile](https://learn.chatgpt.com/docs/config-file/config-advanced.md#profiles)
- [Go 官方安装说明](https://go.dev/doc/install)
- [Go 官方发行包及校验和](https://go.dev/dl/)
- [插件规则和配置说明](../README.zh-CN.md)

安装与 profile 用法已结合以上官方资料和本机 Codex 0.154.0 的帮助信息核对；
来源标记的保留行为以本仓库的真实客户端模拟结果为依据。
