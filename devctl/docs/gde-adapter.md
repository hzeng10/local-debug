# kube-system/gde-adapter：Windows 本地启动与调测

请求路径：`Windows 浏览器/Postman → localhost:8080 → 本地 gde-adapter JVM → 集群 gaussdb/redis/minio`。共享集群中其它服务调用 gde-adapter 时，仍然访问共享实例。

## 1. 管理员准备

按 [离线安装文档](admin.md)部署共享出口。填写 gde-adapter 主容器名、它实际引用的 ConfigMap/Secret，以及 gaussdb、redis、minio 的 Service 名和端口。

文档中的 `gaussdb:5432`、`redis:6379`、`minio:9000` 只是示例，不能据此判断你的 GaussDB 产品、数据库协议或实际端口。GaussDB JDBC 驱动和 URL 沿用项目与集群现有配置，不自动替换成 PostgreSQL 驱动。

管理员交付对应后端的启动 JSON、开发工具目录、公钥指纹及该开发者自己的凭据文件。不要分发管理员 kubeconfig 或 Chisel 私钥。

## 2. 对齐服务配置

模板为 `examples/gde-adapter.json`，安装后会导出填好网络参数的版本。需确认：

| 配置 | 来源与检查 |
|---|---|
| 数据源 URL / username / password | Deployment 的 env/envFrom 对应 ConfigMap 或 Secret；明确加入 envAllow |
| Redis 地址、端口、密码、TLS、拓扑 | 使用现有 Spring 配置，不能把 Redis Cluster 当作单节点或随意更换 database |
| MinIO endpoint / access key / secret key / bucket | Spring Boot 没有统一 MinIO 属性名，沿用 gde-adapter 当前 YAML 和资源引用；不硬编码猜测的 MINIO_* 绑定 |
| application-cluster.yaml | source.files 显式读取现有 ConfigMap 的真实 key，通过 SPRING_CONFIG_ADDITIONAL_LOCATION 引入 |
| 其它挂载配置 | 在 source.files 添加对应 ConfigMap/Secret key，必要时明确本机文件位置覆盖 |
| 日志、临时文件目录 | 原配置若指向容器绝对路径，使用该服务已有配置项改成本机可写路径 |

环境 allowlist 默认拒绝通配符；引用资源的 RBAC 名单必须与 source/envFrom 一致。管理员的 discover 输出可帮助找到这些引用，但不会自动理解自定义 MinIO 配置或入口脚本。

检查消费者、定时任务、启动事件及数据库迁移的本地行为。现有示例只关闭常见 Kafka/RabbitMQ/Flyway/Liquibase 自动配置；自定义逻辑须使用项目已有配置开关。确认后再设置 `backgroundTasksReviewed=true`。

## 3. Windows 启动

本机准备 JDK 21、Maven/Gradle 及项目离线依赖。修改 JSON 中 `application.workDir` 为 gde-adapter 源码路径；默认 Maven wrapper 自动编译启动。

```powershell
$env:Path = "C:\devctl\bin\windows-amd64;$env:Path"
# SSH/Gateway 模式：每个人只读取自己的密码文件。
$credential = Get-Content -Raw C:\devctl\alice.credentials.json | ConvertFrom-Json
$env:DEVCTL_GATEWAY_AUTH = $credential.DEVCTL_GATEWAY_AUTH

devctl.exe validate --profile C:\devctl\gde-adapter.ssh.json
devctl.exe doctor --profile C:\devctl\gde-adapter.ssh.json --format json
devctl.exe connect --profile C:\devctl\gde-adapter.ssh.json
devctl.exe run --profile C:\devctl\gde-adapter.ssh.json
devctl.exe probe --profile C:\devctl\gde-adapter.ssh.json
```

Gateway 的 Windows TUN 需要管理员权限。若使用 Telepresence，则启动你们已预置的本机网络服务或使用所需提升权限；换用 `gde-adapter.kubectl.json`，确保本机 context 具有已授权的配置读取和连接权限。

浏览器或 Postman 请求 `http://127.0.0.1:8080/<实际业务路径>`。IDE 使用 Remote JVM Attach 连接 `127.0.0.1:5005`；请求停在本机断点，数据库和中间件调用由本机 JVM 经共享出口执行。

## 4. 验收与 AI Agent 闭环

复制 `examples/admin/gde-smoke.json`，填写已有的只读业务 API 路径，并调整启动清单中 smoke 配置文件的位置。

```json
{
  "baseURL": "http://127.0.0.1:8080",
  "healthPath": "/actuator/health",
  "readOnlyPaths": [],
  "timeoutSeconds": 20
}
```

readOnlyPaths 默认空，只证明健康检查成功。要验证业务，填入 gde-adapter **已经存在且确认只读**的接口：查询测试数据、读取 Redis 中已知测试记录、读取 MinIO 对象元数据等。不要为了调试入口修改业务代码，也不要假设所有 GET 都没有副作用。

```powershell
devctl.exe test --profile C:\devctl\gde-adapter.ssh.json --suite smoke --format json
# 修改代码后重新构建运行并验证。
devctl.exe stop --profile C:\devctl\gde-adapter.ssh.json
devctl.exe run --profile C:\devctl\gde-adapter.ssh.json
devctl.exe test --profile C:\devctl\gde-adapter.ssh.json --suite smoke --format json
devctl.exe disconnect
```

验证分三层：管理员安装验收的 DNS/TCP、JVM 的依赖认证/驱动、业务接口的结果。gaussdb 的只读查询通过现有业务 API 或项目已有测试执行；基础连接探测不能证明 SQL 或凭据正确。

本示例不写 Redis/MinIO。若需要写入测试，使用项目已有测试程序和明确配置的测试 key 前缀、bucket/prefix，记录并删除本次创建的对象；不能依赖随机修改库号或共享资源名称实现隔离。

最后检查集群 gde-adapter Deployment 的 Pod 数、镜像与配置保持原状，集群调用仍落在共享实例。Windows/VPN/CNI/ambient 实机验收须记录实际版本和结果。
