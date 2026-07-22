# Runbook：Windows 11 → 远程气隙集群 验证（简体中文）

> 本 Runbook 用于在 **Windows 11 笔记本** 上、连接 **远程共享、无外网（气隙）的 Istio ambient
> 集群**，复跑并验证 `ldbg`（Telepresence 全量拦截）方案。前置：该方案已在 Ubuntu + minikube
> 上验证通过（见 [`SETUP.zh-CN.md`](SETUP.zh-CN.md)）。本文是「真实环境复验」的逐步清单，
> 每个阶段都给出**命令 + 期望输出 + 检查点（✅/❌）**，并附回滚步骤。

---

## 0. 范围、风险与前提

**目标**：在 Windows 11 上启动某个 Spring Boot 服务，使其成为远程集群中该服务的真实实例，
验证「入站接管 + 出站调用真实依赖 + 配置同步」三件事端到端可用。

**关键风险（务必先读）**
- **全量拦截 = 完全接管**：拦截期间，集群内所有到目标服务的流量都会打到你的笔记本；
  你的应用一旦停止或停在断点，集群侧该服务即不可用。**请先与团队协调**，并优先选择
  **低流量 / 非关键** 的服务，或先用 §5 的「一次性测试服务」演练。
- **会改动共享工作负载**：`ldbg up` 会给目标工作负载注入 traffic-agent，并在 ambient 下打
  `istio.io/dataplane-mode=none` 标签（`ldbg down` 自动还原）。

**前提条件**
- Windows 11 笔记本：有网络可达远程集群（VPN/直连均可），已安装 `kubectl` 且 kubeconfig
  指向远程集群（`kubectl get ns` 可用）。
- 远程集群：已启用 Istio ambient（`istiod` + `istio-cni` + `ztunnel` 运行），目标命名空间
  打了 `istio.io/dataplane-mode=ambient`，**无外网**。
- 具备给工作负载打补丁、读 ConfigMap/Secret、（演练时）创建 Pod 的 RBAC。
- 一台**有网**的机器（做镜像打包，可以是另一台机器/同一台联网时）。
- `ldbg.exe` 与 `telepresence`（Windows）二进制。

> **版本对齐**：客户端 Telepresence 版本必须与集群 traffic-manager 版本一致。本方案锁定
> **2.29.0**（`ldbg version` 可查）。镜像为 `ghcr.io/telepresenceio/tel2:2.29.0`，**同一镜像**
> 同时用于 traffic-manager 和注入的 traffic-agent。

---

## 阶段 A — 在「有网机器」上打包镜像（一次性）

```powershell
# 需要 docker
ldbg.exe bundle --tp-version 2.29.0 --out tel2-bundle.tar
```
**期望**：生成 `tel2-bundle.tar`（约 ~30MB+）。等价于
`docker pull ghcr.io/telepresenceio/tel2:2.29.0` + `docker save ... -o tel2-bundle.tar`。

把 `tel2-bundle.tar` 拷贝到能访问气隙集群的运维机/跳板机。

- ✅ **检查点 A**：`tel2-bundle.tar` 已生成并送达运维机。

---

## 阶段 B — 集群侧离线安装 traffic-manager（一次性）

在运维机上（已带 `tel2-bundle.tar`、`telepresence`、`ldbg`、指向集群的 kubeconfig）：

```powershell
# 1) 预检
ldbg.exe cluster preflight --import-via registry --registry <内部仓库/路径>

# 2) 导入镜像 + 用内嵌 chart 安装（pullPolicy=IfNotPresent，集群不触网）
ldbg.exe cluster install --bundle tel2-bundle.tar `
  --import-via registry --registry <内部仓库/路径>
```

> 导入方式按你的集群选择：
> - **`registry`**（最常见）：`ldbg` 把镜像 `docker tag` 到 `<内部仓库/路径>/tel2:2.29.0`
>   并 `docker push`；安装时 `images.registry` 指向该仓库。
> - **`ctr`**（containerd 节点、无内部仓库）：把 tar `scp` 到**每个节点**，逐节点执行
>   `sudo ctr -n k8s.io images import tel2-bundle.tar`，再用 `--no-import` 跑安装。

**验证**：
```powershell
kubectl -n ambassador get deploy traffic-manager
kubectl -n ambassador get deploy traffic-manager `
  -o jsonpath='{.spec.template.spec.containers[0].image} {.spec.template.spec.containers[0].imagePullPolicy}'
```
**期望**：`traffic-manager` READY `1/1`；镜像 `…/tel2:2.29.0`，pullPolicy `IfNotPresent`。

- ✅ **检查点 B**：traffic-manager 在 `ambassador` 命名空间 Running，使用离线镜像。

---

## 阶段 C — Windows 11 笔记本环境准备（一次性）

### C.1 工具清单

| 工具 | 版本 | 用途 | 校验命令 |
|------|------|------|----------|
| `telepresence.exe` | **2.29.0**（须与集群 manager 一致） | 隧道 + 拦截 | `telepresence version` |
| `ldbg.exe` | 最新（构建见 README「从源码构建」） | 调试编排 CLI | `ldbg version` |
| `kubectl.exe` | 与集群相近的版本 | 集群操作/排障 | `kubectl version --client` |
| JDK | 服务所需（如 21） | 本地运行 Spring Boot | `java -version` |
| Maven 或 Gradle | 服务所用（wrapper 优先） | 构建/启动 | `.\mvnw.cmd -v` / `.\gradlew.bat -v` |
| IDE（IntelliJ/VS Code） | 任意近期版 | 断点调试 | IntelliJ 需装 **EnvFile** 插件（net.ashald.envfile） |
| ClaudeCode | 最新 | AI 驱动调试闭环（可选，配置见阶段 H） | `claude --version` |

安装步骤：

1. **Telepresence 客户端**：运行安装器 `telepresence-windows-amd64-setup.exe`，或解压
   `telepresence-windows-amd64.zip` 并把 `telepresence.exe` 加入 `PATH`。
   ```powershell
   telepresence version      # 期望 OSS Client : v2.29.0
   ```
2. **`ldbg.exe` 加入 `PATH`**（推荐放到一个固定目录并追加到用户 PATH）：
   ```powershell
   New-Item -ItemType Directory -Force $env:USERPROFILE\bin | Out-Null
   Copy-Item .\ldbg.exe $env:USERPROFILE\bin\
   [Environment]::SetEnvironmentVariable("Path",
     [Environment]::GetEnvironmentVariable("Path","User") + ";$env:USERPROFILE\bin", "User")
   # 新开 PowerShell 窗口后：ldbg version
   ```
3. **kubeconfig 指向远程集群**：把集群管理员发放的 kubeconfig 存为
   `%USERPROFILE%\.kube\config`（或用 `$env:KUBECONFIG` 指定路径）。最小结构示例：
   ```yaml
   apiVersion: v1
   kind: Config
   clusters:
     - name: debug-cluster
       cluster:
         server: https://10.0.0.10:6443          # 集群 API 地址（内网可达）
         certificate-authority-data: <BASE64_CA>
   users:
     - name: dev-user
       user:
         token: <SERVICE_ACCOUNT_TOKEN>           # 或 client-certificate-data/client-key-data
   contexts:
     - name: debug
       context: { cluster: debug-cluster, user: dev-user, namespace: demo }
   current-context: debug
   ```
   ```powershell
   kubectl config current-context    # 期望 debug
   kubectl get ns                    # 能列出远程命名空间
   ```
   > RBAC 最低要求：目标命名空间内 get/list pods、deployments、services、configmaps、secrets，
   > patch deployments（ambient 豁免）；`ambassador`、`logging` 命名空间 get/list（doctor/日志）。
4. **服务仓库工作区**：在服务仓库根目录使用 ldbg（生成的 `.ldbg/`、`.run/` 都落在这里），
   并把以下条目加入服务仓库 `.gitignore`：
   ```gitignore
   .ldbg/
   *.env
   ```

- ✅ **检查点 C**：上表校验命令全部通过；`kubectl get ns` 可列出远程命名空间。

---

## 阶段 D — 连通性与预检

```powershell
# 1) 启动集群网络守护进程（每个会话一次，弹 UAC / 需要管理员）
telepresence connect -n <目标命名空间>
telepresence status        # 期望 Status: Connected；Namespace: <目标命名空间>

# 2) ldbg 预检（客户端 / 集群可达 / traffic-manager / ambient 评估）
ldbg.exe doctor <service> -n <目标命名空间>
```
**期望 `ldbg doctor` 输出**（示意）：
```
✓ telepresence-client    found v2.29.0
✓ cluster-reachable      kubernetes vX.Y
✓ traffic-manager        connected; manager installed
✓ ambient-namespace      namespace "<ns>" dataplane-mode=ambient
! ambient-workload        Deployment/<service> is in ambient; 'ldbg up' will apply dataplane-mode=none
overall: ok
```

> **务必用 `-n <目标命名空间>` 连接**：`ldbg up` 据此把连接 scope 到目标命名空间，`ldbg down`
> 才能用 `telepresence uninstall`（该命令没有 `-n`，按连接的命名空间解析）移除 agent。

- ✅ **检查点 D**：`Connected` 且 `Namespace` = 目标命名空间；`doctor` overall ok。

---

## 阶段 E — 端到端验证

> 先用 §5 的一次性测试服务演练通过后，再对真实服务执行本阶段（强烈建议）。

### E.1 接管服务（同步配置 + ambient 豁免 + 全量拦截）
```powershell
ldbg.exe up <service> -n <目标命名空间>
```
**期望**（关键行）：
```
✓ synced N env vars → .ldbg\<service>.env
✓ connected
… ambient: excluding "<service>" ... (istio.io/dataplane-mode=none)
✓ ambient opt-out applied (reverted by 'ldbg down')
✓ global intercept active — cluster traffic to "<service>" now routes to your laptop
```
确认豁免已带 ldbg 标记（便于 down 精确还原）：
```powershell
kubectl -n <ns> get deploy <service> `
  -o jsonpath='dpm=[{.spec.template.metadata.labels.istio\.io/dataplane-mode}] ann=[{.spec.template.metadata.annotations.ldbg\.local-debug/ambient-optout}]'
# 期望：dpm=[none] ann=[applied]
```

### E.2 在 Windows 上启动 Spring Boot 应用（加载同步的集群环境）
- **IDE（IntelliJ/VS Code）**：把运行配置的 **EnvFile** 设为 `.ldbg\<service>.env`，在
  `ldbg up` 提示的本地端口上 **Run/Debug**（可断点）。
- **PowerShell 命令行**：先把 env-file 注入当前会话，再启动：
  ```powershell
  Get-Content .ldbg\<service>.env |
    Where-Object { $_ -and ($_ -notmatch '^\s*#') } |
    ForEach-Object { $k,$v = $_ -split '=',2; [Environment]::SetEnvironmentVariable($k,$v) }
  .\mvnw.cmd spring-boot:run   # Maven（或 mvn spring-boot:run）
  .\gradlew.bat bootRun        # Gradle（或 java -jar <app>.jar）
  ```
- 也可让 ldbg 直接启动（stdout 同时落盘到 `.ldbg\logs\<service>.log` 供 `logs local` 查询）：
  `ldbg.exe up <service> -n <ns> --run cmd --run /c --run "mvnw.cmd spring-boot:run"`
  （Gradle 则替换为 `"gradlew.bat bootRun"`）
- env-file 默认注入合成变量 `LOGGING_FILE_NAME`（IDE 启动的应用也会写本地日志文件，零代码
  改动；不需要时加 `--no-local-log`）。

### E.3 三项断言（从**集群内部**发起，证明真实接管）
```powershell
# 入站接管：集群内请求应落到你的笔记本进程（而非集群 Pod）
ldbg.exe test <service> -n <目标命名空间>
# 期望：succeeded=true，响应来自你的本地进程
```
手动等价断言（更直观）：
```powershell
kubectl -n <ns> run probe --image=curlimages/curl:8.10.1 --restart=Never --rm -i -- `
  curl -s http://<service>.<ns>.svc.cluster.local:<port>/<健康或回显路径>
```
- **入站**：响应来自笔记本进程（你的标识/主机名），非集群 Pod。 ✅
- **出站**：在你的本地应用里触发一次对真实依赖的调用（DB/MQ/其他服务），确认成功；
  说明笔记本经隧道用集群 DNS 访问到了真实依赖。 ✅
- **配置**：应用以集群环境启动（如 `SPRING_PROFILES_ACTIVE`、数据源等与集群一致）。 ✅

- ✅ **检查点 E**：入站落到笔记本、出站可达依赖、配置一致；可在 IDE 命中断点。

日志侧验证（若集群已部署日志栈，见阶段 G）：
```powershell
ldbg.exe logs query <service> --since 30m          # 经隧道查集群日志库
ldbg.exe logs local <service> --level error        # 拦截期间的本地日志
ldbg.exe doctor <service> -n <ns>                  # log-store / log-collection 两项检查
```

---

## 阶段 F — 清理与回滚

```powershell
# 正常收尾：退拦截 → 卸载 agent → 还原 ambient → 断开
ldbg.exe down -n <目标命名空间>
```
**期望 JSON**（`--json`）：
```json
{ "leftIntercepts":["<service>"], "uninstalledAgents":["<service>"],
  "revertedAmbient":["Deployment/<service>"], "disconnected":true }
```
**验证回到基线**：
```powershell
kubectl -n <ns> get deploy <service> `
  -o jsonpath='dpm=[{.spec.template.metadata.labels.istio\.io/dataplane-mode}] ann=[{.spec.template.metadata.annotations.ldbg\.local-debug/ambient-optout}]'
# 期望：dpm=[] ann=[]（已回到 ambient、无 ldbg 标记）
kubectl -n <ns> get pod -l <selector>     # 期望 1/1（无 traffic-agent），且集群内可达
```

> **手动回滚（若 `ldbg down` 未能卸载 agent，会保留 ambient 豁免以保证服务可用）**：
> ```powershell
> telepresence connect -n <ns>      # 连接 scope 到该命名空间
> telepresence uninstall <service>  # 移除 agent
> kubectl -n <ns> patch deploy <service> --type=json `
>   -p '[{"op":"remove","path":"/spec/template/metadata/labels/istio.io~1dataplane-mode"}]'
> telepresence quit --stop-daemons
> ```

- ✅ **检查点 F**：目标工作负载 `1/1`、回到 ambient、无 ldbg 标记、集群内正常服务；
  `telepresence status` 显示守护进程已停止。

---

## 阶段 G — 日志栈离线部署（可选，一次性）

让 `ldbg logs query/tail/stats` 可用的前提：集群内部署
[log-analysis](https://github.com/hzeng10/log-analysis)（VictoriaLogs + Vector +
可选 Grafana）。完整步骤见其 **`docs/airgap.md`**（中文），此处只列要点：

1. **有网机器上**：`scripts/save-images.sh` 按 `scripts/images.txt` 拉取并导出镜像 tar
   （或 `mirror-to-registry.sh <内部仓库>` 直推）。⚠️ **Grafana 定制镜像必须在有网机器上
   `docker build`**（插件在构建时下载），产物镜像随包分发；只用 CLI 查询可跳过 Grafana。
2. **气隙侧**：`scripts/load-images.sh` 导入每个节点（或走内部仓库），然后：
   ```powershell
   kubectl apply -k deploy/overlays/collect-all/   # 全量采集（调试集群推荐；含基础设施 ns 排除）
   # 或默认 opt-in 白名单：kubectl apply -k deploy/k8s/
   ```
3. **验证**（笔记本上）：
   ```powershell
   ldbg.exe doctor <service> -n <ns>          # log-store=pass、log-collection=pass
   ldbg.exe logs query <service> --since 5m   # 经隧道返回该服务日志
   ```
   未连接 telepresence 时 `logs query` 自动走 port-forward，同样可用。

- ✅ **检查点 G**：`victorialogs-0` 与 `vector` DaemonSet Running；doctor 两项日志检查通过；
  `logs query -n kube-system --since 5m` 计数为 0（排除生效）。

---

## 阶段 H — 配置 ClaudeCode 驱动 ldbg（一次性，可选）

目标：在**服务仓库**里配置好 ClaudeCode，让它无需人工带路即可跑通
「接管 → 复现 → 查日志 → 改代码 → 验证 → 收尾」闭环；开发者只负责 IDE 断点。

### H.1 安装 ClaudeCode（Windows 11）

```powershell
irm https://claude.ai/install.ps1 | iex     # 原生安装器（推荐）
# 或：npm install -g @anthropic-ai/claude-code
claude --version
```

首次运行 `claude` 按提示登录（Pro/Max 订阅或 API Key）。气隙注意：ClaudeCode 本身需要
访问 Anthropic API —— 笔记本须有外网（或经代理），只有**集群侧**是气隙的；若笔记本也
完全无外网，则跳过本阶段，纯手工使用 ldbg。

### H.2 在服务仓库放置 `CLAUDE.md`

把本仓库 [`docs/CLAUDE-template.zh-CN.md`](CLAUDE-template.zh-CN.md) 分隔线之间的内容拷贝为
服务仓库根目录的 `CLAUDE.md`，并替换占位符。以 `orders`（Maven，命名空间 `demo`）为例，
关键行替换后形如：

```markdown
- 服务名：`orders`　命名空间：`demo`　本地构建/启动：`.\mvnw.cmd spring-boot:run`
```

模板已包含：标准调试闭环命令序列、envelope 语义（`ok/data/error/hint`、`truncated`）、
时间窗口词汇（5m…7d）、拦截期间 `logs local` 与 `logs query` 的分工、以及
「connect 需要用户手动提权」的处理方式 —— ClaudeCode 读到即可正确行动。

### H.3 权限配置（免打扰运行 ldbg，同时挡住危险操作）

在服务仓库创建 `.claude/settings.json`（团队共享，随仓库提交）：

```json
{
  "permissions": {
    "allow": [
      "Bash(ldbg:*)",
      "Bash(ldbg.exe:*)",
      "Bash(kubectl get:*)",
      "Bash(kubectl describe:*)",
      "Bash(kubectl logs:*)",
      "Bash(telepresence status:*)",
      "Bash(telepresence version:*)",
      "Bash(mvnw.cmd:*)",
      "Bash(gradlew.bat:*)"
    ],
    "deny": [
      "Bash(kubectl delete:*)",
      "Bash(kubectl apply:*)",
      "Bash(kubectl patch:*)",
      "Bash(kubectl edit:*)",
      "Bash(telepresence uninstall:*)",
      "Bash(telepresence helm:*)"
    ]
  }
}
```

- 思路：**放行 ldbg 全部子命令**（集群改动都由 ldbg 内部受控执行：ambient 豁免、拦截、
  还原），kubectl 只放行只读动词；直接改集群的 kubectl/telepresence 管理命令显式拒绝，
  共享集群更安全。个人偏好（如额外放行 `Bash(jq:*)`）放 `.claude/settings.local.json`（不提交）。
- 可选环境变量：日志后端地址特殊时，在会话里或 settings 的 `env` 中设
  `VLOGS_ADDR=http://<地址>:9428`（默认无需设置——ldbg 自动走隧道或 port-forward）。

### H.4 典型用法（提示词示例）

在服务仓库根目录运行 `claude`，然后例如：

```text
> 帮我调试 orders：接管到本地并启动，复现「下单偶发 500」，
  查最近 4 小时集群日志里的相关异常，定位后修掉并验证，最后清理。
```

期望 ClaudeCode 的行为（模板 + 权限配置生效的标志）：

```powershell
ldbg doctor orders -n demo --json          # 预检（含 log-store/log-collection）
ldbg up orders -n demo --run .\mvnw.cmd --run spring-boot:run --json
ldbg test orders -n demo --json            # 断言接管生效
ldbg logs query orders --since 4h -q Exception --json   # 历史（集群侧）
ldbg logs local --level error --json       # 拦截期间的本地新日志（堆栈完整）
# …读堆栈 → 改代码 → 重启本地进程 → ldbg test 验证…
ldbg logs stats "by (level) count() as c" --since 10m --json   # 错误归零 = 验收
ldbg down --json                           # 必须收尾
```

已知的一次人工介入：首次 `telepresence connect` 需要管理员/UAC 提权，ClaudeCode 会提示你
在自己的终端里执行一次（`telepresence connect -n demo`），之后整个会话它都能自主运行。

- ✅ **检查点 H**：`claude` 内让其执行 `ldbg status --json` 不弹权限确认；让其执行
  `kubectl delete pod xxx` 被拒绝；对着 CLAUDE.md 里的服务名能独立跑完一轮
  up → test → logs → down。

---

## 阶段 J — 远程 kubectl / SSH 跳板机接入（可选：笔记本无法直连集群 API 时）

**适用场景**：`kubectl` 只装在集群侧的**跳板机（bastion）**上，Windows 11 笔记本上既没有
`kubectl`、也没有 kubeconfig，无法直接到达集群 API。只要笔记本能 **SSH 到跳板机**，就用
`ssh -L` TCP 隧道桥接（Windows 11 自带 OpenSSH 客户端，`ssh -L` 开箱即用）。

> 关键事实（本项目实测校正）：曾以为 `kubectl proxy` 这类反向代理会**丢掉** port-forward 所需的
> 流升级（SPDY/WebSocket）。实测推翻了这个假设——`kubectl proxy` 用的是 Kubernetes 自己的
> *upgrade-aware* 代理，能把 port-forward 透传过去（`ssh -L` 只是透明 TCP 隧道，不影响升级）。
> 但**是否可用取决于 kubectl/集群版本以及链路上是否还有其它代理/负载均衡**，所以别假设——用
> `ldbg cluster probe` 逐环境确认。

### J.1 日志（最稳：无需本地 kubectl、无需代理）

在**跳板机**直接把日志库端口 forward 出来，笔记本用 `ssh -L` 把这个普通 TCP 端口引到本地。
这条路**完全不依赖代理承载升级**，最稳：

```bash
# 跳板机（bastion）
kubectl port-forward -n logging svc/victorialogs 9428:9428 --address 127.0.0.1

# 笔记本（Windows PowerShell / 任意终端）
ssh -L 9428:127.0.0.1:9428 user@bastion

# 笔记本：无需任何 kubeconfig
ldbg cluster probe --vlogs-addr http://127.0.0.1:9428          # 期望：log-store ✓
ldbg logs query orders --vlogs-addr http://127.0.0.1:9428 --since 30m
ldbg logs tail  orders --vlogs-addr http://127.0.0.1:9428
```

也可用 `ldbg cluster tunnel --bastion user@bastion` 直接打印上面这套命令。

### J.2 API / REST（sync 等 client-go 操作）

在跳板机跑 `kubectl proxy`（它用跳板机自己的凭据认证），笔记本 `ssh -L` 引到本地，再指向一份
**无凭据**的最小 kubeconfig：

```bash
# 跳板机
kubectl proxy --port=8001 --address 127.0.0.1

# 笔记本
ssh -L 8001:127.0.0.1:8001 user@bastion
ldbg cluster kubeconfig --api http://127.0.0.1:8001 --out proxy.kubeconfig
ldbg --kubeconfig proxy.kubeconfig sync orders -n demo
```

### J.3 用 `ldbg cluster probe` 确认这座桥能承载什么

在依赖某条链路之前，先跑 probe，逐项看 pass/fail：

```bash
ldbg cluster probe --kubeconfig proxy.kubeconfig --vlogs-addr http://127.0.0.1:9428
# ✓ api           REST + 认证可达
# ✓ rbac          能读命名空间内 Pod
# ✓/✗ port-forward 这座桥是否承载流升级（决定性一项）
# ✓ log-store      经隧道的 VictoriaLogs /health 可达
```

- **port-forward = ✓**：这座桥能承载 port-forward，笔记本侧的日志/端口转发都能用。
- **port-forward = ✗**（api 却是 ✓）：该链路不承载升级（个别代理/老版本），回退到 J.1 的
  跳板机 `kubectl port-forward` + `ssh -L`；probe 会把这条建议直接打印出来。

### J.4 重要限制：完整拦截应在跳板机上运行

`ldbg up`（Telepresence 全量拦截）需要到 **Pod 网段的网络** + traffic-manager 连接，仅有一个
REST 代理并不够。因此在这种拓扑下：**完整拦截在跳板机上运行**（在跳板机装 `ldbg` +
Telepresence 客户端，按阶段 D/E 执行），**笔记本侧保留日志查询与只读操作**（J.1/J.2）。

- ✅ **检查点 J**：`ldbg cluster probe --vlogs-addr http://127.0.0.1:9428` 显示 `log-store ✓`，
  且 `ldbg logs query <svc> --vlogs-addr http://127.0.0.1:9428 --since 30m` 能返回记录——
  全程笔记本上没有 kubeconfig。

---

## 验收清单（Pass / Fail Gates）

| # | 验收点 | 命令 / 证据 | 通过标准 |
|---|--------|------------|----------|
| 1 | 离线镜像安装 | 检查点 B | manager `1/1`、`tel2:2.29.0`、`IfNotPresent` |
| 2 | 客户端/集群版本一致 | `telepresence version` vs manager | 均为 2.29.0 |
| 3 | 连接 & 预检 | 检查点 D | Connected + doctor ok |
| 4 | 入站接管 | `ldbg test` / 集群内 curl | 响应来自笔记本进程 |
| 5 | 出站到依赖 | 本地应用调用真实依赖 | 调用成功 |
| 6 | 配置同步 | 应用启动日志/行为 | 与集群一致、无需改代码 |
| 7 | 干净回滚 | 检查点 F | 工作负载回基线、守护进程停止 |

全部 ✅ → Windows 11 + 远程气隙集群 验证通过。

---

## 5. 先做「一次性测试服务」演练（强烈建议）

在动真实服务前，先在一个测试命名空间复现 minikube 上的最小用例（回显服务 `orders` +
依赖 `dep`），把机制跑通：

```yaml
# demo-remote.yaml —— 在测试命名空间验证机制（端口/镜像按你的集群可用镜像调整）
apiVersion: v1
kind: Namespace
metadata: { name: ldbg-smoke, labels: { istio.io/dataplane-mode: ambient } }
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: dep, namespace: ldbg-smoke }
spec:
  replicas: 1
  selector: { matchLabels: { app: dep } }
  template:
    metadata: { labels: { app: dep } }
    spec:
      containers:
        - { name: whoami, image: <可用的 whoami/echo 镜像>, args: ["--name","CLUSTER-DEP"], ports: [{ containerPort: 80 }] }
---
apiVersion: v1
kind: Service
metadata: { name: dep, namespace: ldbg-smoke }
spec: { selector: { app: dep }, ports: [{ name: http, port: 80, targetPort: 80 }] }
---
apiVersion: apps/v1
kind: Deployment
metadata: { name: orders, namespace: ldbg-smoke }
spec:
  replicas: 1
  selector: { matchLabels: { app: orders } }
  template:
    metadata: { labels: { app: orders } }
    spec:
      containers:
        - name: orders
          image: <可用的 whoami/echo 镜像>
          args: ["--name","CLUSTER-ORDERS","--port","8080"]
          ports: [{ containerPort: 8080 }]
          env:
            - { name: DEP_URL, value: "http://dep.ldbg-smoke.svc.cluster.local" }
            - { name: SPRING_PROFILES_ACTIVE, value: "cluster" }
---
apiVersion: v1
kind: Service
metadata: { name: orders, namespace: ldbg-smoke }
spec: { selector: { app: orders }, ports: [{ name: http, port: 8080, targetPort: 8080 }] }
```
> 注意：测试服务用到的镜像在气隙集群也必须**已侧载**（同样用 `ldbg bundle` 思路或内部仓库）。

演练步骤同 §D–§F，把 `<service>=orders`、`<ns>=ldbg-smoke`、`<port>=8080`。验证通过后
`kubectl delete ns ldbg-smoke` 清理。

---

## 6. 与 minikube 验证的差异点（重点）

| 方面 | minikube（已验证） | Windows + 远程气隙（本 Runbook） |
|------|--------------------|-------------------------------|
| 镜像侧载 | `minikube image load`（模拟气隙） | **真实气隙**：内部仓库 push 或逐节点 `ctr import` |
| 提权 | Linux `sudo` | Windows **管理员 / UAC** |
| env-file 加载 | `set -a; . file; set +a` | PowerShell `SetEnvironmentVariable`（见 E.2） |
| 二进制 | `ldbg` / `telepresence` | `ldbg.exe` / `telepresence.exe` |
| 集群 | 本地单节点 | 远程多节点、共享、需 VPN/网络可达 |
| 影响面 | 仅自己 | **共享集群**：全量接管会影响他人，需协调 |

---

## 7. 故障排查（Windows / 远程 / 气隙 特有）

| 现象 | 处理 |
|------|------|
| 集群内调用目标服务 **connection reset** | ambient 下 istio-cni 与 traffic-agent 争端口。`ldbg up` 已自动打 `dataplane-mode=none`；若用了 `--keep-ambient` 会复现。确认目标 Pod 模板含 `istio.io/dataplane-mode=none`。 |
| `telepresence connect` 卡住/失败 | 需管理员/UAC；在你自己的 PowerShell（前台）运行，确保能弹出 UAC。 |
| `telepresence uninstall` 报 workload not found | 连接没 scope 到目标命名空间。先 `telepresence connect -n <ns>`，再 uninstall。 |
| ImagePull 失败 / manager 起不来 | 镜像未真正侧载，或 `images.registry/agentImage` 未指向已导入镜像；确认 `IfNotPresent` 且镜像在集群可见。重做阶段 B。 |
| 笔记本无法解析 `*.svc.cluster.local` | `telepresence connect` 未成功，或公司 DNS/VPN 拦截。检查 `telepresence status` 的 DNS/Subnets；必要时 `--also-proxy`/`--mapped-namespaces`。 |
| 出站被依赖的 L4 AuthorizationPolicy 拒绝 | 实测出站经 traffic-agent 所在 Pod 出去，源表现为目标工作负载 Pod，按调用方鉴权通常可过；若仍被拒，检查该依赖的 PeerAuthentication/AuthorizationPolicy。 |
| 版本不一致告警 | 让客户端与 manager 同为 2.29.0。 |

---

## 8. 一页速查

```powershell
# 一次性（安装/部署）
ldbg.exe bundle --out tel2-bundle.tar                                   # 有网机
ldbg.exe cluster install --bundle tel2-bundle.tar --import-via registry --registry <repo>  # 集群侧
#   日志栈（可选）：见阶段 G；ClaudeCode 配置（可选）：见阶段 H
#   （服务仓库放 CLAUDE.md + .claude/settings.json 权限）

# 每个会话
telepresence connect -n <ns>                  # 管理员/UAC，一次
ldbg.exe doctor <svc> -n <ns>                 # 预检（含 log-store/log-collection）
ldbg.exe up <svc> -n <ns> --run .\mvnw.cmd --run spring-boot:run   # 同步+豁免+拦截+启动
#   或在 IDE 用 .ldbg\<svc>.env 调试（EnvFile 插件）/ gradlew.bat bootRun
ldbg.exe test <svc> -n <ns>                   # 集群内验证落到本地
ldbg.exe logs query <svc> --since 4h -q Exception   # 集群日志库（历史）
ldbg.exe logs local --level error             # 拦截期间的本地日志
ldbg.exe down -n <ns>                         # 收尾+卸载agent+还原ambient+断开
```
