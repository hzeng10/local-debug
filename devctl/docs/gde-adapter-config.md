# gde-adapter 配置项：来源、查询命令与可省略范围

适用于 devctl v0.2.2（配置行为与 v0.2.1 相同）、Kubernetes 1.34，以及本仓库提供的 Telepresence / Gateway 两种接入方式。本文按当前程序校验与运行行为说明，不把 JSON 中的示例地址当成实际集群配置。

推荐由管理员交付已导出的 `gde-adapter.kubectl.json` 或 `gde-adapter.ssh.json`，开发者只维护自己的源码路径、业务配置绑定和本机 DNS/访问入口。已有业务集群并不意味着已经部署 devctl 的共享出口；若出口组件尚未安装，需要先完成管理员部署流程。

## 1. 哪些配置必须填写

`backend` 决定必需的网络字段。`transport=ssh` 会强制选择 Gateway；不能通过保留 `backend=telepresence` 来免填 Gateway 参数。

| 字段 | 实际含义与来源 | Telepresence + kubectl | Gateway + kubectl/ssh |
|---|---|---|---|
| `gatewayNamespace` | 共享出口 Deployment 所在 namespace，由管理员提供 | 必填 | 必填 |
| `gatewayDeployment` | 共享出口 Deployment 名，例如 dev-egress，不是 gde-adapter 或 Istio ingress gateway | 必填，供 `--proxy-via all=<Deployment>` 使用 | 必填，供 port-forward 使用 |
| `managerNamespace` | Telepresence Manager 所在 namespace | 必填 | 可省略 |
| `authEnv` | Windows 进程环境变量的名字，默认交付约定为 DEVCTL_GATEWAY_AUTH | 可省略 | 必填；connect 时该变量必须有值 |
| `fingerprint` | 管理员交付的 Chisel 服务器公钥指纹 | 可省略 | 必填；当前校验要求 44 字符，运行时核验服务器身份 |
| `routeCIDRs` | 需要通过本地 TUN 送入共享出口的 IPv4 目的网段 | 可省略，当前后端不使用此字段 | 必填、非空 |
| `bypassCIDRs` | 需要保留本地/VPN 路径的 IPv4 地址或网段 | 可省略 | 可省略或 `[]`；有实际路由需要时填写 |
| `clusterDNS` | 共享出口能通过 TCP/53 访问的集群 DNS 地址 | 可省略 | 必填、IP 地址 |
| `fallbackDNS` | Windows 本机能访问的企业/VPN DNS，用于非集群域名 | 可省略 | 必填、IP 地址；当前没有自动选择默认值 |
| `dnsSuffixes` | 额外送往集群 DNS 的域名后缀；cluster.domain 自动包含 | 可省略 | 可省略或 `[]` |
| `source.envAllow` | 允许从目标容器配置导入的环境变量名 | 必填、非空 | 必填、非空 |
| `source.requiredEnv` | 导入及 overrides 完成后，必须非空的变量名 | 可省略或 `[]` | 可省略或 `[]` |
| `source.overrides` | 本地启动时新增/覆盖的环境变量 | 结构上可省略 | 结构上可省略 |
| `source.files` | 显式复制 ConfigMap/Secret 中的文件内容 | 不用文件配置时可省略 | 不用文件配置时可省略 |

`overrides` 是否能实际省略取决于应用：如果需要改变绑定地址/端口、容器路径、配置文件位置或后台任务开关，就必须通过它或项目已有的本地配置实现对应行为。

当前 Gateway 只支持本实现的 IPv4 TCP 路径；不能填写 IPv6 CIDR 或 `0.0.0.0/0`。Telepresence 下若仍保留 routeCIDRs/bypassCIDRs，程序也会校验其格式；不用的字段直接删除比填占位符更清楚。

## 2. 查询前确认访问方式

以下查询使用 Windows PowerShell，变量名可以按实际环境修改：

```powershell
$ctx = 'internal-test'
$ns = 'kube-system'
$app = 'gde-adapter'
kubectl config get-contexts
kubectl --context $ctx -n $ns get deployment $app
```

如果 Windows 没有 kubeconfig、只能 SSH 到 Linux 管理节点，在该节点使用相同的 kubectl 查询，或通过 SSH 获取 JSON：

```powershell
$raw = ssh developer-node 'kubectl --context internal-test -n kube-system get deployment gde-adapter -o json'
if ($LASTEXITCODE -ne 0) { throw 'Cannot read remote deployment' }
$dep = ($raw -join "`n") | ConvertFrom-Json
```

SSH 模式的 cluster.context 是**远端身份**的 kubeconfig context；SSH alias、端口、密钥和跳板在 Windows 的 SSH config 中配置。开发者通常只有命名资源的 get 权限，未必能 list Deployment/Secret、读取 Nodes/ServiceCIDR/CoreDNS 或查看日志。遇到 Forbidden 时由管理员查询交付；不要把它当成资源不存在。

## 3. gatewayDeployment：定位共享出口

已知名称时直接检查：

```powershell
kubectl --context $ctx -n $ns get deployment dev-egress
kubectl --context $ctx -n $ns get deployment dev-egress -o 'custom-columns=NAME:.metadata.name,CONTAINERS:.spec.template.spec.containers[*].name,IMAGES:.spec.template.spec.containers[*].image'
```

名称未知、且有 list 权限时列出候选：

```powershell
kubectl --context $ctx -n $ns get deployments -o 'custom-columns=NAME:.metadata.name,CONTAINERS:.spec.template.spec.containers[*].name,IMAGES:.spec.template.spec.containers[*].image'
```

本仓库 Chart 的出口容器名是 `chisel`，默认 Deployment 名为 `dev-egress`。管理员安装 JSON 中的 `gatewayRelease` 会写入导出 profile 的 `gatewayDeployment`；手工安装 Helm 时应以实际渲染的 Deployment 名为准，而不是只看 Helm Release 名。

不要填业务 Deployment `gde-adapter`：Telepresence 在本实现中也通过共享出口上预装的 Traffic Agent 访问集群，Gateway 则转发到出口 Chisel 的 8080 端口。找不到共享出口时，不能靠换成某个现有业务 Pod 名称解决。

## 4. authEnv 与 fingerprint：使用管理员交付

`authEnv` 是**变量名**，不是密码，也不是 Kubernetes Secret 名：

```json
"authEnv": "DEVCTL_GATEWAY_AUTH"
```

管理员 apply 的 handoff 中，每位开发者会有自己的 `alice.credentials.json`，其内容为 `DEVCTL_GATEWAY_AUTH` 对应的 `用户名:密码`。Windows 加载方式：

```powershell
$profilePath = 'C:\devctl\gde-adapter.ssh.json'
$profile = Get-Content -Raw $profilePath | ConvertFrom-Json
$credential = Get-Content -Raw 'C:\devctl\alice.credentials.json' | ConvertFrom-Json
[Environment]::SetEnvironmentVariable($profile.network.authEnv, $credential.DEVCTL_GATEWAY_AUTH, 'Process')
# 检查是否存在，不输出凭据。
-not [string]::IsNullOrEmpty([Environment]::GetEnvironmentVariable($profile.network.authEnv, 'Process'))
```

不要把这里填成下载物料时的 `HTTP_PROXY`，两者是不同的认证。`source.overrides` 是 JVM 的环境，也不能代替 devctl connect 所需的当前 PowerShell 进程环境。

`fingerprint` 直接读取管理员提供的 profile：

```powershell
$profile.network.fingerprint
```

它是 Chisel 的服务器公钥 SHA256/Base64 指纹，不是 Kubernetes CA、SSH 主机指纹或镜像 digest；保留管理员提供的原字符串（本实现要求 44 字符，通常含结尾 `=`），不要自行加 `SHA256:` 前缀。

已有安装配置的管理员可在原 Linux 执行环境重新导出网络 profile：

```sh
/path/to/bundle/bin/linux-amd64/devctl admin export --config /secure/install.local.json --format json
```

按机器架构替换路径；结果写入该配置 `workDir/handoff/`。当前 `admin export` 从既有 Secret 推导指纹并导出 profile，**不会重新生成个人 credentials 文件**；密码文件应沿用 apply 的受限 handoff，缺失时由管理员按既有账号重新交付。不要为取得指纹而重新生成服务器私钥。

管理员也可辅助核对既有 Chisel 的启动日志（日志保留期内存在时）：

```powershell
kubectl --context $ctx -n $ns logs deployment/dev-egress -c chisel --tail=200 |
  Select-String -Pattern 'Fingerprint'
```

以可信管理员渠道导出的指纹为准；普通开发者无需读取包含服务器私钥和所有用户凭据的网关 Secret。一个共享出口的指纹通常全员相同，个人认证值不同。

## 5. routeCIDRs：查 Service 范围，再按依赖补 Pod 范围

这些是**目的地址路由**，不是节点 IP 清单，也不能从几个现有 Service 的 IP 猜测掩码。

Kubernetes 1.34 优先查询所有 ServiceCIDR 对象，包括扩容添加的范围：

```powershell
kubectl --context $ctx get servicecidrs.networking.k8s.io -o 'custom-columns=NAME:.metadata.name,CIDRS:.spec.cidrs'
```

ServiceCIDR API 的相应特性自 1.33 稳定，默认范围由名为 kubernetes 的对象反映。若 API 未启用或权限不足，由管理员核对实际 kube-apiserver 的 `--service-cluster-ip-range`、集群安装配置及已添加的 ServiceCIDR；不能把 `10.96.0.0/12` 当作所有集群的固定值。[Kubernetes 官方说明](https://kubernetes.io/docs/tasks/network/reconfigure-default-service-ip-ranges/)

Pod 网段可先查询各节点分配：

```powershell
kubectl --context $ctx get nodes -o 'custom-columns=NAME:.metadata.name,PODCIDRS:.spec.podCIDRs'
```

这些可能只是已分配节点的子网，可能为空，也不一定覆盖将来的节点；完整 Pod 地址池应由管理员结合实际 CNI/IPAM 配置确认。若使用 kubeadm，可辅助检查安装配置中的网段和集群域名：

```powershell
$cmRaw = kubectl --context $ctx -n kube-system get configmap kubeadm-config -o json
if ($LASTEXITCODE -ne 0) { throw 'Ask the administrator for the cluster network ranges' }
$cm = ($cmRaw -join "`n") | ConvertFrom-Json
$cm.data.ClusterConfiguration -split "`n" | Select-String -Pattern 'serviceSubnet:|podSubnet:|dnsDomain:'
```

kubeadm-config 不存在并不表示集群没有这些配置；k3s/RKE2、托管发行版或 CNI 自管 IPAM 需要查各自实际安装来源。

检查应用实际依赖：

```powershell
kubectl --context $ctx -n $ns get services gaussdb redis minio -o wide
kubectl --context $ctx -n $ns get endpointslices.discovery.k8s.io -l kubernetes.io/service-name=redis -o 'custom-columns=NAME:.metadata.name,ADDRESSES:.endpoints[*].addresses,PORTS:.ports[*].port'
```

按真实 Service 名替换；跨 namespace 的依赖在对应 namespace 查询。

- 只访问普通 ClusterIP Service、且客户端不会收到 Pod/外部节点地址时，可以只路由需要的 Service 范围；也可使用确认过的 Service IP `/32`，代价是 Service 重建换 IP 后必须更新。
- Headless Service（clusterIP 为 None）返回端点地址；Redis Cluster、Kafka、数据库拓扑发现也可能返回各节点地址。此时还必须覆盖客户端真正连接的地址，不能只保留引导 Service 的 IP。[Kubernetes Service 说明](https://kubernetes.io/docs/concepts/services-networking/service/#headless-services)
- 外部数据库或跨网段依赖不一定落在 Service/Pod CIDR 中；需要共享出口代理的目标网段也应显式包含，并由管理员核对出口策略。

假设管理员确认完整范围确实如下，才可以填写：

```json
"routeCIDRs": ["10.96.0.0/12", "10.244.0.0/16"]
```

本机网段/VPN 与目标网段冲突时不能盲目扩大路由。当前 devctl 会拒绝 routeCIDRs 与非 devctl 本机接口前缀重叠，即使填写 bypassCIDRs 也不会跳过此检查；需要缩小实际目的范围或由网络管理员调整访问路径。

## 6. bypassCIDRs：从 Windows 访问路径获取

这个字段通常不能只从集群获得。它表达的是 Windows 到 SSH 主机、跳板、Kubernetes API、企业 DNS 等地址应继续走本机/VPN 的路径。

先在未启动 devctl TUN 时检查：

```powershell
Get-NetRoute -AddressFamily IPv4 | Sort-Object DestinationPrefix | Format-Table DestinationPrefix,NextHop,InterfaceAlias,RouteMetric
Get-NetIPAddress -AddressFamily IPv4 | Format-Table InterfaceAlias,IPAddress,PrefixLength
ssh -G developer-node | Select-String -Pattern '^(hostname|port|proxyjump) '
```

kubectl 模式下，可读取**所选 context** 的 API 地址（不使用 --raw）：

```powershell
$kubeRaw = kubectl --context $ctx config view --minify -o json
if ($LASTEXITCODE -ne 0) { throw 'Cannot read selected context' }
$kubeView = ($kubeRaw -join "`n") | ConvertFrom-Json
$api = [Uri]$kubeView.clusters[0].cluster.server
$api.Host
# 如果是域名，解析该域名实际使用的 IPv4 地址。
Resolve-DnsName $api.DnsSafeHost -Type A
```

SSH 模式没有本机 kubeconfig 时，重点检查 SSH HostName / ProxyJump 的实际地址；远端访问 API 的地址不必自动列为 Windows 的直连目标。

如果这些连接的目标都在 routeCIDRs 之外，通常可以省略或填 `[]`。只有需要显式保留直连路径时才加入实际 `/32` 或精确网段。不要把整个企业网段一概加入 bypass，导致其中的数据库流量也绕过共享出口。示例 `192.0.2.10/32` 是文档地址，不能原样使用。

## 7. clusterDNS、fallbackDNS 与域名

`clusterDNS` 是共享出口访问的集群 DNS，不是 Windows 网卡 DNS。常见集群的 DNS Service 名为 kube-dns：

```powershell
kubectl --context $ctx -n kube-system get service kube-dns -o 'custom-columns=NAME:.metadata.name,IP:.spec.clusterIP,PORTS:.spec.ports[*].port,PROTOCOLS:.spec.ports[*].protocol'
```

必须确认 DNS 端点对出口可达、提供 TCP/53，且管理员网关账号的 DNS 规则与配置地址一致。不存在 kube-dns 时由管理员确认实际 DNS Service，不能假定固定为 `10.96.0.10`。

已有 Pod 的 resolv.conf 可帮助确认搜索域及 DNS（需要 exec 权限，且容器内有 cat）：

```powershell
kubectl --context $ctx -n $ns exec deployment/gde-adapter -c gde-adapter -- cat /etc/resolv.conf
```

如果安装了 NodeLocal DNSCache，Pod 的 nameserver 可能是节点本地地址，不能未经出口可达性和认证规则核对就照抄为 clusterDNS。[NodeLocal DNSCache 官方说明](https://kubernetes.io/docs/tasks/administer-cluster/nodelocaldns/)

`cluster.domain` 取真实集群域名，常见为 cluster.local；可结合 kubeadm-config 的 dnsDomain、Pod 搜索域及管理员提供的 CoreDNS 配置确认。跨平台本地应用优先使用 `redis.kube-system.svc.<实际域名>` 等完整名称，避免依赖容器内特有的短名称搜索规则。[Kubernetes DNS 说明](https://kubernetes.io/docs/concepts/services-networking/dns-pod-service/)

`fallbackDNS` 则从 **Windows 当前企业网络/VPN** 选择，最好在启动 TUN 前查看：

```powershell
Get-DnsClientServerAddress -AddressFamily IPv4 |
  Where-Object { $_.ServerAddresses.Count -gt 0 } |
  Format-Table InterfaceAlias,ServerAddresses
# 替换为真实企业域名与候选 DNS，验证该服务器可以解析。
Resolve-DnsName intranet.corp.example -Server 192.0.2.53 -DnsOnly
```

选择活动企业/VPN 接口中本机能访问的 DNS；不要直接选断开网卡、devctl 虚拟网卡或容器里的 127.0.0.53。当前 sing-box 对 fallbackDNS 使用直接 UDP DNS 请求；它不是“clusterDNS 失败后自动备用”，而是**非集群域名**的默认解析服务器。离线企业环境不要直接换成公网 8.8.8.8 / 1.1.1.1。

`dnsSuffixes` 只为额外由集群 DNS 处理的域名后缀填写；默认 `cluster.domain` 已自动包含。它不会把普通短名称自动变成完整 Service 域名。

## 8. envAllow：读取变量名字及资源引用

先获取 Deployment，选择真实业务容器。下面仅打印名称和引用，不打印环境变量值：

```powershell
$raw = kubectl --context $ctx -n $ns get deployment $app -o json
if ($LASTEXITCODE -ne 0) { throw 'Cannot read deployment' }
$dep = ($raw -join "`n") | ConvertFrom-Json
$dep.spec.template.spec.containers | Select-Object name,image
$containerName = 'gde-adapter'
$container = @($dep.spec.template.spec.containers | Where-Object { $_.name -eq $containerName })
if ($container.Count -ne 1) { throw 'Select the actual business container' }
$container = $container[0]
$container.env | Select-Object name,@{n='ConfigMap';e={$_.valueFrom.configMapKeyRef.name}},@{n='ConfigMapKey';e={$_.valueFrom.configMapKeyRef.key}},@{n='Secret';e={$_.valueFrom.secretKeyRef.name}},@{n='SecretKey';e={$_.valueFrom.secretKeyRef.key}},@{n='FieldPath';e={$_.valueFrom.fieldRef.fieldPath}}
$container.envFrom | Select-Object prefix,@{n='ConfigMap';e={$_.configMapRef.name}},@{n='Secret';e={$_.secretRef.name}}
```

读取 envFrom 引用资源的 key 列表，资源名按上一步替换：

```powershell
$raw = kubectl --context $ctx -n $ns get configmap gde-adapter-env -o json
if ($LASTEXITCODE -ne 0) { throw 'Cannot read referenced ConfigMap' }
$cfg = ($raw -join "`n") | ConvertFrom-Json
$cfg.data.PSObject.Properties.Name

$raw = kubectl --context $ctx -n $ns get secret gde-adapter-secret -o json
if ($LASTEXITCODE -ne 0) { throw 'Cannot read authorized Secret' }
$secret = ($raw -join "`n") | ConvertFrom-Json
$secret.data.PSObject.Properties.Name
Remove-Variable secret,raw
```

最后一个查询不打印 Secret 值，但 Kubernetes get Secret 仍返回整个对象；需要该资源的授权，不存在通过此命令仅获取 key 的独立 RBAC 权限。

变量名按实际注入结果填写：

| Deployment 写法 | envAllow 应写 |
|---|---|
| env 中 name=SPRING_DATASOURCE_PASSWORD，secretKeyRef.key=password | SPRING_DATASOURCE_PASSWORD，不是 password |
| envFrom.secretRef 引入 key=SPRING_DATA_REDIS_HOST，无 prefix | SPRING_DATA_REDIS_HOST |
| envFrom.prefix=APP_，被引用 key=REDIS_HOST | APP_REDIS_HOST |
| ConfigMap 仅挂载 application.yaml，其中含 spring.datasource.url | 不是环境变量；应使用 source.files，不填 spring.datasource.url |

envAllow 是允许导入的名单，不是自动发现规则；每个列出的名字必须能解析，或在 overrides 中提供值。缺失项即使未加入 requiredEnv 也会报错；不能保留模板中实际不存在的 SPRING_* 名字。

当前程序读取 env/envFrom 中的 ConfigMap/Secret、支持普通 env 值及 metadata.namespace；不读取镜像 Dockerfile ENV、入口脚本导出的变量、服务链接自动注入的 *_SERVICE_HOST，也不会进入容器执行 printenv。Pod IP/Pod 名/resourceFieldRef 等值需要本地覆盖或不导入；自定义配置中心与 CSI 挂载也要按实际应用另外处理。

envAllow 只限制最终导出的环境变量，**不会让程序跳过同一容器的其它 env/envFrom 资源引用读取**。减少名单不能替代相应的 named-resource RBAC。

如果数据库/Redis/MinIO 均配置在 YAML 中，选择 source.files 复制实际 key，再用 SPRING_CONFIG_ADDITIONAL_LOCATION 指向本地文件。无需人工把每个 YAML 属性展开成环境变量。

## 9. requiredEnv：开发者定义的启动检查

它不是 Kubernetes 字段，不能靠 kubectl 自动得到。根据项目实际启动条件选择，例如数据库必须用环境变量提供凭据时：

```json
"envAllow": ["SPRING_DATASOURCE_URL", "SPRING_DATASOURCE_USERNAME", "SPRING_DATASOURCE_PASSWORD"],
"requiredEnv": ["SPRING_DATASOURCE_URL", "SPRING_DATASOURCE_USERNAME", "SPRING_DATASOURCE_PASSWORD"]
```

requiredEnv 在导入和 overrides 之后检查非空；它本身不触发导入。每个名字应在 envAllow 中或由 overrides 赋值。配置实际来自 application.yaml 时，不要再要求不存在的 SPRING_DATASOURCE_* 环境变量；此时可以省略 requiredEnv，用应用启动、健康检查和业务测试验证配置。

检查“变量非空”并不证明数据库认证正确。变量是否必须由环境提供，应结合项目的 application*.yaml、配置属性类、@Value、启动校验及既有部署方式确认。

## 10. overrides：根据本地差异设置

它也不是从集群原样复制的字段，而是开发者对本地 JVM 的显式调整。当前优先级是导入集群环境后应用 overrides；overrides 新增的名字不必同时加入 envAllow。

| 常见项 | 本地用途 | 可否删除 |
|---|---|---|
| SERVER_ADDRESS / SERVER_PORT | 本地监听地址与端口 | 应用本地配置已一致时可删，healthURL/浏览器地址同步 |
| SPRING_CONFIG_ADDITIONAL_LOCATION | 引入 source.files 下载的配置 | 使用 source.files 的 YAML 时通常保留 |
| SPRING_KAFKA_LISTENER_AUTO_STARTUP | 关闭 Spring Kafka 自动启动监听器 | 服务没有消费者或其它已核实配置已关闭时可删 |
| SPRING_RABBITMQ_LISTENER_SIMPLE_AUTO_STARTUP / DIRECT_AUTO_STARTUP | 关闭相应 RabbitMQ 监听容器 | 按实际监听容器类型确认 |
| SPRING_FLYWAY_ENABLED / SPRING_LIQUIBASE_ENABLED | 避免本地启动执行共享库迁移 | 不使用这些组件或已有本地禁用配置时可删 |
| 项目自定义调度、启动事件、写入任务开关 | 调整共享资源访问行为 | 按 gde-adapter 实际代码确认，不能猜统一属性名 |

只有 `${CONFIG_DIR}`、`${NAMESPACE}`、`${CLUSTER_DOMAIN}` 三种 devctl 占位符会展开；这里不是任意 Shell/PowerShell 环境变量插值。MinIO 属性名以项目实际绑定为准，不假设通用 MINIO_* 名称。

若集群以容器 command/args 或入口脚本传入参数，开发者还需在 application.command 或实际项目配置中对应；devctl 不会自动复制集群启动命令。后台任务审查完成后才能设置 application.backgroundTasksReviewed=true；删除 overrides 不能绕过这项要求。

## 已有 application.yaml 时是否需要重复配置 Spring 属性

**不需要逐项重复。** 项目 `src/main/resources/application.yaml` 通常会进入运行时 classpath，Spring Boot 本地启动会自动加载。devctl JSON 负责启动与接入，不是另一份完整 Spring 配置文件。[Spring Boot 3.5 外部配置说明](https://docs.spring.io/spring-boot/3.5/reference/features/external-config.html)

| 实际情况 | devctl JSON 中需要保留什么 |
|---|---|
| 项目 application.yaml 已包含本地需要的配置，且不依赖额外变量 | 不重复写数据源、Redis、MinIO 属性；网络接入仍单独配置 |
| YAML 使用 `${DB_URL}` / `${REDIS_PASSWORD}` 等占位符 | 从 Deployment env/envFrom 导入这些实际变量名；需要非空检查时加入 requiredEnv |
| 集群把 application.yaml 挂载到容器路径，源码/构建产物中没有这份配置 | 用 source.files 获取对应 ConfigMap/Secret key，再指定本地 SPRING_CONFIG_ADDITIONAL_LOCATION |
| 配置文件已单独准备在 Windows 本机 | 用项目已有启动参数或额外配置位置指向该文件，不必再配置 source.files |
| application-k8s.yaml / application-local.yaml 需要指定 profile 才生效 | 按项目实际情况设置 spring.profiles.active；集群容器启动参数不会自动带到本地 |
| 只需改变本地端口、文件路径、消费者/迁移开关 | 只覆盖这些差异，优先复用项目已有的 local profile |

例如 YAML 是：

```yaml
spring:
  datasource:
    url: ${DB_URL}
    username: ${DB_USER}
    password: ${DB_PASSWORD}
```

应在 envAllow 中填写 DB_URL、DB_USER、DB_PASSWORD（前提是该容器实际注入了这些变量），而不是机械复制模板中的 SPRING_DATASOURCE_* 名称。环境变量不是 YAML 属性的同义词：具体变量名由项目中的占位符和部署配置决定。

若数据源、Redis、MinIO 已在文件中提供，不应为了套用示例再加同义的 SPRING_* overrides。Spring Boot 中操作系统环境变量通常优先于配置文件，重复导入可能覆盖你刚修改的 YAML；命令行参数等又可能具有更高优先级。应先确认实际激活的 profile 和属性来源。

本地复用 YAML 的同时，容器路径、短 Service 域名、运行时 Secret 占位符及后台任务行为仍需适配。用于本地修改的 application-local.yaml 可以只声明少量差异；是否加载它取决于项目的 profile/配置导入方式。

**v0.2.2 的限制仍然存在：envAllow 必须非空。** 若完全不需要导入集群环境，可以像第 11.2 节一样保留实际使用的 SERVER_PORT，并在 overrides 中设置本机端口。其余 Spring 属性不重复，requiredEnv 可省略，source.files 只在确实需要外部文件时保留。彻底支持 `envAllow: []` 的文件配置模式需要调整程序校验，当前尚未实现。

## 11. 可执行的简化方式

### 11.1 已有 Telepresence 接入：删除不用的 Gateway 字段

满足本机能访问 Kubernetes API、管理员已经安装兼容 Manager/出口 Agent 的前提，可以把 network 简化为：

```json
{
  "backend": "telepresence",
  "transport": "kubectl",
  "managerNamespace": "kube-system",
  "gatewayNamespace": "kube-system",
  "gatewayDeployment": "dev-egress"
}
```

这样不需要个人 Chisel 密码、fingerprint、routeCIDRs、bypassCIDRs、clusterDNS、fallbackDNS、dnsSuffixes。这个选择仍有真实网络/ambient 前提，不能用来替代管理员的连通性验收。

### 11.2 业务配置全在一个 ConfigMap 文件

假设实际存在 `gde-adapter-config/application.yaml`，该文件已经包含所有必需配置，后台任务已通过项目本地配置完成审查。下面是**完整的简化 profile**：

```json
{
  "version": 1,
  "name": "gde-adapter",
  "cluster": {
    "context": "internal-test",
    "namespace": "kube-system",
    "domain": "cluster.local"
  },
  "network": {
    "backend": "telepresence",
    "transport": "kubectl",
    "managerNamespace": "kube-system",
    "gatewayNamespace": "kube-system",
    "gatewayDeployment": "dev-egress"
  },
  "source": {
    "deployment": "gde-adapter",
    "container": "gde-adapter",
    "envAllow": ["SERVER_PORT"],
    "overrides": {
      "SERVER_ADDRESS": "127.0.0.1",
      "SERVER_PORT": "8080",
      "SPRING_CONFIG_ADDITIONAL_LOCATION": "file:${CONFIG_DIR}/application-cluster.yaml"
    },
    "files": [
      {
        "kind": "configmap",
        "name": "gde-adapter-config",
        "key": "application.yaml",
        "path": "application-cluster.yaml"
      }
    ]
  },
  "application": {
    "workDir": "C:/src/gde-adapter",
    "command": [".\\mvnw.cmd", "spring-boot:run"],
    "healthURL": "http://127.0.0.1:8080/actuator/health",
    "backgroundTasksReviewed": true
  }
}
```

这是有条件的文件配置示例，不表示真实 gde-adapter 已满足这些假设。若 YAML 仍引用 `${DB_PASSWORD}` 等环境变量，必须再导入对应变量或使用授权的 Secret 文件；不能只复制 YAML 就认为凭据齐全。若后台任务尚未审查，保持 backgroundTasksReviewed=false，补齐实际开关后再启用。

envAllow 中保留真正使用的 SERVER_PORT，并由 overrides 提供值，是 v0.2.2 下兼容非空校验的写法，无需在集群强行新增 SERVER_PORT。当前不能直接把 envAllow 设为空；放宽该限制属于后续程序改动，而不是已有功能。

省略 dependencies 只是不运行这份清单的依赖探测，不代表应用不再依赖数据库等资源；省略 tests 也不会自动产生业务验收。

### 11.3 必须走 SSH：采用管理员生成的 profile

SSH 会选择 Gateway，因此 authEnv、fingerprint、routeCIDRs、clusterDNS、fallbackDNS 不能直接删除。可让管理员一次填写并用 admin export 生成团队模板，开发者领取个人凭据，按自己的 VPN/DNS 调整 fallbackDNS 和必要的 bypassCIDRs。这是减少重复手填，而不是绕过必需信息。

当前没有 profile include/extends 功能，不能只写一个网络模板路径代替上述 JSON；导出结果是完整 profile。

## 12. 验证顺序

```powershell
$profilePath = 'C:\devctl\gde-adapter.local.json'
devctl.exe validate --profile $profilePath --format json
devctl.exe doctor --profile $profilePath --format json
devctl.exe connect --profile $profilePath --format json
devctl.exe run --profile $profilePath --format json
devctl.exe probe --profile $profilePath --format json
```

validate 只检查本地结构与必要字段；doctor 还检查工具、配置读取及部分路由冲突，但不证明隧道认证或中间件业务成功。Gateway 先按第 4 节加载认证环境变量。最终通过本地浏览器/Postman 的实际 API、断点和业务测试验证。

若需要管理员辅助发现资源，可运行已有的 admin discover；它列出引用和网络线索，不会自动选择本机 fallbackDNS、确定业务 requiredEnv 或生成合适的 overrides。[完整管理员流程](admin.md) · [gde-adapter 启动指南](gde-adapter.md)
