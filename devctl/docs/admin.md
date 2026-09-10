# 管理员安装：只使用已有 namespace

所有模板均不包含 Namespace。不要加 `helm --create-namespace`。先选择已有的共享开发/平台空间，确定可访问的业务与中间件范围。

## Gateway 后端

1. 将 Chisel 1.10.1 的 Linux 镜像导入内网仓库，记录不可变 SHA256 digest。
2. 管理员运行离线 Chisel 的 `server --keygen key.pem` 生成固定服务器密钥。私钥只放在集群 Secret；将对应 SHA256 公钥指纹交付客户端。
3. 为每位开发者创建单独认证项。`users.json` 示例：

```json
{
  "alice:REPLACE_PASSWORD": ["^socks$", "^10\\.96\\.0\\.10:53$"]
}
```

`socks` 允许通过 SOCKS 访问网关可达的目标，并不限制到某个数据库。目的地址范围由出口的 NetworkPolicy/网格策略约束。需要不同权限时预置不同角色的共享网关及账号。DNS 正则按实际 ClusterDNS 修改。

```powershell
kubectl --context internal-test -n EXISTING_NAMESPACE create secret generic dev-egress-auth `
  --from-file=users.json=users.json --from-file=key.pem=key.pem
```

Secret 不放入 Helm values 或版本库。`key.pem` 是 Chisel 服务端密钥，不是 Kubernetes 客户端凭据。

4. 创建内网 values，例如：

```yaml
image: registry.internal.example/devtools/chisel@sha256:REPLACE_WITH_REAL_DIGEST
ambient: true
developerGroups: [internal-developers]
readAccess:
  namespace: business-test
  deployments: [order]
  configMaps: [order-config]
  secrets: [order-db]
egress:
  - to:
      - namespaceSelector:
          matchLabels: {kubernetes.io/metadata.name: kube-system}
        podSelector:
          matchLabels: {k8s-app: kube-dns}
    ports:
      - {protocol: TCP, port: 53}
      - {protocol: UDP, port: 53}
  - to:
      - namespaceSelector:
          matchLabels: {kubernetes.io/metadata.name: business-test}
    ports:
      - {protocol: TCP, port: 8080}
  - to:
      - namespaceSelector:
          matchLabels: {kubernetes.io/metadata.name: middleware}
    ports:
      - {protocol: TCP, port: 6379}
      # 按真实 GaussDB、Kafka 等实例追加端口。
```

5. 先渲染、检查，再由管理员安装：

```powershell
helm template dev-egress ./deploy/gateway -n EXISTING_NAMESPACE -f site-values.local.yaml
helm upgrade --install dev-egress ./deploy/gateway -n EXISTING_NAMESPACE -f site-values.local.yaml --wait
```

Chart 要求非空 egress 和 digest 镜像引用。无 Service/NodePort 对外暴露入口；开发者使用 Kubernetes port-forward。Chisel 不启用 `--reverse`。

RBAC 模板对配置授予 named-resource GET。deployment port-forward 为发现 Pod 需要 get/list pods，并授予该已有 namespace 中的 pods/portforward create；此权限不只限于一个标签。请按该 namespace 的现有安全边界审查，必要时用现有准入控制限制 port-forward 目标。不要把此 Role 当成“只允许访问特定 Pod”的保证。

网络策略和 ambient 的 HBONE/ztunnel/waypoint 路径受 CNI 影响。示例业务端口规则不能代替实际路径验收；按原集群策略补齐必要的代理通信。默认不放行任意目的地。

## Telepresence 主后端

1. 离线准备 OSS 2.31.0 Chart、Windows 客户端、Traffic Manager/Agent 镜像及安装所需的网络组件。核对官方版本的 Chart values，并镜像到内部仓库。
2. 使用 `deploy/telepresence/values.yaml` 作为**待审查覆盖模板**，替换内网 registry。Webhook 的 objectSelector 只选 `devctl.io/shared-egress=true` 的出口 Pod。
3. 在现有 namespace 安装 Traffic Manager，并按官方 RBAC 模板配置客户端权限；配置来源的读权限复用 gateway Chart 的 readAccess。
4. 用 gateway Chart 的 `telepresence.enabled=true` 给专用出口 Pod 添加 Agent 注入标签和注解。此步骤由管理员完成，不依赖开发者首次 connect 自动修改业务 Pod。
5. 网关 NetworkPolicy 还需允许连接 Traffic Manager 及其所需 DNS、网格端口。根据实际 manager labels/ports 定义 egress；不要照搬 Chisel-only 规则。
6. 模板通过 `telepresence.io/mount-policies` 和 `agent.mountPolicies` 将 `credentials` 卷及 `/credentials` 路径设置为 `Ignore`，防止共享网关的私钥和账号文件被 Agent 导出。验证注入后的 Agent 没有挂载这些凭据卷、出口 Pod 仍加入 ambient，再让开发者使用 `connect --proxy-via`。策略定义见 [Telepresence 卷共享配置](https://telepresence.io/docs/reference/cluster-config#control-volume-sharing)。

示例命令：

```powershell
helm template traffic-manager .\offline\telepresence-2.31.0.tgz `
  -n EXISTING_NAMESPACE -f .\deploy\telepresence\values.yaml
# 审查镜像/RBAC/Webhook 范围后由管理员使用同样参数执行 helm upgrade --install。
```

Telepresence 的官方 Chart 自身可能包含集群级 RBAC/Webhook；没有个人 namespace 不等于安装不需要管理员权限。管理组件已有实例时优先复用经确认的版本和权限范围。

## Istio 身份与授权

仅给出口 **Pod** 标记 ambient，不修改整个已有 namespace 的标签。出口 ServiceAccount 在网格中呈现 `TRUST_DOMAIN/ns/EXISTING_NAMESPACE/sa/dev-egress`。

`businessAuthorization` 可选模板按指定 workload selector/端口添加 ALLOW，默认关闭。添加第一条 ALLOW 可能收紧原有允许范围；管理员必须与已有策略合并审查，不能机械套用。目标经过 waypoint 时还要检查已有 targetRefs L7 策略；已有 DENY 不会因新增 ALLOW 被覆盖。

本地功能调试借用的是明确授权的开发出口身份，不是 Windows 原生持有网格证书。业务正式 ServiceAccount 策略仍需集群部署后的验证。

## 离线交付清单

- devctl.exe、示例清单、文档和 SHA256SUMS。
- JDK 21、Maven/Gradle wrapper 分发包及项目依赖缓存，或可访问的内网仓库。
- Telepresence 固定版本客户端/Chart/镜像，或 Chisel 1.10.1＋sing-box 1.12.x＋Windows 网络驱动和 OpenSSH。
- 企业 CA、真实依赖地址、授权配置引用和访问范围。
- 记录实际验收的 Kubernetes patch、Istio 1.28.3、CNI、Windows build、工具版本及镜像 digest。

关闭使用上报由 Telepresence 客户端配置实现；覆盖模板同时关闭管理组件的 usage，并提供离线 Helm hook 镜像覆盖。不要让离线安装过程依赖 `latest`、在线 Helm repo、自动镜像拉取到公网或 wrapper 自动下载。

官方依据：[Telepresence connect](https://telepresence.io/docs/reference/cli/telepresence_connect)、[Chisel](https://github.com/jpillora/chisel)、[sing-box TUN](https://sing-box.sagernet.org/configuration/inbound/tun/)。
