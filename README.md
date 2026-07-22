# local-debug (`ldbg`)

在你的**笔记本**上运行一个 **Spring Boot** 微服务，使它表现为远程共享、启用 **Istio ambient
模式** 的 Kubernetes 集群中该服务的**真实实例**：接收该服务的真实流量、调用集群内真实依赖
（数据库、MQ、Redis、其他微服务），可在 IDE 中断点调试，并可由 **ClaudeCode** 驱动。

它把缓慢的老流程（构建 jar → 拷进 Pod → 重启 → 看日志 → 改 → 重来）替换为：
在笔记本上改代码 → 打断点 → 让流量打到服务 → 跟着真实流量单步调试。

`ldbg` 是对 [Telepresence](https://telepresence.io) 的薄封装（使用其免费、开源的
**global/TCP 全量拦截** = 完全接管），并补充了 Spring Boot 配置同步、**Istio ambient 处理**、
**离线（气隙）安装**路径，以及面向 AI 代理的全量 `--json` 输出。

> 配套中文文档：搭建与使用 [`docs/SETUP.zh-CN.md`](docs/SETUP.zh-CN.md)；
> Windows 11 → 远程气隙集群验证 Runbook [`docs/RUNBOOK.windows-remote.zh-CN.md`](docs/RUNBOOK.windows-remote.zh-CN.md)。

## 工作原理

```
集群内调用方 ──► <svc> Service ──►(全量拦截)──► 你的笔记本进程 ──►(telepresence 隧道)──► 真实依赖
                                       ▲                                      │
                                  IDE 断点调试                            真实 DB/MQ/…
```

- **入站接管** —— 集群内所有到目标 Service 的流量都被路由到你的本地进程（global/TCP 拦截；
  无需 header 路由、无需 waypoint、**无需 License** —— 适合气隙环境）。这是**完全接管**，拦截期间
  会影响共享集群中该服务的其他使用者（已接受的取舍）。
- **出站** —— 本地进程通过 `telepresence connect` 建立的隧道，用集群内 DNS 名称访问真实依赖。
- **配置同步** —— `ldbg` 把工作负载的集群环境变量（`env`/`envFrom`/ConfigMap/Secret）导出为
  env-file；Spring Boot 的 *relaxed binding* 让其覆盖 `application.yaml`，**无需改动应用代码**。

## Istio ambient：最关键的一点

在 ambient 下，被拦截的工作负载若仍留在网格中，其应用端口会被「黑洞」—— istio-cni 的 ztunnel
重定向与注入的 telepresence traffic-agent 同时争抢该端口，导致集群内调用方收到
**"connection reset by peer"**。`ldbg up` 会自动处理：把**被拦截的**工作负载排除出 ambient
（在其 Pod 模板上打 `istio.io/dataplane-mode=none`），依赖服务仍留在网格中；`ldbg down` 会自动
还原。（已在 minikube + Istio ambient 1.30 + Telepresence 2.29.0 上验证。）

## 快速开始

```bash
# 每个会话一次：启动集群网络守护进程（需要一次 sudo/管理员 提权）
telepresence connect

# 把服务接管到你的笔记本（同步 env + ambient 豁免 + 全量拦截）
ldbg up orders -n demo

# 在提示的本地端口上启动你的 Spring Boot 应用（IDE 调试、Maven/Gradle 或 java -jar）
#   IDE：  把运行配置的 EnvFile 指向 .ldbg/orders.env，在该端口 Run/Debug
#   Maven：ldbg up orders -n demo --run ./mvnw --run spring-boot:run
#   Gradle：set -a; . .ldbg/orders.env; set +a; ./gradlew bootRun

ldbg test orders -n demo     # 从集群内发请求 → 证明流量落到了你的笔记本

# 日志：历史查集群日志库，拦截期间的新日志查本地文件
ldbg logs query orders --since 4h -q Exception   # 关键字 + 时间窗（5m…7d 任意相对窗口）
ldbg logs local orders --level error             # 拦截期间的本地日志（堆栈完整返回）

ldbg down                    # 退出拦截、还原 ambient、断开连接、清理
```

## 命令

| 命令 | 作用 |
| --- | --- |
| `ldbg up <svc> -n <ns>` | 同步 env → 连接 → ambient 豁免 → 全量拦截（可加 `--run`） |
| `ldbg down` | 退出拦截、还原 ambient 豁免、断开连接、删除生成的文件 |
| `ldbg status [--json]` | telepresence/集群/拦截 状态 + 下一步提示 |
| `ldbg doctor [svc] -n <ns>` | 预检：客户端、集群、traffic-manager、ambient 评估 |
| `ldbg sync <svc> -n <ns>` | 仅从工作负载的集群 env 生成 env-file（默认注入本地日志落盘变量，`--no-local-log` 退出） |
| `ldbg test <svc> -n <ns>` | 集群内探测，显示是谁应答（证明接管生效） |
| `ldbg logs <svc> -n <ns>` | tail 工作负载的 Pod（含 traffic-agent）；`--manager`、`-f`、`--tail` |
| `ldbg logs query [svc]` | **查询集群日志库**（VictoriaLogs）：按 service/pod/容器/级别/关键字过滤，`--since 5m…7d` 或 `--from/--to`，含已删除 Pod 的历史 |
| `ldbg logs tail [svc]` | 日志库实时流（存储侧过滤，跨全部匹配 Pod） |
| `ldbg logs local [svc]` | **查询拦截期间的本地日志文件**（`.ldbg/logs/<svc>.log`），同一套过滤词汇，堆栈完整归并 |
| `ldbg logs stats <expr>` | LogsQL 聚合统计（如按服务/级别计数——修复后错误数归零即机器可读的验收信号） |
| `ldbg logs fields` / `values <字段>` | 字段/取值自省（agent 探索入口） |
| `ldbg intercept` / `leave` | 底层的全量拦截控制 |
| `ldbg bundle` | （联网机器）`docker save` traffic-manager 镜像为 tar 包 |
| `ldbg cluster install` | （气隙）导入镜像 + 用内嵌 chart 安装 traffic-manager |
| `ldbg cluster probe` | **验证经隧道/代理的集群桥接能否用**：分级检查 api / rbac / port-forward / 日志库，逐项 pass/fail + 提示 |
| `ldbg cluster tunnel` / `kubeconfig` | 为「kubectl 只在跳板机」的场景打印 `ssh -L` 接入命令 / 生成指向本地代理的最小 kubeconfig |

每个命令都支持 `--json` 与有意义的退出码。`sync` 和 `up` 支持
`--run-config intellij|vscode`，据 env-file 生成 IDE 运行配置（IntelliJ `.run/*.run.xml` 经
EnvFile 插件加载；VS Code `.vscode/launch.json` 用原生 `envFile`）。

## 离线 / 气隙安装

traffic-manager 与注入的 traffic-agent 是**同一个镜像**：`ghcr.io/telepresenceio/tel2:<版本>`。
Helm chart 已内嵌在 Telepresence 客户端中（无需联网，也无需 `helm` 二进制）。

```bash
# 在联网机器上
ldbg bundle --tp-version 2.29.0 --out tel2-bundle.tar

# 在气隙环境内（导入镜像 → 内嵌 chart 安装，pullPolicy=IfNotPresent）
ldbg cluster install --bundle tel2-bundle.tar --import-via registry --registry <内部仓库/路径>
#   minikube/kind/k3d 则用：  --import-via minikube
```

## 远程接入：kubectl 只在跳板机（Windows 11 笔记本无法直连集群 API）

场景：`kubectl` 只装在集群侧的**跳板机（bastion）**，Windows 11 笔记本上既没有 `kubectl` 也没有
kubeconfig。只要笔记本能 **SSH 到跳板机**，就用 `ssh -L` 隧道桥接（Windows 11 自带 OpenSSH，
`ssh` 开箱即用）。三个新子命令：`ldbg cluster probe`（逐项验证桥接）、`tunnel`（打印接入命令）、
`kubeconfig`（生成最小 kubeconfig）。

**① 先离线自检（无需任何集群，验证子命令可用）——PowerShell：**

```powershell
ldbg cluster probe --help                              # 看到 probe 用法即安装成功
ldbg cluster tunnel --bastion user@bastion             # 打印下面两套 ssh -L 接入命令
ldbg cluster kubeconfig --api http://127.0.0.1:8001    # 打印一份最小 kubeconfig
```

**② 日志（最稳：无需本地 kubectl、无需代理）——PowerShell：**

```powershell
# 跳板机（在一个 SSH 会话里，保持运行）：
#   kubectl port-forward -n logging svc/victorialogs 9428:9428 --address 127.0.0.1

# 笔记本：后台持有隧道（新开一个 ssh 窗口），再直接用 --vlogs-addr（无需 kubeconfig）
Start-Process ssh -ArgumentList '-N','-L','9428:127.0.0.1:9428','user@bastion'
ldbg cluster probe --vlogs-addr http://127.0.0.1:9428              # 期望：log-store ✓
ldbg logs query orders --vlogs-addr http://127.0.0.1:9428 --since 30m
```

**③ API / REST（`sync` 等 client-go 操作）——PowerShell：**

```powershell
# 跳板机： kubectl proxy --port=8001 --address 127.0.0.1
Start-Process ssh -ArgumentList '-N','-L','8001:127.0.0.1:8001','user@bastion'
ldbg cluster kubeconfig --api http://127.0.0.1:8001 --out proxy.kubeconfig
ldbg cluster probe --kubeconfig .\proxy.kubeconfig                # 看这座桥都能承载什么
ldbg --kubeconfig .\proxy.kubeconfig sync orders -n demo
```

`ldbg cluster probe` 逐项验证 **api / rbac / port-forward / 日志库**。实测：`kubectl proxy`
是 *upgrade-aware* 的，**能**承载 port-forward——但是否可用取决于 kubectl/集群版本及链路上是否还有
其它代理，所以用 probe 逐环境确认，别假设。**完整拦截**（`ldbg up`）需要 Pod 网段网络 +
traffic-manager，仅有 REST 代理不够，通常应**在跳板机上运行**；笔记本侧保留日志查询与只读操作。
关闭隧道：关掉那个 ssh 窗口，或 `Get-Process ssh | Stop-Process`。详见
[`docs/RUNBOOK.windows-remote.zh-CN.md`](docs/RUNBOOK.windows-remote.zh-CN.md) 阶段 J。

## ClaudeCode 用法

```bash
ldbg up orders -n demo --json      # 结构化结果，便于代理据此分支决策
ldbg status --json                 # connected / interceptActive / clusterReachable / hint
ldbg test orders -n demo --json    # 从集群内源头证明接管生效
ldbg logs query --since 30m -q Exception --json   # 拦截激活时 service 自动默认为被拦截服务
ldbg logs local --level error --json              # 拦截期间的本地日志（data.source=local-file）
ldbg logs stats "by (level) count() as c" --since 10m --json   # 修复后错误计数归零 = 验收信号
```

envelope 约定：`{ok, command, data, error, hint}`；`data.truncated=true` 时按 `hint`
收窄查询。日志库保留期 30 天，`--since` 支持 `5m/30m/1h/4h/8h/12h/24h/2d/7d/…` 任意相对窗口。
给业务服务仓库放一份 [`docs/CLAUDE-template.zh-CN.md`](docs/CLAUDE-template.zh-CN.md)，
任意会话中的 ClaudeCode 即可即插即用地跑通整个调试闭环。

分工：开发者负责 IDE 断点；ClaudeCode 负责 `up`/`test`/`status`/`logs query`/`logs local`、
读堆栈、改代码并迭代 —— 二者共享同一个 `ldbg` 会话。

## 从源码构建

`ldbg` 是纯 Go、零 CGO 的单文件二进制，可在 **Ubuntu** 与 **Windows 11** 上原生构建，也可在任一平台
交叉编译出三大平台产物。下面分别给出两套完整步骤。

### 0. 前置依赖（两个平台通用）

| 工具 | 版本 | 说明 |
| --- | --- | --- |
| **Go** | **1.22+**（`go.mod` 锁定 `go 1.22.0`） | 唯一的硬性依赖 |
| **Git** | 任意近期版本 | 拉取源码 |
| **make** | 可选 | 仅 `Makefile` 用；Windows 下可直接用 `go build`，无需 make |

构建只需联网拉取一次 Go module 依赖（`go.sum` 已锁定，`go build` 会自动下载到本地模块缓存）。
之后即可离线构建。本仓库无 CGO 依赖，故无需 C 编译器。

```bash
git clone https://github.com/hzeng10/local-debug.git
cd local-debug
```

---

### 1. 在 Ubuntu（24.04 LTS / 24.x）上构建

**安装 Go 1.22+。** Ubuntu 24.04 的 `apt` 自带 Go 1.22，可直接用；若需更新版本或其它 Ubuntu，
建议装官方 tarball。

```bash
# 方式 A：发行版自带（Ubuntu 24.04 即 Go 1.22，满足要求）
sudo apt update && sudo apt install -y golang-go git make

# 方式 B：官方 tarball（任意 Ubuntu，版本最新最可控）
curl -fsSL https://go.dev/dl/go1.22.2.linux-amd64.tar.gz -o /tmp/go.tgz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf /tmp/go.tgz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.profile && source ~/.profile

go version   # 应 ≥ go1.22
```

**构建（用 Makefile，最简单）：**

```bash
make build      # → 本机二进制 ./ldbg
make test       # 运行单元测试
make vet        # go vet 静态检查
make cross      # 交叉编译三平台 → dist/ldbg-{linux-amd64,windows-amd64.exe,darwin-arm64}
make clean      # 删除 ./ldbg 和 dist/
```

**或不依赖 make，直接用 go：**

```bash
go build -ldflags "-s -w -X github.com/hzeng10/local-debug/cmd.Version=$(git describe --tags --always --dirty)" -o ldbg .
./ldbg version
```

---

### 2. 在 Windows 11 上构建

Windows 默认没有 `make`，因此直接用 `go build`（下面给出 **PowerShell** 命令）。

**安装 Go 1.22+。** 任选其一：

```powershell
# 方式 A：winget（Windows 11 自带）
winget install --id GoLang.Go -e
winget install --id Git.Git -e

# 方式 B：到 https://go.dev/dl/ 下载 go1.22.x.windows-amd64.msi 双击安装
```

安装后**新开一个** PowerShell 窗口（让 PATH 生效），确认：

```powershell
go version    # 应 ≥ go1.22
git --version
```

**构建本机二进制（生成 `ldbg.exe`）：**

```powershell
git clone https://github.com/hzeng10/local-debug.git
cd local-debug

# 取版本号（可选；拿不到就用 0.0.0-dev）
$ver = (git describe --tags --always --dirty 2>$null); if (-not $ver) { $ver = "0.0.0-dev" }

go build -ldflags "-s -w -X github.com/hzeng10/local-debug/cmd.Version=$ver" -o ldbg.exe .
.\ldbg.exe version
```

**在 Windows 上交叉编译出三平台产物**（PowerShell 通过 `$env:` 设置 `GOOS/GOARCH`）：

```powershell
$ldflags = "-s -w -X github.com/hzeng10/local-debug/cmd.Version=$ver"

$env:GOOS="windows"; $env:GOARCH="amd64"; go build -ldflags $ldflags -o dist\ldbg-windows-amd64.exe .
$env:GOOS="linux";   $env:GOARCH="amd64"; go build -ldflags $ldflags -o dist\ldbg-linux-amd64 .
$env:GOOS="darwin";  $env:GOARCH="arm64"; go build -ldflags $ldflags -o dist\ldbg-darwin-arm64 .
Remove-Item Env:GOOS, Env:GOARCH      # 复原，避免影响后续命令
```

> 若用 `cmd.exe` 而非 PowerShell：用 `set GOOS=windows`、`set GOARCH=amd64`（每行一条），再 `go build ...`。
> 若装了 GNU Make（`winget install GnuWin32.Make` 或 Git Bash 里的 make），也可直接 `make build` / `make cross`。

---

### 3. 构建产物与验证

| 平台 | `make cross` / 交叉编译产物 | 运行环境 |
| --- | --- | --- |
| Linux | `dist/ldbg-linux-amd64` | Ubuntu 笔记本 |
| Windows 11 | `dist/ldbg-windows-amd64.exe` | Windows 笔记本 |
| macOS | `dist/ldbg-darwin-arm64` | Apple Silicon Mac |

版本号通过 ldflags 注入到 `cmd.Version`，构建后用 `ldbg version` 核对（同时显示锁定的
Telepresence 版本 **2.29.0**）。把二进制放到 `PATH` 下（Linux 可 `sudo install -m755 ldbg /usr/local/bin/`；
Windows 把 `ldbg.exe` 拷到某个已在 `PATH` 的目录）即可全局使用。

> **运行时依赖**：构建产物本身自包含；但**运行** `ldbg` 需要笔记本上已装 `telepresence`（2.29.0）
> 和能访问目标集群的 `kubectl`/kubeconfig。详见 [`docs/SETUP.zh-CN.md`](docs/SETUP.zh-CN.md)。

---

### 4. 集成测试（可选）

`test/integration/harness.sh` 会拉起 kind + Istio ambient + 示例应用，驱动真实的
`up`→`test`→`down` 流程，并断言 ambient 豁免、入站接管、出站、离线安装与干净收尾。它在 CI 中运行
（`.github/workflows/integration.yml`）；本地运行需要 `docker`、`kind`、`istioctl`、`kubectl`。
详见 [`test/integration/README.md`](test/integration/README.md)。

## 注意事项 / 范围

- **完全接管**：拦截期间，共享集群中所有到目标服务的流量都会打到你的笔记本（你的应用一旦停止或
  停在断点，该服务即不可用）。对气隙集群而言，免费的全量拦截是唯一可行模式（基于 header 的
  *personal* 拦截是付费的，且需要集群无法获取的 License）。
- **出站身份**：笔记本的出站调用经由被拦截 Pod 中的 traffic-agent 发出，因此依赖看到的源是该
  工作负载的 Pod IP —— 按调用方身份鉴权的 L4 策略可以通过。
- **暂不支持**：header/personal 拦截、同时本地运行多个服务、非 JVM 服务。
