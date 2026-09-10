# 管理员：Windows 准备物料，SSH 自动部署到离线 Kubernetes

本流程为 devctl 的共享接入组件提供离线安装、验证和恢复。业务示例为 **Deployment `kube-system/gde-adapter`**，依赖已有 gaussdb、redis、minio。所有调测组件也使用已有 `kube-system`；开发者不创建 namespace，不替换业务 Pod。

## 1. 执行环境与交付内容

联网准备机：Windows 11、PowerShell 5.1+、devctl.exe，无需 Docker、WSL 或 Go。Windows 自带 OpenSSH 用于后续上传，SSH 密钥与已确认的 host key 预先配置。构建源码才需要 Go。

离线执行机：可通过 SSH 登录的 Linux 节点，具备现有 kubeconfig/context；节点镜像导入使用 root 或 `sudo -n`。Shell、tar、sha256sum 和 SSH/SCP 是节点前提；Helm、crane、kubectl、crictl、Chisel、istioctl 来自物料包。不会运行 apt/yum、在线 Helm repo update 或公网镜像拉取。

默认物料清单面向 Windows amd64＋Linux amd64。ARM64 使用对应官方资产/member，并将镜像 platform 改为 linux/arm64。一个安装配置的候选节点必须同架构；混合架构集群选一个架构的节点子集，避免错误的镜像与调度组合。

JSON 示例：`examples/admin/bundle.json`、`install.registry.json`、`install.nodes.json`。请复制为 `*.local.json` 并保持目录位置；移动配置文件时，相对路径相对于该 JSON 文件重新解释。完整字段见 [管理员配置说明](admin-config.md)。

## 2. 版本、镜像和下载来源

以下是本实现固定的工具组合，不代表整个组合已经通过你们现场环境验收。

| 物料 | 固定版本 | 下载方式与用途 |
|---|---|---|
| Telepresence Windows 客户端、Manager/Agent | 2.31.0 | [官方 Release](https://github.com/telepresenceio/telepresence/releases/tag/v2.31.0)；镜像 `ghcr.io/telepresenceio/tel2:2.31.0` |
| Telepresence Chart | 2.31.0 | OCI `ghcr.io/telepresenceio/telepresence-oss:2.31.0`；自动读取 manifest 和 Chart 内容层，校验 digest |
| Chisel 客户端、服务器镜像 | 1.10.1 | [官方 Release](https://github.com/jpillora/chisel/releases/tag/v1.10.1)；`docker.io/jpillora/chisel:1.10.1` |
| sing-box Windows | 1.12.0 | [官方 Release](https://github.com/SagerNet/sing-box/releases/tag/v1.12.0)，仅用于本机 Gateway/TUN 后端 |
| Wintun | 0.14.1 | [官方发布页](https://www.wintun.net/)，校验发布页 SHA256，DLL 与 sing-box.exe 放在同一工具目录 |
| Helm | 3.19.0 | [官方 Release](https://github.com/helm/helm/releases/tag/v3.19.0)；get.helm.sh 文件及摘要 |
| crane | 0.20.3 | [官方 Release](https://github.com/google/go-containerregistry/releases/tag/v0.20.3)，原生 Windows/Linux，无 Docker 镜像归档与推送 |
| kubectl | 示例 1.34.0 | dl.k8s.io；现场将 JSON 中下载 URL 与校验 URL 一并改为匹配集群的 1.34 patch |
| crictl | 1.34.0 | [官方 Release](https://github.com/kubernetes-sigs/cri-tools/releases/tag/v1.34.0)，在实际 CRI socket 检查镜像 |
| istioctl | 1.28.3 | [官方 Release](https://github.com/istio/istio/releases/tag/1.28.3)，供管理员进一步排查 |
| Helm hook 镜像 | busybox 1.36.1、curl 8.1.1 | `docker.io/library/busybox:1.36.1`、`docker.io/curlimages/curl:8.1.1` |

物料下载地址、归档成员路径、摘要来源均在 bundle JSON 中显式列出，可提前替换企业镜像站。提供本地 `file` 时必须同时提供该归档的 `sha256`。指定 SHA256 后可完全复用预下载文件，不必调用公网摘要 API。

默认镜像 tag 只用于联网解析：归档记录 sourceDigest、导入后的 manifest digest、config digest 和文件 SHA256。镜像格式转换可能改变 manifest digest，因此不会把原始远端 digest 误当作归档 digest。

**Chart 特别处理**：官方 OCI Chart 保留为 `charts/telepresence-upstream.tgz`。2.31.0 的测试 Pod 缺少明确的 pullPolicy 和调度约束；准备程序生成带这两个补丁的 `charts/telepresence.tgz`，两者分别进入锁文件。Helm 不把所有 hook 交给 post-renderer，因此不能只依赖 post-renderer 修复 hook。

这些镜像不包括 gaussdb、redis、minio：它们是既有依赖，不重新部署。JDK 21、项目 Maven/Gradle 分发包、依赖缓存、企业 CA、GaussDB 驱动也应按业务项目单独准备。无挂载功能的 Telepresence 连接不要求安装 SSHFS；不使用会在离线阶段补下载组件的在线安装器。

## 3. 联网 Windows 准备离线包

先确认使用的是包含管理员命令的新版本 devctl。源码构建后，将 `dist`、deploy、docs、examples、scripts 与 README 一起作为 `distribution`；源码构建命令见 README。

```powershell
Copy-Item .\examples\admin\bundle.json .\examples\admin\bundle.local.json
# 按实际平台、内网源、版本和目录提前编辑 JSON。
.\scripts\prepare-offline.ps1 -Config .\examples\admin\bundle.local.json -Devctl .\dist\devctl-windows-amd64.exe
.\dist\devctl-windows-amd64.exe bundle verify --bundle .\.offline\bundle --format json
```

### 3.1 PowerShell 代理

在运行脚本的**同一个 PowerShell 窗口**设置进程环境变量。`$HTTPS_PROXY = ...` 是普通 PowerShell 变量，子进程不会继承；`setx` 设置的是后续新进程的环境，也不会更新当前窗口。请使用 `$env:` 写法（[Microsoft 环境变量说明](https://learn.microsoft.com/en-us/powershell/module/microsoft.powershell.core/about/about_environment_variables)）。

```powershell
$env:HTTP_PROXY = 'http://127.0.0.1:7890'
$env:HTTPS_PROXY = $env:HTTP_PROXY
$env:NO_PROXY = 'localhost,127.0.0.1,::1,.corp.example'
.\scripts\prepare-offline.ps1 -Config .\examples\admin\bundle.local.json
```

端口替换为代理实际的 HTTP / mixed 监听端口。`HTTPS_PROXY` 表示访问 HTTPS 目标时使用的代理，值通常仍是 `http://...`，通过 CONNECT 建立 TLS 隧道；只有代理监听端口本身支持 TLS 时才写 `https://...`。SOCKS 端口须写 `socks5://...`，不要把 SOCKS 端口当作 HTTP 端口。

脚本读取当前进程的 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY`（兼容小写），修剪首尾空白，为 `host:port` 补 `http://`；只设置 HTTP_PROXY 时将其用于 HTTPS。devctl 与下载镜像/Chart 的 crane 继承同一组变量。脚本结束或失败后恢复当前窗口原来的代理环境。直接调用 devctl 时遵循 [Go 标准代理规则](https://pkg.go.dev/net/http#ProxyFromEnvironment)，HTTPS 下载请明确设置 HTTPS_PROXY。

也可显式指定本次脚本的代理；`-Proxy` 优先于环境变量。`-NoProxy` 可覆盖旁路列表，传空字符串表示本次不使用环境中的旁路列表（Go 仍默认直连 localhost/回环地址）：

```powershell
.\scripts\prepare-offline.ps1 -Config .\examples\admin\bundle.local.json -Proxy 'http://127.0.0.1:7890' -NoProxy ''
```

认证代理支持 `用户名:密码@主机:端口`：

```powershell
.\scripts\prepare-offline.ps1 `
  -Config .\examples\admin\bundle.local.json `
  -Proxy 'http://myuser:mypassword@proxy.example.com:8080'
```

将端口替换为实际值；HTTP 代理省略端口时默认使用 80。用户名或密码包含 `@`、`#`、`%` 等特殊字符时，先分别 URL 编码，再拼接 URL，例如：

```powershell
$proxyUser = [Uri]::EscapeDataString('myuser')
$proxyPassword = [Uri]::EscapeDataString('p@ss:word#')
$proxyUrl = 'http://{0}:{1}@proxy.example.com:8080' -f $proxyUser, $proxyPassword
.\scripts\prepare-offline.ps1 -Config .\examples\admin\bundle.local.json -Proxy $proxyUrl
```

默认优先使用交付包根目录 `devctl.exe`，其次使用源码 `dist/` 对应 Windows 架构程序，最后查 PATH；`-Devctl` 可以显式指定。更新时请同时替换脚本和程序。脚本会在 stderr 输出实际程序路径及代理端点，下载器会输出每次请求（包括重定向）的目标主机和 `via proxy` / `via direct`；stdout 保持 JSON，日志不打印代理用户名、密码或签名下载 URL。

- 显示 `via direct`：检查 `$env:` 是否设置在当前窗口、HTTPS_PROXY 是否为空，及 NO_PROXY 是否含 `*`、github.com、其 API/CDN 或镜像仓库域名。
- 显示 `via proxy` 仍失败：检查端口和协议。`407` 表示代理要求认证，`connection refused` 表示代理端点未接受连接；证书错误需要正确安装企业 CA，程序不会跳过 TLS 校验。
- GitHub 下载可能重定向到其他资产域名；还需要访问 api.github.com、ghcr.io、Docker Hub，以及 JSON 中其他下载站点，不能只放通 github.com 首页。
- 脚本不自动读取浏览器代理/PAC，也不会把本机代理变量部署到离线节点。失败重试时按下述规则使用空输出目录，已有校验下载缓存可保留。

准备结果为目录：`bin/`、`charts/`、`images/`、`deploy/`、`docs/`、`examples/`、`scripts/`、`bundle.lock.json`、`SHA256SUMS`。公共包不应放 SSH 私钥、kubeconfig、仓库密码或业务 Secret。

只接受 HTTPS 下载，校验官方 checksum、GitHub asset digest 或显式配置的 SHA256；若上游未提供可用摘要，停止并要求配置可信摘要。生成本地 SHA256 不等于验证上游真实性。传入内网后可再次执行 `bundle verify`。

输出目录必须为空，避免把上次失败或其他配置的残留文件混入包。下载缓存可保留；失败重试使用新的输出目录或在确认后清理失败输出。修改第三方版本需要同步更新归档成员、镜像、Chart 兼容检查，不能只改某个字符串。

## 4. 预置安装 JSON

共同填写：

- `context`、Linux 上的 `kubeconfig`，留空 `kubectl` 则使用包内版本。
- `ssh`：管理节点的 SSH alias、端口、身份/known-hosts 路径、可选 ProxyJump、sudo。
- `nodes`：允许调度共享组件的真实 Kubernetes Node 名称、架构及 SSH 配置。
- namespace 固定 kube-system；Manager 默认 `traffic-manager`，出口默认 `dev-egress`。
- `credentials.users`：个人账号名称。首次自动生成独立密码与固定服务器密钥；也可使用 existingSecret。
- `developerGroups`：你们已存在的 Kubernetes 用户组；不会创建登录账号或复制管理员 kubeconfig。
- `configMaps`、`secrets`：gde-adapter 真正引用的资源名；不是泛化的 namespace 读取权限。
- `dependencies`：真实中间件 Service、namespace 和 Service 端口；自动解析 selector/targetPort 形成出口规则。
- `clusterDNS`、域名、Service/Pod CIDR、SSH/API bypassCIDR、企业 fallbackDNS。
- `extraEgress`：按真实 CNI/ambient/waypoint 路径补充必要规则；不自动放行整个 kube-system。

`admin discover` 只读取节点、Service 和业务配置引用，不返回环境变量值或 Secret 内容。返回内容用于补充 JSON；地址、业务语义、认证信息不能可靠自动推断。

```powershell
.\scripts\deploy-remote.ps1 -Config .\examples\admin\install.nodes.local.json -Operation discover -Devctl .\dist\devctl-windows-amd64.exe
```

远程执行时，管理节点 `ssh` 的文件路径在 Windows 解释；`nodes[].ssh` 的路径和 SSH alias 在 Linux 管理节点解释，管理节点必须能够连接所列工作节点。也可在 nodes 的 SSH 配置中指定跳板。`remoteDir` 使用不含空格的绝对路径，工作文件放入权限受限的目录。

### 4.1 内网仓库模式

从 `install.registry.json` 复制，设置 `imageMode=registry`、`registry.prefix`。需要认证时提供 Docker 格式的私有认证文件路径 `registry.configFile`；它是 crane 的认证配置，不要求安装 Docker。配置 `pullSecret` 用于 Pod 拉取。

程序把归档推送到指定内网前缀，检查目标 digest，Manager、Agent、Chisel 和 hook 全部使用内网引用。节点必须已信任内网仓库 TLS，不能依赖脚本修改 containerd/Docker 配置或重启运行时。

### 4.2 节点导入模式

从 `install.nodes.json` 复制，设置 `imageMode=nodes`。自动识别先检查 kubelet 的实际 runtime endpoint，再验证运行时连接，不以机器上存在 docker 命令作为判据。

标准 containerd 使用实际 socket 和 `k8s.io` 镜像空间。自定义路径、k3s/RKE2 环境需要显式提供实际 `socket` 和可用的 `ctr` 路径；Docker 必须通过真实 cri-dockerd endpoint 验证。未能确认时停止，不按猜测导入。

导入后通过该节点的 CRI 检查 config digest／manifest digest。Docker 不支持给镜像设置 digest 格式的 tag，因此节点模式使用锁文件中的 `devctl.local/...:<内容摘要>-<架构>` 标签和 `Never`，并检查实际内容。Manager、出口及 hook 限制在已准备的 nodes 上；新节点加入不会自动扩展此范围。

## 5. kube-system 与 ambient 的前置条件

Istio 1.28.3 默认 CNI 排除列表包含 kube-system。预检读取你配置的 CNI ConfigMap 和 DaemonSet；排除仍存在、无法辨认配置或 ambient 未开启时停止，不部署组件。错误提示交由 Istio 管理员处理。不要对整个 kube-system 加 ambient 标签，不自动升级或重启 Istio。

修复应由网格管理员针对现有安装方式保留原配置、修改实际 CNI 排除列表并验证，随后重复执行 `plan/apply`。仅出口 Pod 使用 ambient 标签；验证要求实际 `ambient.istio.io/redirection=enabled` 和容器就绪。必要时进一步用包内 istioctl 检查 ztunnel workload 与策略。

Telepresence 显式管理 `namespaces: [kube-system]`，Webhook 只选 `devctl.io/shared-egress=true`。凭据卷设置 Ignore，不允许 Traffic Agent 挂载导出。已有 Manager 必须版本匹配；使用 `reuseManager=true` 时仍校验管理范围和卷排除策略，不自动接管旧 ldbg 的安装。

命名资源配置读权限与连接权限分开。Kubernetes 普通 RBAC 的 pods/portforward 权限不能按标签限制到出口 Pod，因此该权限的实际边界是 namespace；在 kube-system 使用时须沿用现有身份与准入边界。

## 6. 自动部署、报告与恢复

```powershell
# 可选：只读集群预检和 Chart 渲染，不安装。
.\scripts\deploy-remote.ps1 -Config .\examples\admin\install.registry.local.json -Operation plan -Devctl .\dist\devctl-windows-amd64.exe
# 非交互执行；内部仍会重新预检，无需手工拆分步骤。
.\scripts\deploy-remote.ps1 -Config .\examples\admin\install.registry.local.json -Operation apply -Devctl .\dist\devctl-windows-amd64.exe
```

脚本上传物料并核对 SHA256，运行远端 devctl，取回结构化报告和 handoff。管理员已通过 SSH 登录 Linux 时，可以把包解压到现场目录，在 install JSON 中填写本机路径后运行：

```sh
./scripts/deploy-offline.sh /secure/install.local.json plan
./scripts/deploy-offline.sh /secure/install.local.json apply
./scripts/deploy-offline.sh /secure/install.local.json verify
```

同一工作目录使用部署锁；相同 Helm values 不触发无意义升级。凭据 Secret 一旦存在则复用，新增用户不会通过重跑脚本隐式轮换已有账号。

安装失败尝试恢复本次改动的 Helm Release：升级回退前一 revision，首次安装撤回本次组件。凭据和已分发镜像保留供重试；不删除业务资源。`failure.json` 记录恢复结果，恢复失败仍返回失败。异常断电留下 apply.lock 时先确认没有执行中的部署，再人工移除锁；不要同时从不同管理节点操作同一 Release。

`verify` 检查出口调度、镜像、Agent 卷排除、ambient 注解、Manager 范围，并建立短时 localhost port-forward＋Chisel SOCKS 通道，经真实出口解析依赖域名和连接 TCP 端口。这个结果不表示数据库认证或业务逻辑成功，后者由本地 JVM 的 smoke 验收。

## 7. 开发者交付

handoff 包含两种 `gde-adapter` 启动清单及按账号分开的 `*.credentials.json`。每人只领取自己的凭据文件，不能把整个管理员 handoff 共享给全员。Chisel 私钥只保留在集群 Secret 和管理员受限目录，不放入开发者启动清单。

开发者设置自己的 SSH alias、访问 context、源码目录及业务配置绑定，按 [gde-adapter 示例](gde-adapter.md)运行。Windows 本地接收的 handoff 目录限制为当前用户 ACL。

现场验收与自动化测试分开记录。本仓库的测试替身可以验证命令、幂等和恢复流程，但不能证明你们真实 CNI、Windows VPN、网格策略或中间件已经通过。
