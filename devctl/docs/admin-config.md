# 管理员 JSON 配置参考

JSON `version` 当前为 1；未知字段和尾随内容报错。PowerShell UTF-8 BOM 可读取。用户填写的是配置，不是 shell 脚本；SSH 远端参数逐项进行 POSIX quoting。

## bundle.json

| 字段 | 内容 |
|---|---|
| output / cache / distribution | 输出目录、下载缓存、devctl 分发目录；相对于 JSON 文件 |
| artifacts | 所有工具或 Chart 的来源与包内目标路径，按顺序准备；crane 必须先于 OCI Chart |
| artifacts[].id / target | 日志标识、包内相对路径；禁止路径穿越、绝对路径和 symlink |
| url / file | HTTPS 源文件或已有本地归档；file 相对于 JSON 所在目录 |
| sha256 | 上游归档的 SHA256，提供后优先使用；本地 file 必填 |
| checksumURL | 官方单文件摘要或 checksums.txt；根据源文件名匹配 |
| githubAssetAPI | 固定版本 GitHub Release API，通过同名 asset 的 digest 校验 |
| format / member | file、gz、tar.gz、zip；压缩包中精确的可执行文件路径 |
| helmOCI | 固定版本 Chart OCI 引用；使用已校验 crane 获取 manifest 与 Chart 内容层 |
| helmIndexURL / chartName / chartVersion | 可选传统 Helm 索引来源；默认 Telepresence 使用 OCI |
| images[].name / source / platform / file | 内部标识、来源镜像、linux/amd64 或 linux/arm64、归档目标路径 |

内置安装逻辑使用 chisel/tel2/busybox/curl 四个镜像标识。修改标识不是换源方式，换源应修改 source。安装只使用锁文件，忽略原来的公网来源。

包内路径固定约定：`bin/<os>-<arch>/<tool>`、`charts/telepresence-upstream.tgz`、`charts/telepresence.tgz`、`deploy/gateway`。可执行文件来源于显式 artifact；devctl 本身从 distribution/dist 复制。

`bundle.lock.json` 记录文件 SHA256、上游来源及摘要，以及镜像 sourceDigest、digest、configDigest 和缓存引用。既拒绝摘要不符，也拒绝未列入锁的额外文件。锁文件用于完整性与复现，传输链路和来源信任仍由现有交付渠道保证，并不提供额外的数字签名身份。

## install.json

| 字段组 | 说明 |
|---|---|
| namespace | 当前固定 kube-system，必须已经存在 |
| context / kubeconfig / kubectl | Linux 执行机的访问配置；kubectl 留空时选择包内二进制 |
| bundle / workDir | 本次调用所在机器的包路径、受限工作目录 |
| remoteDir | SSH 管理节点上的绝对工作目录，只接受简单路径；remote 命令重写远端 bundle/workDir |
| ssh | Windows 到管理节点的连接配置；host、port、identityFile、knownHostsFile、proxyJump、sudo |
| nodes | Kubernetes Node 名称、arch、节点 ssh、runtime、socket、ctr 路径；remote 执行时在管理节点解释节点 SSH 配置 |
| imageMode | registry 或 nodes |
| registry.prefix | 指定内网 registry host/project，不包含 https:// |
| registry.configFile / pullSecret | 私有 Docker 认证 JSON 路径、用于 Pod 的 Secret 名；不需要 Docker 进程 |
| backends | `["telepresence","gateway"]` 或单一后端；Telepresence 模式仍使用 Chisel 出口部署作为 Agent 宿主和验证入口 |
| managerRelease / gatewayRelease | Manager 与出口 Release 名；默认 traffic-manager / dev-egress |
| reuseManager | 复用已有的匹配版本 Manager，只检查、不升级它 |
| credentials | mode=generate 或 existingSecret、Secret 名、个人 users 列表 |
| developerGroups | 已有 Kubernetes Group 名称；连接权限和 named-resource 配置权限 |
| configMaps / secrets | 允许读取的业务配置资源名；按真实 envFrom/env/valueFrom/files 填写 |
| businessDeployment / businessContainer | gde-adapter 及对应主容器，副容器不作为来源 |
| developerProfile | 本地 JVM 启动模板；网络与业务来源字段自动填入，应用配置不被猜测 |
| developerSSHHost / developerContext | 导出给开发者的 SSH alias 与 context；与管理员访问身份分开 |
| domain / clusterDNS | 集群 DNS 域与 DNS Service IPv4 |
| routeCIDRs / bypassCIDRs / fallbackDNS | 集群路由、SSH/API 绕行地址和直接可达的企业 DNS |
| dependencies | name、namespace、service、Service port，可选显式 Pod selector；默认读取 Service selector/targetPort |
| extraEgress | 根据现有 CNI、ztunnel、waypoint 和其它 API 需要添加的 NetworkPolicy egress 项 |
| tolerations | 共享组件必须运行在有污点节点时显式配置；默认不容忍所有污点 |
| istioNamespace / cniDaemonSet / cniConfigMap | 现场现有 Istio CNI 位置，必须读取实际配置验证 |
| timeoutSeconds | 10～3600，默认 300，用于 rollout、安装及恢复 |

不支持的内容：创建 namespace、安装数据库、任意 shell 导入命令、修改 Istio、自动识别所有自定义任务框架、接管已有非 devctl 管理的 Release。selector-less／外部数据库需要先在你们既有接入方式中明确目标策略，本版 Service 示例要求可明确选择目标 Pod。

## 命令输出

`discover`：节点、Service、业务环境变量名称及资源引用，不包含环境变量值。

`plan`：预检结果、解析后的节点、两个 Chart 的 values、计划步骤；用临时目录渲染，不修改集群。

`apply`：执行预检、分发、安装、验证、导出；输出成功状态、配置摘要、依赖 TCP 结果及 handoff 位置。

`verify`：只检查已有安装并建立短时本地探测隧道，不重建凭据或应用 Helm 更新。

`export`：读取已有凭据的公钥指纹并填充开发者启动模板，不修改集群。个人密码文件在 apply 阶段由管理员领取。

`remote`：通过 SSH 执行上述 operation。只向 stdout 输出一个 JSON 结果；执行进度进入 stderr。返回非零即失败，不将恢复失败报告为成功。
