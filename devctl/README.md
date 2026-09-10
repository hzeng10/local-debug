# devctl：Windows 本地 JVM 连接共享 Kubernetes

独立 Go 模块，**不依赖原目录的 ldbg 实现**。Go 实现 CLI、本地管理进程、配置解析、健康检查及测试编排；不需要安装 Python、Node.js、Docker 或 WSL。Go 只用于构建，开发者使用编译后的 EXE。

```text
本地浏览器 / Postman → http://127.0.0.1:8080 → 本地 JVM
                                              ↓
                                  共享出口 / 集群 DNS
                                              ↓
                                集群业务 API、数据库及中间件
```

集群 B 调用 A 时仍使用共享集群 A。没有流量拦截、镜像、个人 namespace 或业务副本。管理员在已有 namespace 预置共享组件；开发者命令仅读取配置和建立 port-forward。

## 构建与验证

Go 1.22 或更高版本；管理员模块使用固定版本 YAML 解析库，源码和许可证已放入 vendor，构建可禁用网络：

```bash
cd devctl
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off go test ./...
go vet ./...
make build
```

Windows 使用 `scripts/build.ps1` 构建，再运行 `scripts/package.ps1` 生成包含四个平台二进制、配置和脚本的管理员交付包 `dist/devctl-admin-kit.zip`。`make build` 生成 Windows amd64、Windows arm64、Linux amd64/arm64 和 SHA256SUMS。示例、部署模板与文档应随 EXE 一起交付。

可选设置 `DEVCTL_HELM_VALIDATOR`、`DEVCTL_SINGBOX_VALIDATOR` 为离线验证器的绝对路径，测试会校验 Chart 渲染和 sing-box 实际配置解析；不会创建 TUN 或访问集群。

`test/integration_test.go` 使用 Go 编译的假 kubectl、Telepresence 和 HTTP 应用，验证真实 CLI/后台进程/配置注入/测试/退出流程。它**不证明 Windows TUN 或真实 Istio 兼容性**；现场执行 [验收流程](docs/acceptance.md)。

## 管理员离线部署

新增 `bundle prepare/verify`、`admin discover/plan/apply/verify/export/remote`。Windows 无 Docker 准备固定版本工具和镜像归档，使用 JSON 配置经 SSH 上传到 Linux 节点，部署到已有 kube-system。

参见 [完整离线部署](docs/admin.md)、[管理员 JSON 字段](docs/admin-config.md)、[gde-adapter 本地调测](docs/gde-adapter.md)、[配置项获取与精简指南](docs/gde-adapter-config.md)。入口为 scripts/prepare-offline.ps1、deploy-remote.ps1 和 deploy-offline.sh。示例配置位于 examples/admin；示例地址和资源名必须改为实际值。

CI/本地测试可额外设置 `DEVCTL_TP_CHART` 指向官方 Telepresence 2.31.0 Chart；测试会检查真实 Chart 的 schema、镜像、离线 hook 和调度边界。SSH/集群状态使用测试替身，实机验收仍须在现场执行。

## 开发者快速开始

1. 管理员先完成 [共享组件部署](docs/admin.md)。
2. 本机安装 JDK 21；安装该接入后端需要的离线工具。
3. 复制 `examples/order.json` 为 `order.local.json`，填写实际 context、已有 namespace、工作目录、配置键、端口及测试命令。
4. 逐项确认消费者、定时任务、自动迁移的本地行为，再将 `backgroundTasksReviewed` 改为 `true`。示例故意默认不通过，不能直接套用到未知服务。
5. Maven/Gradle wrapper、JDK、插件和依赖必须来自已有内网仓库或离线缓存。工具不会自动下载依赖。

```powershell
.\devctl.exe validate --profile .\order.local.json
.\devctl.exe doctor --profile .\order.local.json --format json
.\devctl.exe connect --profile .\order.local.json
.\devctl.exe run --profile .\order.local.json
.\devctl.exe probe --profile .\order.local.json
.\devctl.exe test --profile .\order.local.json --suite smoke --format json
.\devctl.exe status --format json
.\devctl.exe stop --profile .\order.local.json
.\devctl.exe disconnect
```

`connect` 启动本地后台管理进程，终端随后可关闭。`run` 等待健康检查通过后返回；应用持续运行。浏览器/Postman 访问本地业务端口；IDE 用 Remote JVM Attach 连接 `127.0.0.1:5005`。示例用 Maven `spring-boot:run` 自动编译并启动，无需复制 Jar 到集群。

IDE 配置示例在 `examples/intellij-devctl.run.xml`（复制到项目 `.run/`）和 `examples/vscode-launch.json`（合并到 `.vscode/launch.json`）。端口应与启动清单一致。

修改代码后 `stop` → `run`；更改启动清单或集群配置后，先 `stop`，再 `connect` 刷新配置。`run/test` 使用管理进程中的已准备快照，不会静默读取新的配置版本。

一个用户默认一个网络会话，可 `connect` 多个具有相同网络设置的服务清单；每个应用使用独立端口。`stop` 只停止指定应用；`disconnect` 停止全部管理中的应用、测试及隧道。多人的管理进程分别在各自电脑上。

## 两种接入后端

### Telepresence：可以本地访问 Kubernetes API

`network.backend=telepresence`，`network.transport=kubectl`。预装 Telepresence OSS 2.31.0，并由管理员确认专用出口 Pod 中已有 Traffic Agent。devctl 调用 `connect --proxy-via all=<共享出口>`；不会调用 `intercept`、`replace`、`ingest` 或 Helm 安装。

已有手工 Telepresence 会话时拒绝抢占；先自行断开该会话。工具使用自己的配置文件关闭使用上报和本地调用捷径，退出仅断开它取得的连接，不执行全局 `quit --stop-daemons`。

### Gateway：本地或远端 kubectl 均可

`network.backend=gateway`，安装 Chisel 1.10.1 和 sing-box 1.12.x；本机 TUN 需要管理员权限。管理员给出 gateway 的 Chisel 公钥指纹和个人认证信息：

```powershell
# 使用你们现有的凭据交付方式设置，仅示意格式；勿提交到源码。
$env:DEVCTL_GATEWAY_AUTH = 'user:password'
.\devctl.exe connect --profile .\order.local.json --transport ssh --ssh-host dev-node
```

`--transport ssh` 自动选择 gateway 后端。SSH 身份、端口、ProxyJump 写入本机 OpenSSH 配置，使用经过确认的 host key；只支持无交互认证，不自动接受未知主机指纹。远端主机须有对应 context 的 kubectl。

```text
本地 kubectl：TUN → Chisel → 本地 port-forward → 共享 Chisel Pod
远端 kubectl：TUN → Chisel → SSH -L → 远端 port-forward → 同一共享 Pod
```

Windows 无需 Kubernetes 凭据；Kubernetes 读取和转发通过远端 SSH 命令完成。认证值经子进程环境传给 Chisel，不出现在参数列表中。不建立反向转发。

v1 的 gateway 后端只代理 **IPv4 TCP 应用流量**，DNS 经专用 TCP 通道转发；集群 CIDR 内其他协议明确拒绝。`routeCIDRs` 必须涵盖实际 Service/Pod/中间件公布地址，禁止默认路由。`bypassCIDRs` 应包含 SSH/API 入口；`fallbackDNS` 是可从本机直接访问的企业 DNS，集群域名走集群 DNS。不要填公共 DNS。工具发现本地网卡网段重叠时拒绝启动；额外 VPN 路由仍需现场检查。

sing-box 自身管理 TUN 路由和接口 DNS，devctl 不写 hosts、不修改持久化 DNS 注册表。先启动转发及 Chisel，验证配置后启动 TUN；退出顺序相反。丢失子进程会触发重连；上游链路断开由 Chisel 的 keepalive/reconnect 处理。隧道存活不等于每个依赖可用，使用 `probe` 和真实功能测试确认。

## 配置边界与秘密处理

参见 [启动清单说明](docs/profile.md)。只导出 `envAllow` 明确列出的变量。支持 envFrom、显式 env、ConfigMap/Secret key 引用、Kubernetes `$(VAR)` 展开以及 `metadata.namespace`。Pod IP、Pod 名称、resourceFieldRef 需要显式本地覆盖。ServiceAccount Token Secret 被拒绝；投射 Token/PVC/容器文件系统不自动复制。

必要的配置文件通过 `source.files` 显式映射，不自动读取整个容器或整个 namespace。实际镜像 ENV、入口脚本生成的配置、复杂挂载来源和动态凭据续期需要在接入该服务时明确处理。读取失败会中止准备，即使引用标记为 optional；不会把权限错误误当作可忽略资源缺失。

环境只进入应用/测试子进程。默认不继承宿主 `SPRING_*`、`JAVA_TOOL_OPTIONS` 等可能覆盖快照的变量；确需继承时使用 `application.inheritEnv`。服务级覆盖保持在同一环境来源，环境变量会优先于挂载的 Spring YAML。清单可以显式提供 `SPRING_APPLICATION_JSON`，其更高优先级及应用命令行参数需由服务维护者一起检查。

状态默认在用户缓存目录的 `devctl` 下；Linux 权限 0700/0600，Windows 使用当前用户 SID 限制目录 ACL。配置文件在断开时删除，Secret 和环境快照驻留管理进程内存。日志对已知 Secret、导出值进行脱敏；不能保证识别应用自行编码/拼接后输出的一切敏感内容，应用仍不应输出凭据。

会话 IPC 只监听 loopback，使用私有状态文件中的随机 bearer token；不提供无认证控制端口。状态/报告不包含配置值。Windows 使用 Job Object 管理进程树；Unix 使用进程组。异常断电/SIGKILL 后可能留下锁，按 [恢复流程](docs/acceptance.md) 检查后处理，不按陈旧 PID 盲目杀进程。

## 已实现与现场前提

已实现管理员离线物料和 SSH 部署、CLI、两种网络后端编排、SSH 远端读取、环境和配置文件快照、受控本地进程树、健康检查、命令测试、结构化报告、离线构建及管理员 Chart。

尚未在本交付环境验证：真实 Windows 11 TUN/企业 VPN 组合、真实 Telepresence 服务、你们的 Istio 1.28.3/CNI 策略、真实 GaussDB/Kafka/Redis 等协议。没有提供集群访问参数，因此未部署共享组件，也未连接或变更现有集群。上线前需通过现场验收。
