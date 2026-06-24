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

# 在提示的本地端口上启动你的 Spring Boot 应用（IDE 调试、bootRun 或 java -jar）
#   IDE：把运行配置的 EnvFile 指向 .ldbg/orders.env，在该端口 Run/Debug
#   或：  set -a; . .ldbg/orders.env; set +a; ./gradlew bootRun

ldbg test orders -n demo     # 从集群内发请求 → 证明流量落到了你的笔记本
ldbg down                    # 退出拦截、还原 ambient、断开连接、清理
```

## 命令

| 命令 | 作用 |
| --- | --- |
| `ldbg up <svc> -n <ns>` | 同步 env → 连接 → ambient 豁免 → 全量拦截（可加 `--run`） |
| `ldbg down` | 退出拦截、还原 ambient 豁免、断开连接、删除生成的文件 |
| `ldbg status [--json]` | telepresence/集群/拦截 状态 + 下一步提示 |
| `ldbg doctor [svc] -n <ns>` | 预检：客户端、集群、traffic-manager、ambient 评估 |
| `ldbg sync <svc> -n <ns>` | 仅从工作负载的集群 env 生成 env-file |
| `ldbg test <svc> -n <ns>` | 集群内探测，显示是谁应答（证明接管生效） |
| `ldbg logs <svc> -n <ns>` | tail 工作负载的 Pod（含 traffic-agent）；`--manager`、`-f`、`--tail` |
| `ldbg intercept` / `leave` | 底层的全量拦截控制 |
| `ldbg bundle` | （联网机器）`docker save` traffic-manager 镜像为 tar 包 |
| `ldbg cluster install` | （气隙）导入镜像 + 用内嵌 chart 安装 traffic-manager |

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

## ClaudeCode 用法

```bash
ldbg up orders -n demo --json      # 结构化结果，便于代理据此分支决策
ldbg status --json                 # connected / interceptActive / clusterReachable / hint
ldbg test orders -n demo --json    # 从集群内源头证明接管生效
```

分工：开发者负责 IDE 断点；ClaudeCode 负责 `up`/`test`/`status`/`logs`、读堆栈、改代码并迭代
—— 二者共享同一个 `ldbg` 会话。

## 构建

```bash
make build      # 本机二进制 ./ldbg
make cross      # dist/ldbg-{linux,windows,darwin}-…（目标笔记本为 Windows 11 + Ubuntu）
make test       # 单元测试
```

需要 Go 1.22+。锁定 Telepresence 2.29.0（用 `ldbg version` 查看）。

**集成测试** —— `test/integration/harness.sh` 会拉起 kind + Istio ambient + 示例应用，驱动真实的
`up`→`test`→`down` 流程，并断言 ambient 豁免、入站接管、出站、离线安装与干净收尾。它在 CI 中运行
（`.github/workflows/integration.yml`）；详见 [`test/integration/README.md`](test/integration/README.md)。

## 注意事项 / 范围

- **完全接管**：拦截期间，共享集群中所有到目标服务的流量都会打到你的笔记本（你的应用一旦停止或
  停在断点，该服务即不可用）。对气隙集群而言，免费的全量拦截是唯一可行模式（基于 header 的
  *personal* 拦截是付费的，且需要集群无法获取的 License）。
- **出站身份**：笔记本的出站调用经由被拦截 Pod 中的 traffic-agent 发出，因此依赖看到的源是该
  工作负载的 Pod IP —— 按调用方身份鉴权的 L4 策略可以通过。
- **暂不支持**：header/personal 拦截、同时本地运行多个服务、非 JVM 服务。
