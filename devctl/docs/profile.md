# 服务启动清单

格式为严格 JSON、`version: 1`，未知字段报错。使用 JSON 是为了让原生 Go 二进制无需额外解析库；可由任何脚本或 IDE 编辑。`examples/order.json` 给出全部核心字段。

| 字段 | 含义 |
|---|---|
| `name` | 本地服务标识，不创建任何同名 namespace |
| `cluster.context` | 必填，SSH 模式指远端 kubeconfig 中的 context |
| `cluster.namespace` | 配置来源的现有业务 namespace |
| `cluster.domain` | 实际集群 DNS 域，不能假定永远是 cluster.local |
| `cluster.kubectl` / `remoteKubectl` | 可执行文件路径，不能包含 shell 参数 |
| `network.backend` | `telepresence` 或 `gateway` |
| `network.transport` | `kubectl` 或 `ssh`；SSH 强制选择 gateway |
| `network.gatewayNamespace` / `gatewayDeployment` | 管理员预装共享出口的位置 |
| `source.deployment` / `container` | 读取启动环境的目标容器，副容器不混入 |
| `source.envAllow` | 明确导入的变量名称列表，不支持 `*` |
| `source.requiredEnv` | 解析后必须非空的变量 |
| `source.overrides` | 覆盖或新增本地环境变量 |
| `source.files` | 显式读取 ConfigMap/Secret key 到本地相对路径 |
| `application.command` | 参数数组，直接启动进程，不当成 shell 字符串执行 |
| `application.workDir` | 绝对路径或相对清单文件路径 |
| `application.healthURL` | 本地 loopback HTTP 地址，禁用重定向；默认期望 200 |
| `application.startupSeconds` | 默认 180 秒；超时停止启动的进程树 |
| `application.tests` | 测试名称到参数数组的映射 |
| `application.testTimeoutSeconds` | 默认 300 秒 |
| `application.inheritEnv` | 除常见运行环境之外显式继承的宿主变量 |
| `dependencies` | 可选 TCP 地址或 HTTP(S) URL 探测清单 |

覆盖模板只接受 `${CONFIG_DIR}`、`${NAMESPACE}`、`${CLUSTER_DOMAIN}`。不展开任意 shell、PowerShell 表达式或本地环境变量，以免把请求数据变成命令。

`source.files` 的 `kind` 是 `configmap` 或 `secret`；`path` 必须是正常相对路径，不能含 `..`、绝对路径、反斜杠或 Windows ADS 冒号。多层目录用 `/`。binaryData 和 Secret base64 自动解码；内容不改变。

环境解析按 envFrom 顺序合并，再执行显式 env；之后导出 allowlist，最后应用本地覆盖。镜像默认 ENV 无法从 Deployment 中推导，缺失的 allowlist 变量必须加入明确覆盖或已有的配置来源。

## Spring Boot 启动示例

Maven wrapper 自动编译启动：

```json
[".\\mvnw.cmd", "spring-boot:run", "-Dspring-boot.run.jvmArguments=-agentlib:jdwp=transport=dt_socket,server=y,suspend=n,address=127.0.0.1:5005"]
```

已有构建产物：

```json
["java", "-agentlib:jdwp=transport=dt_socket,server=y,suspend=n,address=127.0.0.1:5005", "-jar", "target/order.jar"]
```

Gradle 可使用 `[".\\gradlew.bat", "bootRun"]`，调试参数通过项目已有的 Gradle 配置设置。Windows `.cmd/.bat` 调用仅支持无 cmd.exe 特殊元字符的参数；复杂 JVM JSON/引号使用环境变量或直接 java.exe 启动。

应用健康检查端口必须空闲；否则拒绝启动，防止误把旧 JVM 当作新代码。业务端口与管理端口分离的应用，应额外把业务入口加入 smoke 测试。

## 数据与后台行为

`backgroundTasksReviewed` 表示服务维护者已检查本地消费者、定时任务和迁移配置，不代表工具能自动识别全部框架。示例的 Kafka/RabbitMQ/Flyway/Liquibase 开关只覆盖常见 Spring 自动配置。自定义消费者、`@Scheduled`、分布式任务注册、启动事件副作用要单独确认。

数据库类型、端口、JDBC 驱动必须按你们的 GaussDB 产品与版本填写。Redis Cluster 不通过切换 database 编号做隔离。Kafka 换 Group 不等于独立 Topic。需要写入隔离时使用现有测试数据机制或管理员提供的逻辑资源，不创建个人 namespace。
