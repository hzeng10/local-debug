# 实机验收与故障恢复

自动化假集群测试覆盖工具编排，不替代以下真实环境验收。

## 验收顺序

1. 记录 `java -version`、`devctl version`、网络工具版本、Kubernetes/Istio/CNI 版本；离线交付物校验 SHA256。
2. 使用配置完成且已审查后台任务的清单运行 `validate`、`doctor`。SSH 模式移除本机 Kubernetes 凭据后测试远端读取。
3. `connect` 后运行 `probe`。检查服务 FQDN、企业 DNS、API/SSH 入口均可访问。
4. `run` 后由本地浏览器/Postman 调用 `127.0.0.1`；IDE 附加 5005 端口，确认断点来自当前本地代码。
5. 修改代码，`stop` → `run` → smoke，确认响应反映本地更改；观察共享 API 和中间件操作。
6. 两位开发者在各自电脑同时运行同一服务。确认未创建 namespace、未增加业务副本、未改变共享 Service 后端或路由。
7. 检查本地 A → 共享 B → 共享 A 的实际路径，确认没有意外反向接管。
8. 验证实际业务协议：数据库事务；Redis Cluster MOVED/ASK 或 Sentinel；Kafka 全部 advertised.listeners；MinIO 上传/下载/预签名 URL；ES 节点发现。TCP 建连只是初步检查。
9. 观察 ztunnel/waypoint 日志，确认出口身份及原策略执行效果。不要通过全局关闭 ambient/mTLS 获得通过。
10. 分别中断网络、关闭 JVM、重启出口 Pod/Manager。检查 `status` 的进程退出/网络降级，随后恢复；运行真实测试确认重连。HTTP 断点停顿可能触发上游超时，这是应用行为而非隧道失败。
11. `disconnect` 后确认受管 JVM、测试和隧道已退出，临时配置已删除，路由/DNS 恢复，共享组件仍存在。
12. 在无公网网络中重复冷启动；禁用已有构建缓存则需内网依赖仓库，否则应明确报告缺失依赖。

`scripts/acceptance.ps1` 自动执行正常路径并保存报告；多人、故障、网格身份和真实协议语义需要结合现场环境补充。默认报告目录在当前目录，注意不要提交含业务数据的测试报告。

## 常见问题

- **配置读取失败**：核对 context/namespace/container 和 named ConfigMap/Secret GET 权限；工具不回显 kubectl 的原始 stdout/stderr，避免泄露凭据。
- **应用启动失败**：`status` 输出 logPath；查看脱敏日志、JDK 21、工作目录、内部 CA、Spring 配置优先级与实际 build 命令。
- **健康端口被占用**：先停止旧实例。工具不自动杀死未知进程，避免将旧服务健康状态误认为新 JVM。
- **SSH 认证失败**：检查现有 OpenSSH 配置、代理跳转、host key、BatchMode 认证和 AllowTcpForwarding。只支持普通非交互 SSH；强制图形堡垒机交互不在 v1 支持范围。
- **SSH 转发远端端口被占用**：工具使用随机本地端口并使用同号远端端口；碰撞时重新 connect，或检查遗留的远端 kubectl。远端进程由该 SSH 会话管理，不常驻安装 daemon。
- **网段冲突**：工具检查网卡地址范围，但无法证明所有 VPN 路由没有冲突。按实际 VPN 路由处理；不要强行覆盖默认路由。
- **TLS 失败**：同步 CA/信任库和正确 FQDN，不使用跳过证书验证作为默认处理。
- **Gateway 已连接但依赖失败**：检查 Chisel 认证项、NetworkPolicy、ambient 身份、DNS 以及 Kafka/Redis 返回的地址是否在代理范围内。`status` 是控制/进程状态，`probe` 和业务测试才验证实际依赖。

## 异常关机或强制终止后

正常 `disconnect` 幂等性以活动会话为边界：没有活动会话时明确报错，不把未知清理状态当成功。如果 supervisor 被强制结束，可能有 `network.lock` 残留。

1. 查看用户缓存目录 `devctl/session.json`、`supervisor.log` 和 `sessions` 目录。
2. 确认旧 devctl 管理进程确实已退出；在 Windows 任务管理器核对可执行路径/进程树，不能仅依据陈旧 PID。
3. 如有本工具启动的 Telepresence 会话，使用对应配置执行 `telepresence quit`；如有本工具的 sing-box/Chisel/SSH，确认所属会话后停止。
4. 检查 `devctl` TUN 及其路由已清理。Windows Job Object 在管理进程句柄关闭后会清理所管理进程；仍需实机确认网络组件恢复。
5. 如果有 cleanup-error.txt，先按其提示确认 Telepresence 已断开并恢复路由，再删除该错误标记。删除该工具的陈旧 session.json、network.lock 和会话内配置目录，再重新连接。不要删除管理员共享资源。

`--state-dir` 供测试/诊断使用；生产开发机应始终使用同一默认目录，避免通过不同状态目录同时启动多个网络会话。
