# local-debug (`ldbg`) 环境搭建与使用指南（简体中文）

> 让开发者或 ClaudeCode 在**笔记本**上运行一个 Spring Boot 微服务，使它表现为远程
> **Istio ambient** Kubernetes 集群中该服务的**真实实例**：接收该服务的真实入站流量、
> 调用集群内真实依赖（数据库、MQ、Redis、其他微服务），并可在 IDE 中打断点调试、由
> ClaudeCode 驱动。底层是对 **Telepresence**（免费的 global/TCP 全量拦截）的薄封装。

本指南覆盖两类环境：

- **笔记本（开发机）**：Windows 11 或 Ubuntu，**有网络**。
- **远程集群**：共享开发集群，**无外网（离线 / air-gapped）**，启用 Istio ambient。

> 针对 **Windows 11 → 远程气隙集群** 的逐步验证清单（含期望输出与验收门），见
> [`RUNBOOK.windows-remote.zh-CN.md`](RUNBOOK.windows-remote.zh-CN.md)。

---

## 0. 工作原理（一分钟）

```
集群内调用方 ──► orders Service ──►（全量拦截）──► 你的笔记本进程 ──►（telepresence 隧道）──► 集群依赖
                                          ▲                                   │
                                     IDE 断点调试                         真实 DB/MQ/...
```

- **入站接管**：对该服务的所有集群流量被路由到你笔记本上的本地进程（global/TCP 拦截 =
  完全接管，会影响共享集群里该服务的其他使用者，这是已接受的取舍）。
- **出站**：本地进程通过 `telepresence connect` 建立的隧道，用集群内 DNS 调用真实依赖。
- **配置同步**：`ldbg` 把该工作负载的集群环境变量（env / envFrom / ConfigMap / Secret）
  导出为 env-file；Spring Boot 的 *relaxed binding* 让这些环境变量覆盖 `application.yaml`，
  **无需修改应用代码或配置**（YAML 或 properties 都适用）。

> **为什么用 global/TCP 拦截**：它免费、无需 Ambassador Cloud / License、ambient 下不需要
> waypoint —— 非常适合离线集群。基于 header 的 personal 拦截是付费的、ambient 下需要
> waypoint、且需要离线集群无法获取的 License，因此不采用。

---

## 1. 前提条件

### 1.1 笔记本（开发机）
- `kubectl`，且有可访问远程集群的 kubeconfig（能 `kubectl get ns` 即可）。
- **Telepresence 客户端 v2.29.0**（见 §3.1 安装）。
- 你的 Spring Boot 工程（JDK / Maven / Gradle 照常）。
- 一次性管理员权限：Telepresence 的 **root 网络守护进程**需要提权（Linux 用 `sudo`，
  Windows 用管理员 / UAC）。这是每个会话**仅一次**的操作。

### 1.2 远程集群
- 已启用 **Istio ambient**（`istiod` + `istio-cni` + `ztunnel` 运行中）。
- 你的命名空间打了 `istio.io/dataplane-mode=ambient` 标签。
- 具备给工作负载打补丁、读取 ConfigMap/Secret 的 RBAC 权限。
- **无需** Helm 二进制（chart 已内嵌在 Telepresence 客户端中）。
- **无需**外网（镜像离线侧载，见 §2、§4）。

---

## 2. 离线准备（在**有网**的机器上，一次性）

集群无外网，所以先在有网机器上把 traffic-manager 镜像打包成传输包。
traffic-manager 与注入的 traffic-agent **是同一个镜像**：`ghcr.io/telepresenceio/tel2:<版本>`。

```bash
# 用 ldbg 一步完成：docker pull + docker save
ldbg bundle --tp-version 2.29.0 --out tel2-bundle.tar
# 等价于：
#   docker pull ghcr.io/telepresenceio/tel2:2.29.0
#   docker save ghcr.io/telepresenceio/tel2:2.29.0 -o tel2-bundle.tar
```

把 `tel2-bundle.tar` 拷贝到能访问集群的跳板机 / 运维机。

---

## 3. 笔记本侧设置（一次性）

### 3.1 安装 Telepresence 客户端 v2.29.0

**Ubuntu：**
```bash
sudo curl -fL https://github.com/telepresenceio/telepresence/releases/download/v2.29.0/telepresence-linux-amd64 \
  -o /usr/local/bin/telepresence
sudo chmod +x /usr/local/bin/telepresence
telepresence version          # 应显示 OSS Client : v2.29.0
```
（无 sudo 写 `/usr/local/bin` 权限时，可放到 `~/.local/bin` 并确保它在 `PATH` 中；
`ldbg` 会自动在 `PATH` 与 `~/.local/bin` 中查找 telepresence。）

**Windows 11（PowerShell，管理员）：** 下载安装器
`telepresence-windows-amd64-setup.exe`（同一 Release 页面）并运行；或解压
`telepresence-windows-amd64.zip` 后把 `telepresence.exe` 加入 `PATH`。

### 3.2 安装 `ldbg`
把对应平台的二进制放到 `PATH`：`ldbg-linux-amd64` / `ldbg-windows-amd64.exe`。
（从源码构建：仓库根目录执行 `make build` 或 `make cross`。）

### 3.3 客户端版本要与集群 traffic-manager 一致
`ldbg version` 显示本工具锁定的 Telepresence 版本（默认 2.29.0），需与 §4 安装的
traffic-manager 版本一致。

---

## 4. 集群侧离线安装 traffic-manager（一次性）

在能访问集群的运维机上（已带好 §2 的 `tel2-bundle.tar`、`telepresence` 客户端、`ldbg`、
正确的 kubeconfig）：

```bash
# 0) 预检
ldbg cluster preflight --import-via <registry|minikube|kind|k3d|ctr>

# 1) 一步：导入镜像 + 用内嵌 chart 安装 traffic-manager（pullPolicy=IfNotPresent）
#    —— 内部镜像仓库方式（最常见）：
ldbg cluster install --bundle tel2-bundle.tar --import-via registry \
  --registry <你的内部仓库/路径>

#    —— 或单节点 minikube：
ldbg cluster install --bundle tel2-bundle.tar --import-via minikube
```

`ldbg cluster install` 实际执行：
1. 把镜像导入集群（内部仓库推送，或 `minikube/kind/k3d image load`；containerd 节点用
   `ctr -n k8s.io images import` 逐节点导入）。
2. `telepresence helm install`（chart 内嵌、**无需联网**），并设置
   `images.agentImage`、必要时 `images.registry`，以及 `images.pullPolicy=IfNotPresent`，
   确保集群只用已侧载的镜像、**绝不访问外网**。

验证：
```bash
kubectl -n ambassador get deploy traffic-manager      # READY 1/1
kubectl -n ambassador get deploy traffic-manager \
  -o jsonpath='{.spec.template.spec.containers[0].image} {.spec.template.spec.containers[0].imagePullPolicy}{"\n"}'
# 期望：ghcr.io/telepresenceio/tel2:2.29.0 IfNotPresent
```

---

## 5. 日常调试流程（每次，"只在笔记本上启动并调试"）

> 一次性设置（§3、§4）完成后，每个会话只剩三步：连接一次 → `ldbg up` → 在 IDE 里调试。

### 5.1 连接集群（每个会话一次，需要提权）
```bash
telepresence connect            # Linux 会提示 sudo 密码；Windows 弹 UAC
telepresence status             # 看到 Status: Connected 即可
```
连接成功后，root 守护进程常驻；后续 `ldbg`/`telepresence` 命令**不再需要** sudo。

### 5.2 预检目标服务（可选，推荐）
```bash
ldbg doctor orders -n demo
# 检查：客户端 / 集群可达 / traffic-manager / 命名空间是否 ambient / 该工作负载是否需要 ambient 豁免
```

### 5.3 一键接管
```bash
ldbg up orders -n demo
```
`ldbg up` 会自动完成：
1. **同步配置** → 写出 `.ldbg/orders.env`（已 git-ignore，secret 以 0600 权限保存、日志中打码）。
2. **确保已连接**（未连接则尝试 connect）。
3. **Ambient 豁免（关键）**：若目标在 ambient 命名空间，自动给其 Pod 模板打
   `istio.io/dataplane-mode=none`，避免 istio-cni 与 traffic-agent 争抢端口导致
   "connection reset"；依赖服务仍留在 ambient。该改动由 `ldbg down` 自动还原。
4. **全量拦截**（global/TCP 全量接管）。

### 5.4 在笔记本上启动你的 Spring Boot 应用
- **IDE（IntelliJ / VS Code）**：把运行配置的 *EnvFile* 设为 `.ldbg/orders.env`，
  在 `ldbg up` 提示的本地端口上 **Run / Debug**（可打断点）。
- **命令行**：
  ```bash
  set -a; . .ldbg/orders.env; set +a   # Windows PowerShell 见 §7
  ./mvnw spring-boot:run                # Maven（或 mvn spring-boot:run）
  ./gradlew bootRun                     # Gradle（或 java -jar app.jar）
  ```
- **让 ldbg 直接启动**（stdout/stderr 自动落盘到 `.ldbg/logs/<svc>.log` 供 `logs local` 查询）：
  ```bash
  ldbg up orders -n demo --run ./mvnw --run spring-boot:run    # Maven
  ldbg up orders -n demo --run ./gradlew --run bootRun         # Gradle
  ```

> 本地日志落盘：`sync`/`up` 默认向 env-file 注入合成变量 `LOGGING_FILE_NAME`
> （绝对路径，Spring Boot relaxed binding 映射到 `logging.file.name`），IDE 启动的
> 应用也会同时写 `.ldbg/logs/<svc>.log`，零代码改动。不需要时加 `--no-local-log`。
> 应用若自带完全自定义的 logback 配置，该属性可能被忽略（`--run` 的 tee 不受影响）。

### 5.5 验证 & 观察
```bash
ldbg status --json     # connected / interceptActive / intercepts / 下一步提示
ldbg test              # 通过集群路径发请求，断言其落到本地进程
```

**日志查询**（需要集群内部署了 [log-analysis](https://github.com/hzeng10/log-analysis)
日志栈，`ldbg doctor` 会检查；气隙集群的离线部署步骤见
[RUNBOOK 阶段 G](RUNBOOK.windows-remote.zh-CN.md#阶段-g--日志栈离线部署可选一次性)）：

```bash
ldbg logs query orders --since 4h -q Exception     # 集群日志库：历史 + 已删除 Pod
ldbg logs query --pod orders-xxx -c istio-proxy --level error --since 12h
ldbg logs local orders --level error               # 拦截期间的本地日志（堆栈完整归并）
ldbg logs tail orders -q error                     # 日志库实时流
ldbg logs stats "by (service) count() as c" --since 1d
ldbg logs fields && ldbg logs values service       # 字段自省
```

- 时间窗：`--since 5m/30m/1h/4h/8h/12h/24h/2d/7d/…`（保留期 30 天），或 `--from/--to`（RFC3339）。
- 地址解析优先级：`--vlogs-addr` / `VLOGS_ADDR` → telepresence 隧道直连
  `victorialogs.logging.svc:9428` → 自动 port-forward（无需 telepresence 也能查）。
- `logs local` 默认识别 Spring Boot 缺省时间戳格式（本地时区），特殊格式用 `--ts-format`
  覆盖；拦截激活时省略服务名即默认为被拦截服务。
手动验证（从集群内发起，证明真正被接管）：
```bash
kubectl -n demo run t --image=curlimages/curl --restart=Never --rm -i -- \
  curl -s http://orders.demo.svc.cluster.local:8080/
# 响应应来自你的本地进程（而非集群 Pod）
```

### 5.6 收尾
```bash
ldbg down              # 退出拦截、断开连接、还原 ambient 豁免、清理 .ldbg/
# 想保留连接只退拦截：ldbg down --stay-connected
```

---

## 6. ClaudeCode 协作

`ldbg` 的每个命令都支持 `--json` 与有意义的退出码，便于 AI 代理驱动：

```bash
ldbg status --json           # 机器可读：connected / interceptActive / clusterReachable / hint
ldbg up orders -n demo --json
ldbg test --json
ldbg logs query --since 30m -q Exception --json   # 拦截激活时 service 自动默认
ldbg logs local --level error --json              # data.source=local-file
```

**分工建议**：开发者在 IDE 里打断点、单步；ClaudeCode 负责
`ldbg up/test/status`、`logs query`/`logs local`、读堆栈、改代码、迭代，二者共享同一个
`ldbg` 会话。给业务服务仓库放一份
[`CLAUDE-template.zh-CN.md`](CLAUDE-template.zh-CN.md)，代理即可即插即用。
ClaudeCode 的安装、`.claude/settings.json` 权限白名单示例与典型提示词，见
[RUNBOOK 阶段 H](RUNBOOK.windows-remote.zh-CN.md#阶段-h--配置-claudecode-驱动-ldbg一次性可选)。

---

## 7. 平台差异（Windows 11）

- `telepresence connect` 需要**管理员 / UAC**（对应 Linux 的 sudo）。
- 加载 env-file 到当前 shell（PowerShell）：
  ```powershell
  Get-Content .ldbg\orders.env | Where-Object { $_ -and ($_ -notmatch '^\s*#') } | ForEach-Object {
    $k,$v = $_ -split '=',2; [System.Environment]::SetEnvironmentVariable($k,$v)
  }
  ```
- 二进制：`ldbg-windows-amd64.exe`、`telepresence-windows-amd64-setup.exe`。

---

## 8. 故障排查

| 现象 | 原因 / 处理 |
| --- | --- |
| 集群内调用该服务出现 **connection reset** | ambient 下 istio-cni 与 traffic-agent 争端口。`ldbg up` 已自动打 `dataplane-mode=none` 豁免；若用了 `--keep-ambient` 则会复现。确认目标 Pod 模板带 `istio.io/dataplane-mode=none`。 |
| `telepresence connect` 失败 / 卡住 | root 守护进程需要提权。Linux：在自己的终端里运行（可输入 sudo 密码）；不要在无 TTY 的非交互环境运行。 |
| `ldbg up` 报无法连接 | 同上——先在终端手动 `telepresence connect` 一次，再跑 `ldbg up`。 |
| traffic-manager 起不来 / ImagePull 失败 | 镜像没侧载成功，或 `images.registry/agentImage` 没指向已导入镜像。重做 §4，确认 `imagePullPolicy=IfNotPresent` 且镜像在集群可见。 |
| 客户端与 manager 版本不一致 | 让 `telepresence version`（客户端）与 §4 安装版本一致（默认 2.29.0）。 |
| 出站调用被依赖的 L4 AuthorizationPolicy 拒绝 | 实测中本地出站经由 traffic-agent 所在 Pod 出去，源 IP 表现为被拦截工作负载的 Pod IP，因此按调用方身份鉴权通常**可通过**；如仍被拒，检查该依赖上是否有更严格的 PeerAuthentication/AuthorizationPolicy。 |
| 笔记本上没有 kubectl / kubeconfig，`kubectl` 只在跳板机 | 用 `ssh -L` 隧道桥接：跳板机 `kubectl port-forward svc/victorialogs` + 笔记本 `ldbg logs query --vlogs-addr http://127.0.0.1:9428`；先用 `ldbg cluster probe` 确认桥接可用。完整拦截应在跳板机上运行。详见 [RUNBOOK 阶段 J](RUNBOOK.windows-remote.zh-CN.md)。 |

---

## 9. 命令速查

```bash
ldbg version                         # ldbg 及目标 Telepresence 版本
ldbg doctor [service] -n <ns>        # 预检 + ambient 评估
ldbg sync   <service> -n <ns>        # 仅同步集群 env 到 env-file
ldbg up     <service> -n <ns>        # 同步 + 连接 + ambient 豁免 + 全量拦截
ldbg status [--json]                 # 连接 / 拦截状态
ldbg test                            # 经集群路径验证落到本地
ldbg down  [--stay-connected]        # 收尾 + 还原
ldbg bundle --out tel2-bundle.tar    # （有网机）打包 traffic-manager 镜像
ldbg cluster install --bundle ...    # （集群侧）离线安装 traffic-manager
ldbg cluster probe --vlogs-addr <url> --kubeconfig <file>   # 验证隧道/代理桥接能承载什么
ldbg cluster tunnel --bastion user@host                     # 打印 ssh -L 接入命令
ldbg cluster kubeconfig --api http://127.0.0.1:8001 --out proxy.kubeconfig  # 生成最小 kubeconfig
```
