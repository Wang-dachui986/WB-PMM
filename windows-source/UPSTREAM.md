# UPSTREAM.md — 上游源码基线与升级记录

> 本文件记录本项目所复用的两个开源上游的**固定提交**、获取方式、构建方式、工具链版本与本地补丁。
> 任何更换上游版本的行为都必须在此登记，并附接口差异与重跑验证的结论。

## 1. 固定源码基线

| 组件 | 仓库 | 固定提交 | 提交时间（UTC） | 核对结果 |
|---|---|---|---|---|
| API 网关 `wb2api` | https://github.com/Sliverkiss/workbuddy2api | `d1023f373065468d67791faf7e2235768af1e056` | 2026-09-21 01:52:34 | ✅ 与开发计划 §3.1 一致 |
| 社区面板 `wbgui` | https://github.com/linbeize/workbuddy2api-gui | `9413e70bc694a1e76c416d00cf70f5c9869bc3ca` | 2026-09-18 02:57:56 | ✅ 与开发计划 §3.1 一致 |

本地目录：`gateway/`（网关）、`panel/`（面板）。两者均为**普通目录，不是 Git 子模块**（开发计划 §6.3 允许）。

### 获取方式（可重复执行）

```bash
clone_pinned() {
  local dir="$1" url="$2" sha="$3"
  rm -rf "$dir"; mkdir -p "$dir"; cd "$dir"
  git init -q
  git remote add origin "$url"
  git fetch -q --depth 1 origin "$sha"
  git checkout -q FETCH_HEAD
  cd ..
}
clone_pinned gateway https://github.com/Sliverkiss/workbuddy2api.git d1023f373065468d67791faf7e2235768af1e056
clone_pinned panel   https://github.com/linbeize/workbuddy2api-gui.git 9413e70bc694a1e76c416d00cf70f5c9869bc3ca
```

`git rev-parse HEAD` 实测输出与上表提交号完全一致，未被上游最新分支污染。

### 许可证

两个仓库均为 **MIT**。`gateway/LICENSE` 与 `panel/LICENSE` 已在各自目录保留，第三方声明见 `THIRD_PARTY_NOTICES.md`。

## 2. 固定工具链版本

| 工具 | 实测版本 | 路径 / 说明 |
|---|---|---|
| macOS | 26.6.2（Build 25G83） | 目标架构 arm64（Apple Silicon） |
| Go | **go1.27.1 darwin/arm64** | `/Users/wangguiyang/.workbuddy-ai/binaries/go/go/bin/go` |
| Node.js | v22.22.2 | 托管运行时 |
| npm | 10.9.7 | 使用 `npm ci`，按 `package-lock.json` 锁定 |
| Xcode | 26.5（Build 17F42） | `xcode-select -p` = `/Applications/Xcode.app/Contents/Developer` |

上游 `go.mod` 声明：网关 `go 1.22.5`、面板 `go 1.22`。本机 Go 1.27.1 向下兼容，两仓库 `go build ./...` 均零错误。

### 前端依赖实际解析版本（`npm ci` 后 `npm ls` 实测）

| 包 | `package.json` 声明范围 | 锁文件实际解析 |
|---|---|---|
| react / react-dom | `^18.3.1` | 18.3.1 |
| react-router-dom | `^6.26.2` | 6.30.6 |
| typescript | `^5.6.3` | 5.9.3 |
| vite | `^5.4.9` | 5.4.21 |
| @vitejs/plugin-react | `^4.3.2` | 4.7.0 |

> **P0 结论**：开发计划 §5.1 列出的 `React 18.3.1 / TypeScript 5.6.3 / Vite 5.4.9` 是**声明范围**而非锁死版本；锁文件实际解析到更高的次版本（TS 5.9.3、Vite 5.4.21）。P0 已按 `package-lock.json` 固定，后续如需升级必须单独提交，不与 UI 改造混为一次大升级。

## 3. 构建命令（可重复）

完整脚本见 `scripts/build.sh`。核心步骤：

```bash
export GO=<go 路径>
OUT=<输出目录>

# 1) 前端生产构建（产物落到 Go embed 目录，见 panel/web/vite.config.ts 的 outDir）
cd panel/web && npm ci && npm run build

# 2) 面板后端（含 embed 前端资源）
cd panel && CGO_ENABLED=0 "$GO" build -trimpath -ldflags "-s -w -X main.version=<ver>" -o "$OUT/wbgui" ./cmd/server

# 3) 网关后端
cd gateway && CGO_ENABLED=0 "$GO" build -trimpath -ldflags "-s -w" -o "$OUT/wb2api" ./cmd/server
```

### P0 实测产物

| 产物 | 大小 | 架构 | sha256 |
|---|---:|---|---|
| `wb2api` | 8.4 MB | Mach-O 64-bit arm64 | `28ceb360a47bacccd1cbdf08023b378c6349df5ac8fc9965cbbb9bcf0c97a28e` |
| `wbgui` | 7.5 MB | Mach-O 64-bit arm64 | `25fa3dec3ecc9be1651a8043bc1026b59ca76e19dbdac604f6468e6a582fa52c` |

> **勘误（2026-09-21，WB-01 期间发现并更正）**
>
> 本表 `wb2api` 原先记录的哈希为 `db6196a3483be792d7e36511ba6d8f6f636ca683d9c7cb5cf98694b575f70c05`，
> 该值**无法从当前纯净源码树复现**，已作废。
>
> 复核过程与证据：
> 1. `git -C gateway status --porcelain` 为空 → 工作区与固定提交 `d1023f37…` 逐字节一致，无本地补丁（与本文档 §4 一致）。
> 2. 网关两个内嵌资源 `internal/prompt/defaultprompt.md`、`internal/upstream/model.json` 的 `git diff HEAD` 为空，内容哈希分别为 `2ab07c22a65f0a17…`、`2dc8f8f9e13021f9…`，自克隆以来 mtime 未变（均为 2026-09-21 11:06）。
> 3. 独立构建三次（两次手工 `go build`、一次经 `scripts/build.sh`），三次输出哈希**完全相同**，均为 `28ceb360a47baccc…`。
> 4. 同一次 `build.sh` 产出的 `wbgui` 哈希与旧记录一致（`25fa3dec…`），说明本工具链的构建是确定性的，哈希差异不是构建抖动。
>
> 结论：`28ceb360a47baccc…` 才是固定提交在纯净树上的可重复构建结果，本文档已按此更正。
> 后续 WB-02 起以本表值为准；任何偏离都视为源码树被改动，须先查清再继续。

前端产物：`internal/webui/dist/index.html`（467 B）、`assets/index-BEIjNl4j.js`（270 614 B）、`assets/index-DQPncp76.css`（11 282 B）。

`CGO_ENABLED=0` 在 macOS arm64 上验证通过，产物为纯静态可执行文件，无动态库依赖。

## 4. 本地补丁

### 4.1 网关 `gateway/`

**无本地补丁。** `git status --porcelain` 为空，工作区与固定提交 `d1023f373065468d67791faf7e2235768af1e056` 逐字节一致。

因此 `wb2api` 的哈希与 §3 基线表一致：`28ceb360a47baccc…`。**这是当前唯一可用于校验网关树是否被改动的锚点。**

### 4.2 面板 `panel/`（WB-02 引入）

基线提交 `9413e70bc694a1e76c416d00cf70f5c9869bc3ca`。补丁共 3 个文件：

| 文件 | 类型 | 行数 | 原因 |
|---|---|---:|---|
| `cmd/server/desktop.go` | 新增 | 356 | 桌面模式：由原生壳通过 stdin 管道驱动 |
| `cmd/server/desktop_test.go` | 新增 | 204 | 上述模式的单元测试 |
| `cmd/server/main.go` | 修改 | 195 | 增加 `--desktop` 标志；抽出 `runServer` / `buildHandler` 供两种模式共用 |

**新增了什么（不是上游已有功能）**

1. **`--desktop` 标志**：不读 `WBGUI_*` 环境变量、不读 `config.json`，改为从 stdin 读取一行 JSON 启动参数。
2. **启动握手**：监听成功后向 **stdout** 输出一行 `@@WBGUI-READY@@ {…}`（含服务名、协议版本、实际端口、实例 ID、PID，**不含任何密钥**）；普通日志一律走 stderr。
3. **动态端口**：`net.Listen("tcp", "127.0.0.1:0")` 由系统分配，避免固定端口冲突。
4. **父管道 EOF 退出**：stdin 读到 EOF 即优雅停机，保证父应用被强杀时不留孤儿服务。
5. **桌面模式配置硬覆盖**：`UpstreamAuthDir`、`UpstreamConfigFile`、`GatewayURL`、`UI.Username/Password` 一律以原生层下发值为准；`CredentialsFile=""`（禁用网页改密）、`DockerContainer=""`、`RefreshConfigOnLogin=false`、`ReadOnly=false`。
6. **高危操作显式开关**：`dangerous_ops` 必须由原生层在启动参数里显式下发 `true` 才生效；**未下发或环境变量 `WBGUI_DANGEROUS_OPS=true` 都不生效**（有对应单元测试）。

**为什么必须打这个补丁**：开发计划 §7.1 要求「面板不监听固定端口、内部口令不进命令行/URL/日志、父进程消失即退出」。上游面板三者皆无 —— 它只有固定 `:7863` + 配置文件 + `WBGUI_*` 环境变量三条通道，而后两条对桌面模式都是风险面（环境变量残留可改写监听地址与口令）。

**补丁产物**

| 产物 | sha256 |
|---|---|
| `wbgui`（含桌面模式 + 视觉层补丁） | `cba11557cf346b386d18713543c3907fb955a21dd9ff32d763cb3c1154e4a3d5` |

> 该值与 §3 基线表的 `25fa3dec…` 不同是**预期的** —— 基线表记录的是未打补丁的上游构建。
> 面板补丁含前端产物，因此**任何一次 `web/src/` 的改动都会改变这个哈希**。
> 重跑 `scripts/build.sh` 后若与上表不符，先确认是不是有意的前端改动，再查源码树。

### 4.3 面板 · 视觉层（macOS 原生重设计）

上游面板的默认外观是「深色 GitHub 风格后台」，与产品的「macOS 原生、浅色默认」定位不符。
本节记录视觉层的改动。**只动样式与图标，不动任何数据流、状态管理与接口调用。**

| 文件 | 改动 |
|---|---|
| `web/src/styles.css` | 全量重写（1075 行）。默认浅色 + `prefers-color-scheme` 深色 |
| `web/src/ui.tsx` | `Alert` 的 emoji 图标换成内联 SVG |
| `web/src/App.tsx` | 侧栏 emoji 图标换成内联 SVG（`ICONS` 表） |
| `web/src/pages/*.tsx`（9 个） | 移除装饰性 emoji（65 处），按钮回归纯文字 |

**设计标准来自用户提供的参考图**（`docs/design-reference.png`），色值是从该图**逐像素采样**得到的，
不是估的：

| 用途 | 参考图实测值 |
|---|---|
| 内容区背景 | `#ffffff`（纯白） |
| 次级按钮填充 | `#efefef`（平灰，无边框） |
| 表头背景 | `#f6f6f6` |
| 选中态 | `#e8f4ff`（浅蓝） |
| 危险浅底 | `#fcebec` |
| 主色 | `#007aff`（macOS systemBlue） |
| 危险色 | `#ff3b30`（macOS systemRed） |

三条贯穿全表的规则：**不用阴影、不用重边框**（靠留白与发丝线分隔）；
**按钮是平灰填充而非白底描边**（只有主操作才实心蓝）；**默认浅色**。

> **参考图本身不满足 4.5:1**（灰标签约 3.3:1、主色 4.02:1）。项目规则要求正文文字达标，
> 因此三处文字色做了同色系最小幅度加深：`#8e8e93→#6a6a6f`、`#007aff→#0066dd`、
> `#ff3b30→#d70015`。深色主题另有两处。完整说明与回退方式见 `docs/panel-visual.md` §3.2。

完整规范见 `docs/panel-visual.md`。校验：`node scripts/check-panel-contrast.js`（19 项 × 2 主题）。

> **这一层与 §4.2 的桌面模式补丁互相独立**：视觉层只改 `web/src/`，桌面模式只改 `cmd/server/`。
> 回退视觉层不会影响桌面模式，反之亦然。

### 4.4 补丁纪律

后续若需修改上游代码，必须遵守：
- 每个补丁在本节登记：文件、行号范围、原因、对应的开发计划条目（A01–A18 等）。
- 分别记录「上游已有功能」与「本次新增功能」，不得混写。
- 优先小补丁，不重写上游实现风格。
- **不改上游 `.gitignore`**：构建产生的临时文件（如 vite 的 `vite.config.ts.timestamp-*.mjs`）由 `scripts/build.sh` 在构建后清理，而不是往上游加忽略规则 —— 少一个补丁面。

## 5. 升级记录

| 日期 | 组件 | 从 → 到 | 接口差异 | 重跑的验证 | 结论 |
|---|---|---|---|---|---|
| 2026-09-21 | — | 初始基线 | — | P0 全量检查（见 `docs/p0-baseline.md`） | 基线可用 |
| 2026-09-21 | 面板 | 基线 `9413e70b…` + 本地补丁 | 新增 `--desktop` 模式（见 §4.2） | `go test ./cmd/server/`、`scripts/verify-desktop-handshake.sh`（19 项）、`scripts/verify-macos-runtime.sh`（30 项） | 补丁可用 |

> 禁止「直接拉取最新分支后沿用旧结论」。更换上游版本必须更新本表并重跑 P0 检查项。
> 更换上游版本还须重做 §4.2 的补丁移植，并重新核对 `desktop.go` 与上游 `config.go` 的字段耦合。
