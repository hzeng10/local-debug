# 开发者指南：用 `ldbg` 在本地调试远程集群里的服务（简体中文）

> **本文是"照抄就能跑"的分步操作手册**，Linux / macOS 与 Windows 11 双平台并列给出命令。
> 全文用同一个例子贯穿：
>
> | 项目 | 值 |
> | --- | --- |
> | 服务名（k8s Service） | `gde-adapter` |
> | 命名空间 | `kube-system` |
> | 工作负载（Deployment） | `gde-adapter` |
> | Service 端口（示例） | `8080` —— **请按你集群的实际端口替换** |
>
> 相关文档：安装与离线部署见 [`SETUP.zh-CN.md`](SETUP.zh-CN.md)；
> Windows→气隙集群的验收门见 [`RUNBOOK.windows-remote.zh-CN.md`](RUNBOOK.windows-remote.zh-CN.md)；
> 项目总览见 [`../README.md`](../README.md)。

**目录**

- [§0 先读这一段](#0-先读这一段30-秒)
- [§1 前提清单](#1-前提清单双平台)
- [§2 一次性准备](#2-一次性准备)
- [§3 一次调试会话：七步](#3-一次调试会话七步)
- [§4 Linux ↔ Windows 差异速查](#4-linux--windows-差异速查)
- [§5 `kube-system` 专属注意事项](#5-kube-system-专属注意事项)
- [§6 故障排查](#6-故障排查)
- [§7 一页速查](#7-一页速查可直接抄)
- [§8 让 Claude Code 驱动这套流程](#8-让-claude-code-驱动这套流程)

---

## §0 先读这一段（30 秒）

`ldbg` 把远程集群里 `gde-adapter` 的**真实流量**接到你笔记本上的进程，同时你的进程能用集群内
DNS 访问真实依赖（DB / MQ / Redis / 其他微服务）：

```
集群内调用方 ──► gde-adapter Service ──►(全量拦截)──► 你笔记本上的进程 ──►(telepresence 隧道)──► 真实依赖
                                              ▲                                    │
                                        IDE 断点调试                          真实 DB/MQ/…
```

三件必须先知道的事：

1. **⚠️ 这是"全量接管"，不是分流。** 拦截期间集群里**所有**对 `gde-adapter` 的调用都会打到你的
   笔记本。你的本地进程停止、崩溃、或**停在断点上**时，该服务对整个集群就是不可用的。
   （免费版 Telepresence 只有全量拦截；按 header 分流的 personal 拦截需要付费 License。）
2. **`kube-system` 的影响面比业务命名空间大。** 该命名空间里的组件通常被全集群依赖。
   强烈建议**先在你有权限的测试命名空间里演练一遍**（参考
   [RUNBOOK §5 一次性测试服务](RUNBOOK.windows-remote.zh-CN.md#5-先做一次性测试服务演练强烈建议)），
   再对 `gde-adapter` 操作，并提前和相关同事打招呼。
3. **必须收尾。** 调完一定执行 `ldbg down`：它会退出拦截、卸载注入的 traffic-agent、还原
   `ldbg` 做过的集群改动、断开连接并清理生成文件。

---

## §1 前提清单（双平台）

### 1.1 工具

| 工具 | 版本要求 | 用途 | 校验命令（Linux / macOS） | 校验命令（Windows PowerShell） |
| --- | --- | --- | --- | --- |
| `ldbg` | 最新 | 调试编排 CLI | `ldbg version` | `ldbg.exe version` |
| `telepresence` | **2.29.0**，须与集群里的 traffic-manager 一致 | 隧道 + 拦截 | `telepresence version` | `telepresence version` |
| `kubectl` | 与集群相近 | 排障 / 手工验证 | `kubectl version --client` | `kubectl version --client` |
| JDK | 服务所需（如 21） | 本地跑 Spring Boot | `java -version` | `java -version` |
| Maven / Gradle | 用仓库自带 wrapper | 构建 / 启动 | `./mvnw -v` / `./gradlew -v` | `.\mvnw.cmd -v` / `.\gradlew.bat -v` |
| IDE | IntelliJ / VS Code | 断点调试 | IntelliJ 需装 **EnvFile** 插件（`net.ashald.envfile`） | 同左 |

### 1.2 kubeconfig 与集群访问

kubeconfig 放在默认位置即可（`~/.kube/config`；Windows `%USERPROFILE%\.kube\config`），
或用环境变量指定路径。通过判据是**能列出目标命名空间的 Pod**：

```bash
# Linux / macOS
kubectl -n kube-system get pods | head
kubectl -n kube-system get deploy,svc gde-adapter
```

```powershell
# Windows 11 (PowerShell)
kubectl -n kube-system get pods | Select-Object -First 10
kubectl -n kube-system get deploy,svc gde-adapter
```

`ldbg` 用的是同一套解析规则：`--kubeconfig` > `$KUBECONFIG` > `~/.kube/config`；
`--context` 可指定非当前 context。

> 如果你的笔记本**没有** kubeconfig、只能 SSH 到集群节点，先用
> `ldbg cluster fetch-kubeconfig --ssh <user>@<节点IP>` 把凭证拉下来并改写为走 `ssh -L` 隧道，
> 详见 [RUNBOOK 阶段 J](RUNBOOK.windows-remote.zh-CN.md#阶段-j--远程-kubectl--ssh-跳板机接入可选笔记本无法直连集群-api-时)。
> 本文假设你已经能直接 `kubectl` 到集群。

### 1.3 RBAC 最低权限

`kube-system` 通常权限更严，先确认这几项（用 `kubectl auth can-i` 自查）：

| 操作 | 谁需要 | 自查命令 |
| --- | --- | --- |
| get/list `services`、`deployments`、`pods` | `doctor` / `sync` / `up` / `test` 解析目标 | `kubectl auth can-i get deploy -n kube-system` |
| get `configmaps`、`secrets` | `sync` 要解析 `env` / `envFrom` 引用 | `kubectl auth can-i get secret -n kube-system` |
| **patch `deployments`** | 仅当需要 ambient 豁免（见 §3 第 1 步） | `kubectl auth can-i patch deploy -n kube-system` |
| **create `pods`** | 仅 `ldbg test`（在集群内起临时 curl Pod） | `kubectl auth can-i create pod -n kube-system` |
| get/list（`ambassador`、`logging` 命名空间） | `doctor` 检查 traffic-manager / 日志栈 | `kubectl auth can-i get pods -n ambassador` |

### 1.4 一次性提权

`telepresence` 的 root 网络守护进程需要提权：**Linux 用 `sudo`，Windows 弹 UAC**。
每个会话只需一次，之后 `ldbg` / `telepresence` 命令都不再需要。

---

## §2 一次性准备

只做一次，之后每次调试直接跳到 §3。

1. **装 `ldbg` 与 `telepresence` 2.29.0** —— 见 [SETUP §3](SETUP.zh-CN.md#3-笔记本侧设置一次性)
   与 [README「从源码构建」](../README.md#从源码构建)。校验：

   ```bash
   ldbg version          # 显示 ldbg 版本 + 锁定的 Telepresence 版本
   telepresence version  # OSS Client : v2.29.0
   ```

2. **确认集群里已装 traffic-manager**（一般由集群管理员做过一次）：

   ```bash
   kubectl -n ambassador get deploy traffic-manager     # 期望 READY 1/1
   ```

   没有的话，气隙集群的离线安装步骤见 [SETUP §4](SETUP.zh-CN.md#4-集群侧离线安装-traffic-manager一次性)。

3. **在 `gde-adapter` 的服务仓库根目录使用 `ldbg`** —— 生成的 `.ldbg/`（env-file、本地日志）和
   `.run/`（IDE 运行配置）都落在当前目录。把它们加进服务仓库的 `.gitignore`：

   ```gitignore
   .ldbg/
   .run/
   *.env
   ```

   > env-file 里含集群 Secret 的**明文值**，`ldbg` 以 `0600` 写出、在终端输出里打码，
   > 但**绝不要提交到 git**。

---

## §3 一次调试会话：七步

每一步的格式是：**做什么 → 命令 → 期望输出 → 出问题怎么办**。

### 第 1 步：预检

```bash
# Linux / macOS
ldbg doctor gde-adapter -n kube-system
```

```powershell
# Windows 11
ldbg.exe doctor gde-adapter -n kube-system
```

**期望输出**（`kube-system` 通常**不在** Istio ambient 网格里，所以是这个分支）：

```
✓ telepresence-client    found v2.29.0
✓ cluster-reachable      kubernetes v1.31.4
✓ traffic-manager        connected; manager installed
✓ ambient-namespace      namespace "kube-system" is not ambient
✓ ambient-workload       Deployment/gde-adapter not in ambient; no opt-out needed
! log-store              VictoriaLogs unreachable — 'ldbg logs query' won't work …
overall: ok
```

怎么读这份报告：

- `✓ pass` / `! warn` / `✗ fail`；**只有 `fail` 会让 `doctor` 以退出码 1 结束**，`warn` 不阻塞。
- **`ambient-workload` 分两种情况**：
  - `not in ambient; no opt-out needed` → 第 3 步的 `ldbg up` **不会改动**你的工作负载。
  - `! Deployment/gde-adapter is in ambient; 'ldbg up' will apply dataplane-mode=none`
    → 命名空间被打了 `istio.io/dataplane-mode=ambient`。这时 `up` 必须给该工作负载打
    `istio.io/dataplane-mode=none` 豁免（否则 istio-cni 的 ztunnel 重定向与注入的 traffic-agent
    抢同一个端口，集群调用方会收到 **connection reset**）。这个改动会触发一次滚动重启，
    并由 `ldbg down` 自动还原。
- `log-store` / `log-collection` 只是 **warn**：日志栈是可选设施，没有它照样能拦截调试（见 §5）。

**出问题**：`✗ cluster-reachable` → 检查 kubeconfig / context（`kubectl config current-context`）；
`✗ target-workload` → 服务名或命名空间写错，用 `kubectl -n kube-system get svc gde-adapter` 确认。

> 🤖 Claude Code：`ldbg doctor gde-adapter -n kube-system --json`（详见 [§8](#8-让-claude-code-驱动这套流程)）

### 第 2 步：连接集群（每个会话一次，需要提权）

```bash
# Linux / macOS —— 会提示输入 sudo 密码
telepresence connect -n kube-system
telepresence status
```

```powershell
# Windows 11 —— 会弹 UAC
telepresence connect -n kube-system
telepresence status
```

**期望**：`Status: Connected`，且 `Namespace: kube-system`。

> **务必带 `-n kube-system`**。`ldbg down` 收尾时用 `telepresence uninstall <svc>` 移除注入的
> traffic-agent，而该命令**没有 `-n` 参数**、按当前连接的命名空间解析。连接时命名空间不对，
> agent 就删不掉。

**出问题**：命令卡住或失败，几乎都是提权问题 —— 必须在**你自己的交互式终端**里运行
（能输 sudo 密码 / 能点 UAC），不要在无 TTY 的脚本或 CI 里跑。

> 🤖 Claude Code：**这一步必须人工执行一次**，代理无法代劳提权。

### 第 3 步：接管服务

```bash
# Linux / macOS
ldbg up gde-adapter -n kube-system
```

```powershell
# Windows 11
ldbg.exe up gde-adapter -n kube-system
```

**期望输出**：

```
✓ synced 23 env vars → .ldbg/gde-adapter.env
✓ connected
✓ global intercept active — cluster traffic to "gde-adapter" now routes to your laptop

Ready. Start your Spring Boot app on local port 8080, then send traffic through the cluster.

  env-file : .ldbg/gde-adapter.env   (Telepresence/IDE EnvFile format)
  port     : 8080:8080   (local:remote)
  local log: .ldbg/logs/gde-adapter.log   (query with 'ldbg logs local gde-adapter')

Next:
  • IntelliJ/VS Code: set the run config EnvFile to .ldbg/gde-adapter.env, run/debug on port 8080
  • or:  set -a; . .ldbg/gde-adapter.env; set +a; ./gradlew bootRun
  • verify:  ldbg test
  • stop:    ldbg down
```

> 上面是**非 ambient**（`kube-system` 的常见情况）的输出。若该命名空间是 ambient，中间会**多出两行**：
> ```
> … ambient: excluding "gde-adapter" from ztunnel redirection (istio.io/dataplane-mode=none) so the intercept isn't black-holed
> ✓ ambient opt-out applied (reverted by 'ldbg down')
> ```

`ldbg up` 依次做了四件事：

1. **同步集群配置** → 解析工作负载的 `env` / `envFrom` / ConfigMap / Secret，写出
   `.ldbg/gde-adapter.env`（`0600`）。Spring Boot 的 *relaxed binding* 会让这些环境变量覆盖
   `application.yaml`，**不用改一行代码**。同时注入合成变量 `LOGGING_FILE_NAME`，让应用把日志
   落到 `.ldbg/logs/gde-adapter.log`（供 `ldbg logs local` 查询；`--no-local-log` 可关闭）。
2. **确保 telepresence 已连接**（第 2 步已连好，这里是幂等检查）。
3. **ambient 处理**：需要时打 `istio.io/dataplane-mode=none` 并等待 rollout 就绪；`down` 自动还原。
4. **建立 global（TCP）全量拦截**。

**两个必须知道的细节：**

- **本地端口从哪来**：默认取该 Service 的**第一个端口**，映射成 `<port>:<port>`。
  例：`gde-adapter` Service 端口是 `8080` → 你的应用要监听本地 `8080`。
  想换本地端口（端口被占用，或 Service 端口 <1024 而 Linux 非 root 不能绑定）：

  ```bash
  ldbg up gde-adapter -n kube-system --port 18080:8080    # local:remote
  ```

- **名字是怎么解析的**：先按 **Service** 找（`gde-adapter`），再按 Service 的 selector 找到背后的
  Deployment/StatefulSet；找不到同名 Service 才退回直接找同名
  Deployment/StatefulSet/DaemonSet。`gde-adapter` 的 Service 与 Deployment 同名，所以直接写服务名即可。

**其它常用开关**：`--env-out <路径>`（换 env-file 位置）、`--keep-ambient`（不做 ambient 豁免，
仅用于排障）、`--no-local-log`、`--run-config intellij|vscode`（顺手生成 IDE 运行配置）。

**出问题**：报 `no Service/Deployment/... named "gde-adapter"` → 命名空间或名字错；
报 RBAC 相关错误 → 对照 §1.3 自查权限；报另一个拦截已存在 → 先 `ldbg down`。

> 🤖 Claude Code：`ldbg up gde-adapter -n kube-system --json`

### 第 4 步：在笔记本上启动你的应用

三选一，都要让应用监听第 3 步给出的**本地端口**（示例 8080）。

**A. IDE 调试（推荐，能打断点）**

- **IntelliJ IDEA**：装 **EnvFile** 插件 → Run/Debug Configurations → EnvFile 页签 → 勾选
  Enable EnvFile → 添加 `.ldbg/gde-adapter.env`（Windows：`.ldbg\gde-adapter.env`）→ **Debug**。
- **VS Code**：`.vscode/launch.json` 里用原生 `envFile`：

  ```json
  { "type": "java", "name": "gde-adapter (ldbg)", "request": "launch",
    "mainClass": "com.example.gdeadapter.Application",
    "envFile": "${workspaceFolder}/.ldbg/gde-adapter.env" }
  ```

- 也可以让 `ldbg` 直接生成：`ldbg up gde-adapter -n kube-system --run-config intellij`
  （或 `--run-config vscode`）。

**B. 命令行手动加载 env 再启动**

```bash
# Linux / macOS
set -a; . .ldbg/gde-adapter.env; set +a
./mvnw spring-boot:run          # 或 ./gradlew bootRun / java -jar target/app.jar
```

```powershell
# Windows 11 —— PowerShell 没有 `set -a`，逐行注入
Get-Content .ldbg\gde-adapter.env | Where-Object { $_ -and ($_ -notmatch '^\s*#') } | ForEach-Object {
  $k,$v = $_ -split '=',2; [System.Environment]::SetEnvironmentVariable($k,$v)
}
.\mvnw.cmd spring-boot:run      # 或 .\gradlew.bat bootRun
```

**C. 让 `ldbg` 直接启动（stdout/stderr 自动落盘，便于 `logs local`）**

```bash
# Linux / macOS
ldbg up gde-adapter -n kube-system --run ./mvnw --run spring-boot:run
```

```powershell
# Windows 11
ldbg.exe up gde-adapter -n kube-system --run .\mvnw.cmd --run spring-boot:run
```

> `--run` 可重复，按顺序拼成一条命令（第一个是可执行文件，后面是参数）。

### 第 5 步：验证接管确实生效

```bash
# Linux / macOS
ldbg status
ldbg test gde-adapter -n kube-system
```

```powershell
# Windows 11
ldbg.exe status
ldbg.exe test gde-adapter -n kube-system
```

`ldbg status` **期望输出**：

```
telepresence: found=true connected=true
cluster:      reachable=true version=v1.31.4 context=<你的 context>
intercepts:   active=true [gde-adapter]
hint:         intercept active; run your app locally and use 'ldbg test'
```

`ldbg test` 会在**集群内部**起一个临时 curl Pod 去请求
`http://gde-adapter.kube-system.svc.cluster.local:8080/`，所以请求来自真实的集群调用方视角：

```
in-cluster GET http://gde-adapter.kube-system.svc.cluster.local:8080/
  succeeded=true exit=0
  response: <你本地进程返回的内容>
(if this is your local process's response, the global intercept works)
```

常用参数：`--path /actuator/health`（换请求路径）、`--port 8080`（Service 端口推导不对时覆盖）。

> **`kube-system` 提醒**：该命名空间常有 Pod Security Admission / 准入控制器限制，
> `ldbg test` 可能因为**建不了临时 Pod** 而失败。这**不代表拦截没生效**。替代验证：
>
> ```bash
> # 在你有权限的命名空间里发起（把 <你的命名空间> 换掉）
> kubectl -n <你的命名空间> run t --image=curlimages/curl --restart=Never --rm -i -- \
>   curl -s http://gde-adapter.kube-system.svc.cluster.local:8080/
>
> # 或者从一个已有的 Pod 里发
> kubectl -n <你的命名空间> exec -it <某个pod> -- curl -s http://gde-adapter.kube-system.svc.cluster.local:8080/
> ```
>
> 最直接的判据：**你本地进程的控制台/断点收到了这个请求**。

> 🤖 Claude Code：`ldbg test gde-adapter -n kube-system --json`，看 `data.succeeded`

### 第 6 步：调试闭环（断点 + 日志）

- 在 IDE 里打断点 → 让集群流量进来 → 单步调试。
- 改完代码 **只需重启本地进程**，`ldbg up` 不用重跑（拦截仍然有效），然后 `ldbg test` 复验。

**日志有两个来源，一套过滤词汇：**

| 场景 | 命令 |
| --- | --- |
| 集群日志库的历史日志（含已删除 Pod，保留 30 天） | `ldbg logs query gde-adapter --since 4h -q Exception` |
| **拦截期间**本地进程的新日志（堆栈完整归并） | `ldbg logs local gde-adapter --level error` |
| 日志库实时流 | `ldbg logs tail gde-adapter -q error` |
| 聚合统计（修复后错误归零 = 验收信号） | `ldbg logs stats "by (level) count() as c" --since 10m` |
| 有哪些字段/取值可查 | `ldbg logs fields` / `ldbg logs values service` |
| 直接 tail 集群 Pod（含 traffic-agent，排查 agent 本身） | `ldbg logs gde-adapter -n kube-system -f` |

- 时间窗：`--since 5m/30m/1h/4h/12h/1d/2d/7d…`，或 `--from/--to`（RFC3339）。
- 过滤：`-q 关键字`（多词 AND，`"带空格的短语"` 保持整体，`-i` 忽略大小写）、`--level`、
  `--pod`、`-c <容器>`。`logs query` 的 `-q` 含 `:` 或 `|` 时按原生 LogsQL 透传（如 `-q trace_id:abc`）。
- **拦截期间集群侧对 `gde-adapter` 是空窗**（流量都在你本地了）→ 这段时间用 `logs local`。
- 日志库地址解析顺序：`--vlogs-addr` / `$VLOGS_ADDR` → telepresence 隧道直连 → 自动 port-forward。

> 🤖 Claude Code：`ldbg logs query gde-adapter --since 4h -q Exception --json`、
> `ldbg logs local --level error --json`

### 第 7 步：收尾（必须做）

```bash
# Linux / macOS
ldbg down
```

```powershell
# Windows 11
ldbg.exe down
```

**期望输出**：

```
Torn down. Left intercepts [gde-adapter].
```

（若第 3 步做过 ambient 豁免，还会追加
`Reverted ambient on [Deployment/gde-adapter] (clean baseline).`）

`down` 的顺序是有讲究的：**先退拦截 → 再卸载 traffic-agent → 最后还原 ambient 豁免**
（agent 还在时把工作负载放回 ambient 会被黑洞）。变体：

- `ldbg down --stay-connected` —— 只退拦截，保留 telepresence 连接（马上还要再接管别的服务时用）。
- `ldbg down --keep-files` —— 保留 `.ldbg/` 里的 env-file 与日志。

**回滚校验**（只有第 3 步真的打过 ambient 豁免时才需要）：

```bash
kubectl -n kube-system get deploy gde-adapter \
  -o jsonpath='dpm=[{.spec.template.metadata.labels.istio\.io/dataplane-mode}] ann=[{.spec.template.metadata.annotations.ldbg\.local-debug/ambient-optout}]'
# 期望：dpm=[] ann=[]   （已回到原状）
```

**出问题**：`down` 输出里若出现
`! Could not remove the traffic-agent on [gde-adapter], so they were KEPT out of ambient to stay functional.`
—— 说明 agent 没删掉，`ldbg` 有意保留了豁免以免服务不可用。按提示重连命名空间后手工卸载：

```bash
telepresence connect -n kube-system
telepresence uninstall gde-adapter
```

> 🤖 Claude Code：`ldbg down --json`

---

## §4 Linux ↔ Windows 差异速查

| 事项 | Linux / macOS | Windows 11 (PowerShell) |
| --- | --- | --- |
| 二进制名 | `ldbg` | `ldbg.exe` |
| 提权方式 | `sudo`（`telepresence connect` 提示输密码） | UAC 弹窗（需管理员确认） |
| kubeconfig 默认位置 | `~/.kube/config` | `%USERPROFILE%\.kube\config` |
| 指定 kubeconfig | `export KUBECONFIG=/path/to/cfg` | `$env:KUBECONFIG = "C:\path\to\cfg"` |
| env-file 路径 | `.ldbg/gde-adapter.env` | `.ldbg\gde-adapter.env` |
| 加载 env-file 到当前 shell | `set -a; . .ldbg/gde-adapter.env; set +a` | `Get-Content … \| ForEach-Object { $k,$v = $_ -split '=',2; [System.Environment]::SetEnvironmentVariable($k,$v) }`（见 §3 第 4 步 B） |
| 构建 wrapper | `./mvnw` / `./gradlew` | `.\mvnw.cmd` / `.\gradlew.bat` |
| `--run` 写法 | `--run ./mvnw --run spring-boot:run` | `--run .\mvnw.cmd --run spring-boot:run` |
| 后台跑一条命令（如 ssh 隧道） | `ssh -N -L … &` | `Start-Process ssh -ArgumentList '-N','-L',…` |
| 结束后台 ssh | `kill %1` | `Get-Process ssh \| Stop-Process` |

其余命令（`ldbg` 全部子命令、`kubectl`、`telepresence`）两平台**完全一致**。

---

## §5 `kube-system` 专属注意事项

1. **影响面**：`kube-system` 里的服务往往被全集群依赖，全量接管期间它的可用性 = 你笔记本进程的
   可用性。请先在测试命名空间演练、提前通知相关方、并把调试窗口压到最短。
2. **可能触发滚动重启**：只有在该命名空间被标记为 ambient 时，`ldbg up` 才会 patch Pod 模板
   （打 `istio.io/dataplane-mode=none`），这会滚动重启 `gde-adapter`。`kube-system` 通常不在
   ambient 网格里，这一步会被跳过 —— 用第 1 步的 `doctor` 提前确认属于哪种情况。
3. **`ldbg test` 可能被准入控制挡住**：见 §3 第 5 步的替代验证方案。
4. **RBAC 更严**：先跑 §1.3 的 `kubectl auth can-i` 自查，别等 `up` 跑到一半才失败。
5. **日志采集常常不覆盖 `kube-system`**：`ldbg doctor` 会给出
   `! log-collection  no logs from "gde-adapter" in the last 15m …` 的 warn。
   要让集群日志库收到它的日志，需要给工作负载加采集标签（`logging.example.com/collect=true`）
   或让日志栈使用 collect-all overlay（见
   [log-analysis](https://github.com/hzeng10/log-analysis)）。**这不影响拦截调试** ——
   拦截期间你本来就该看 `ldbg logs local`。

---

## §6 故障排查

| 现象 | 原因 | 处置 |
| --- | --- | --- |
| 集群调用 `gde-adapter` 出现 **connection reset** | 该命名空间是 ambient，而工作负载没做豁免（例如用了 `--keep-ambient`） | 去掉 `--keep-ambient` 重跑 `ldbg up`；确认 `kubectl -n kube-system get deploy gde-adapter -o jsonpath='{.spec.template.metadata.labels}'` 含 `istio.io/dataplane-mode: none` |
| `telepresence connect` 卡住 / 失败 | root 守护进程要提权 | 在自己的交互式终端里执行（Linux 输 sudo 密码 / Windows 点 UAC）；不要在无 TTY 环境跑 |
| `ldbg up` 报 "the root network daemon needs elevation" | 同上 | 先手工 `telepresence connect -n kube-system`，再 `ldbg up` |
| 本地应用起不来：端口被占用 / 权限不足 | 默认本地端口 = Service 端口；<1024 在 Linux 非 root 不能绑 | `ldbg up gde-adapter -n kube-system --port 18080:8080`，应用改监听 18080 |
| `no Service/Deployment/StatefulSet/DaemonSet named "gde-adapter"` | 命名空间或名字不对 | `kubectl -n kube-system get svc,deploy \| grep gde-adapter` 核对；确认 `-n kube-system` 没漏 |
| `sync` / `up` 报 forbidden（读 ConfigMap/Secret） | RBAC 不足 | 对照 §1.3；`sync` 必须能 get 该工作负载引用的所有 CM/Secret |
| `ldbg test` 失败但本地进程明明收到了请求 | `kube-system` 不允许建临时 Pod（PSA/准入） | 用 §3 第 5 步的替代验证；不必强求 `test` 通过 |
| `ldbg logs query` 查不到任何记录 | 日志栈没部署，或采集未覆盖 `kube-system`，或地址解析不到 | 先看 `ldbg doctor` 的 `log-store` / `log-collection`；必要时 `--vlogs-addr http://127.0.0.1:9428`；拦截期间改用 `ldbg logs local` |
| 拦截建立了但集群流量没过来 | 客户端与集群 traffic-manager 版本不一致 | `telepresence version` 与 `kubectl -n ambassador get deploy traffic-manager -o jsonpath='{..image}'` 对齐（本工具锁定 2.29.0） |
| `ldbg down` 后 agent 还在 | `telepresence uninstall` 按已连接命名空间解析，连错了删不掉 | `telepresence connect -n kube-system` 后 `telepresence uninstall gde-adapter` |

更通用的排查项（离线安装、镜像侧载、出站鉴权）见 [SETUP §8](SETUP.zh-CN.md#8-故障排查)。

---

## §7 一页速查（可直接抄）

**Linux / macOS**

```bash
# 一次性：确认工具与集群
ldbg version && telepresence version && kubectl -n kube-system get svc gde-adapter

# 每次调试
ldbg doctor gde-adapter -n kube-system          # 1 预检
telepresence connect -n kube-system             # 2 连接（需 sudo，每会话一次）
ldbg up gde-adapter -n kube-system              # 3 接管（记下 local port）
#   IDE：EnvFile 指向 .ldbg/gde-adapter.env 后 Debug            ← 4 启动本地应用
#   或： set -a; . .ldbg/gde-adapter.env; set +a; ./mvnw spring-boot:run
#   或： ldbg up gde-adapter -n kube-system --run ./mvnw --run spring-boot:run
ldbg status                                     # 5 验证
ldbg test gde-adapter -n kube-system
ldbg logs local gde-adapter --level error       # 6 调试闭环（拦截期间看本地日志）
ldbg logs query gde-adapter --since 4h -q Exception   #   历史看集群日志库
ldbg down                                       # 7 收尾（必须）
```

**Windows 11 (PowerShell)**

```powershell
# 一次性：确认工具与集群
ldbg.exe version; telepresence version; kubectl -n kube-system get svc gde-adapter

# 每次调试
ldbg.exe doctor gde-adapter -n kube-system      # 1 预检
telepresence connect -n kube-system             # 2 连接（弹 UAC，每会话一次）
ldbg.exe up gde-adapter -n kube-system          # 3 接管（记下 local port）
#   IDE：EnvFile 指向 .ldbg\gde-adapter.env 后 Debug             ← 4 启动本地应用
#   或： Get-Content .ldbg\gde-adapter.env | Where-Object { $_ -and ($_ -notmatch '^\s*#') } |
#          ForEach-Object { $k,$v = $_ -split '=',2; [System.Environment]::SetEnvironmentVariable($k,$v) }
#        .\mvnw.cmd spring-boot:run
#   或： ldbg.exe up gde-adapter -n kube-system --run .\mvnw.cmd --run spring-boot:run
ldbg.exe status                                 # 5 验证
ldbg.exe test gde-adapter -n kube-system
ldbg.exe logs local gde-adapter --level error   # 6 调试闭环
ldbg.exe logs query gde-adapter --since 4h -q Exception
ldbg.exe down                                   # 7 收尾（必须）
```

**代理版（全部加 `--json`，见 §8）**

```bash
ldbg doctor gde-adapter -n kube-system --json
# （人工一次）telepresence connect -n kube-system
ldbg up gde-adapter -n kube-system --run ./mvnw --run spring-boot:run --json
ldbg test gde-adapter -n kube-system --json
ldbg logs query gde-adapter --since 4h -q Exception --json
ldbg logs local --level error --json
ldbg logs stats "by (level) count() as c" --since 10m --json
ldbg down --json
```

---

## §8 让 Claude Code 驱动这套流程

`ldbg` 的每个命令都支持 `--json` 和有意义的退出码，就是为了让 AI 代理能自主跑完
「接管 → 复现 → 查日志 → 改代码 → 验证 → 收尾」闭环。本节假设 `claude` 已经能在你机器上运行
（安装不在本文范围）。

### 8.1 一次性配置（两件事，都放在 `gde-adapter` 服务仓库里）

**① 仓库根目录放 `CLAUDE.md`** —— 下面这份是把
[`CLAUDE-template.zh-CN.md`](CLAUDE-template.zh-CN.md) 的占位符换成本例实参、并补上
`kube-system` 约束后的**可直接粘贴**版本：

````markdown
## 用 ldbg 在本地调试本服务（远程集群真实流量）

本仓库的服务运行在远程共享 k8s 集群。用 `ldbg` 可以把它接管到本机：本地进程接收该服务的
真实集群流量、访问真实依赖（DB/MQ/Redis/其他微服务）。

- 服务名：`gde-adapter`　命名空间：`kube-system`　本地启动：`./mvnw spring-boot:run`
  （Windows 用 `.\mvnw.cmd spring-boot:run`；Gradle 项目改 `./gradlew bootRun`）
- **所有 ldbg 命令都支持 `--json`**，envelope：`{ok, command, data, error, hint}`。
  失败按 `hint` 行动；`data.truncated=true` 表示结果被截断，需收窄条件。
- ⚠️ **全量接管**：拦截期间集群里对 `gde-adapter` 的所有调用都会打到本机（进程停止或停在
  断点时该服务不可用）。`kube-system` 影响面大 —— **执行 `ldbg up` 前必须先向我确认**。
- ⚠️ 调试完**必须** `ldbg down`。

### 标准调试闭环

```bash
ldbg doctor gde-adapter -n kube-system --json     # 预检（含 log-store / log-collection）
# 若 data.ok=false 且 cluster-reachable 为 fail → 停下来报告，不要继续
ldbg up gde-adapter -n kube-system --run ./mvnw --run spring-boot:run --json
ldbg test gde-adapter -n kube-system --json       # 看 data.succeeded
# …复现问题、读日志、改代码、重启本地进程…
ldbg test gde-adapter -n kube-system --json       # 验证修复
ldbg down --json                                  # 必须执行
```

首次 `telepresence connect -n kube-system` 需要 sudo/管理员提权，**你无法代劳** ——
遇到 connect 失败就提示我在自己的终端里执行一次，然后重试。

`ldbg test` 会在 `kube-system` 里创建临时 curl Pod，可能被准入控制拒绝。这**不代表拦截失败**：
改用本地日志（`ldbg logs local`）确认请求是否到达本机，并如实报告。

### 日志：两个来源，一套过滤词汇

| 场景 | 命令 |
|------|------|
| 历史 / 集群侧（含已删除 Pod） | `ldbg logs query gde-adapter --since 4h -q Exception --json` |
| **拦截期间**的新日志 | `ldbg logs local --level error --json`（拦截激活时可省略服务名） |
| 聚合验收（修复后错误归零） | `ldbg logs stats "by (level) count() as c" --since 10m --json` |
| 探索可查什么 | `ldbg logs fields` / `ldbg logs values service` |

- 时间窗：`--since 5m/30m/1h/4h/12h/1d/2d/7d…` 或 `--from/--to`（RFC3339）。
- 过滤：`-q 关键字`（多词 AND、`"短语"` 整体、`-i` 忽略大小写）、`--level`、`--pod`、`-c 容器`。
- 拦截期间集群侧对本服务是空窗 → 用 `logs local`（堆栈完整归并，`-q` 能匹配堆栈帧）。
- `kube-system` 可能不在日志采集范围内，`logs query` 查不到属正常，不要据此判断服务异常。

### 分工

开发者负责 IDE 断点与单步；你负责 `up/test/status/logs query/logs local`、读堆栈、改代码并迭代
—— 共享同一个 ldbg 会话。
````

**② 仓库根目录放 `.claude/settings.json`** —— 放行 `ldbg` 全部子命令（集群改动都由 `ldbg`
内部受控执行），kubectl 只放行只读动词，直接改集群的命令显式拒绝：

```json
{
  "permissions": {
    "allow": [
      "Bash(ldbg:*)",
      "Bash(ldbg.exe:*)",
      "Bash(kubectl get:*)",
      "Bash(kubectl describe:*)",
      "Bash(kubectl logs:*)",
      "Bash(kubectl auth can-i:*)",
      "Bash(telepresence status:*)",
      "Bash(telepresence version:*)",
      "Bash(./mvnw:*)",
      "Bash(./gradlew:*)",
      "Bash(mvnw.cmd:*)",
      "Bash(gradlew.bat:*)"
    ],
    "deny": [
      "Bash(kubectl delete:*)",
      "Bash(kubectl apply:*)",
      "Bash(kubectl patch:*)",
      "Bash(kubectl edit:*)",
      "Bash(kubectl scale:*)",
      "Bash(telepresence uninstall:*)",
      "Bash(telepresence helm:*)"
    ]
  }
}
```

> `allow` 里同时列出了两平台的 wrapper（`./mvnw` 与 `mvnw.cmd`），一份配置在 Linux 和
> Windows 上都能用。个人偏好（例如再放行 `Bash(jq:*)`）放 `.claude/settings.local.json`（不提交）。
> 日志后端地址特殊时可在会话里设 `VLOGS_ADDR=http://<地址>:9428`（默认不用设）。

### 8.2 代理必须知道的输出契约

所有 `--json` 输出都是同一个 envelope：

```jsonc
{ "ok": true, "command": "status", "data": { … }, "error": "…", "hint": "…" }
```

- **`ok`** = 这条命令本身是否成功执行；失败时 `data` 缺省、`error` + `hint` 有值，进程退出码为 **1**。
- **`data.ok`** 是**聚合判据**，只有 `doctor` 和 `cluster probe` 有：即使 envelope `ok:true`，
  `data.ok:false` 也表示有 `fail` 项（此时退出码同样是 1）。**别把两个 `ok` 搞混。**
- 失败时**按 `hint` 行动**，不要重试同一条命令。
- `data.truncated=true`（日志类命令）→ 按 `hint` 收窄时间窗或加过滤条件。

各命令 `data` 的真实字段（以此为准，不要解析人类文本）：

| 命令 | `data` 关键字段 |
| --- | --- |
| `status` | `telepresenceFound`、`connected`、`kubeContext`、`namespace`、`interceptActive`、`intercepts[]`、`managerInstalled`、`clusterReachable`、`clusterVersion`、`hint` |
| `doctor` | `checks[]`（每项 `name` / `status`=`pass\|warn\|fail` / `detail`）、`ok` |
| `up` | `target`、`namespace`、`envFile`、`varsWritten`、`localLogFile`、`port`、`localPort`、`connected`、`interceptActive`、`launched`、`ambient`、`ambientOptOutApplied` |
| `sync` | `target`、`namespace`、`kind`、`container`、`envFile`、`written`、`skipped`、`localLogFile`、`vars[]` |
| `test` | `url`、`fromClusterOrigin`、`succeeded`、`exitCode`、`body` |
| `down` | `leftIntercepts[]`、`uninstalledAgents[]`、`revertedAmbient[]`、`keptAmbientOptOut[]`、`disconnected`、`removedFiles[]` |

真实样例：

```console
$ ldbg status --json
{
  "ok": true,
  "command": "status",
  "data": {
    "telepresenceFound": true,
    "connected": false,
    "interceptActive": false,
    "managerInstalled": false,
    "clusterReachable": false,
    "hint": "cluster unreachable; check kube-context/kubeconfig"
  }
}
```

```console
$ ldbg test gde-adapter -n kube-system --json      # 失败形态，退出码 1
{
  "ok": false,
  "command": "test",
  "error": "get service \"gde-adapter\": …",
  "hint": "pass --port"
}
```

### 8.3 代理版七步

与 §3 的七步一一对应，只是全部加 `--json`：

```bash
# Linux / macOS
ldbg doctor gde-adapter -n kube-system --json                       # 1 预检；看 data.ok 与 checks[]
# 2 连接：人工执行一次 telepresence connect -n kube-system（需提权，代理无法代劳）
ldbg up gde-adapter -n kube-system --run ./mvnw --run spring-boot:run --json   # 3+4 接管并启动
ldbg status --json                                                  # 5 看 connected/interceptActive
ldbg test gde-adapter -n kube-system --json                         #   看 data.succeeded
ldbg logs query gde-adapter --since 4h -q Exception --json          # 6 历史（集群侧）
ldbg logs local --level error --json                                #   拦截期间（本地文件）
ldbg logs stats "by (level) count() as c" --since 10m --json        #   修复后错误归零 = 验收
ldbg down --json                                                    # 7 收尾（必须）
```

```powershell
# Windows 11
ldbg.exe doctor gde-adapter -n kube-system --json
# 人工一次： telepresence connect -n kube-system
ldbg.exe up gde-adapter -n kube-system --run .\mvnw.cmd --run spring-boot:run --json
ldbg.exe status --json
ldbg.exe test gde-adapter -n kube-system --json
ldbg.exe logs query gde-adapter --since 4h -q Exception --json
ldbg.exe logs local --level error --json
ldbg.exe logs stats "by (level) count() as c" --since 10m --json
ldbg.exe down --json
```

### 8.4 人机分工与必须人工介入的点

| 谁 | 做什么 |
| --- | --- |
| 开发者 | IDE 断点、单步、变量观察；`telepresence connect` 提权；**批准对 `kube-system` 的接管** |
| Claude Code | `doctor` / `up` / `status` / `test` / `logs *` / `down`、读堆栈、改代码、迭代验证 |

两者共享**同一个** `ldbg` 会话，互不干扰：代理跑 `ldbg up` 建立拦截后，你在 IDE 里 attach
或直接 Debug 启动都可以。

**唯一必须人工的一步**是 `telepresence connect`（root 守护进程要 sudo/UAC）。此外，鉴于
`kube-system` 的影响面，建议在 `CLAUDE.md` 里保留"执行 `ldbg up` 前先向我确认"这条约束
（8.1 的模板已包含）。

### 8.5 提示词示例

```text
> 帮我调试 gde-adapter（kube-system）：先预检，接管到本地并启动，复现「上游偶发 502」，
  查最近 4 小时的相关异常，定位后修掉并用 ldbg test 验证，最后清理。接管前先问我确认。
```

```text
> 不要接管服务。只查 gde-adapter 最近 12 小时里 level=error 的日志，按异常类型归类给我一个统计。
```

```text
> 我刚改了 GdeAdapterClient 的超时处理并重启了本地进程。跑一轮 ldbg test 验证，
  再用 logs stats 确认最近 10 分钟错误数归零。
```

**反例**（应当被 `.claude/settings.json` 的 `deny` 挡住，代理应转而建议用 `ldbg` 的受控路径）：

```text
> 帮我把 kube-system 里 gde-adapter 的 deployment 删了重建一下。
```

### 8.6 自检清单

- [ ] 在服务仓库根目录运行 `claude`，让它执行 `ldbg status --json` —— **不应**弹权限确认。
- [ ] 让它执行 `kubectl delete pod xxx` —— **应当**被拒绝。
- [ ] 让它按 `CLAUDE.md` 独立跑完一轮 `doctor → up → test → logs → down`，并在 `up` 前向你确认。
- [ ] `ldbg doctor --json` 的 `data.ok=false` 时，它会停下来报告而不是硬着头皮往下走。
