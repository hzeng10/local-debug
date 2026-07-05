# CLAUDE.md 模板 —— 让 ClaudeCode 用 `ldbg` 调试本服务

> 把下面分隔线之间的内容拷贝到你的 Spring Boot 服务仓库的 `CLAUDE.md`（或追加到已有
> 文件），并替换 `<service>` / `<namespace>` / 构建命令。任意会话中的 ClaudeCode 读到它
> 即可即插即用地跑通「接管 → 复现 → 查日志 → 改代码 → 验证 → 收尾」闭环。

---

## 用 ldbg 在本地调试本服务（远程集群真实流量）

本仓库的服务运行在远程共享 k8s 集群（Istio ambient）。用 `ldbg` 可以把它接管到本机：
本地进程接收该服务的真实集群流量、访问真实依赖（DB/MQ/Redis/其他微服务）。

- 服务名：`<service>`　命名空间：`<namespace>`　本地构建/启动：`./mvnw spring-boot:run`
  （Gradle 项目改为 `./gradlew bootRun`）
- **所有 ldbg 命令都支持 `--json`**，输出统一 envelope：`{ok, command, data, error, hint}`。
  失败时按 `hint` 行动；`data.truncated=true` 表示结果被截断，需收窄条件。
- ⚠️ 全量接管：拦截期间集群里对该服务的所有调用都会打到本机（进程停止或停在断点时
  该服务不可用）。调试完必须 `ldbg down`。

### 标准调试闭环

```bash
ldbg doctor <service> -n <namespace> --json   # 预检（含 log-store / log-collection 检查）
ldbg up <service> -n <namespace> --run ./mvnw --run spring-boot:run --json
                                              # 接管 + 以集群环境启动本地进程（stdout 自动落盘）
ldbg test <service> -n <namespace> --json     # 断言集群流量确实落到了本机（data.succeeded）
# …复现问题、读日志、改代码、重启本地进程…
ldbg test <service> -n <namespace> --json     # 验证修复
ldbg down --json                              # 退拦截、还原 ambient、清理（必须执行）
```

首次连接需要用户手动执行 `telepresence connect`（root 守护进程要 sudo/管理员提权）——
遇到 connect 失败就提示用户在终端里自己跑一次，然后重试。

### 日志：两个来源，一套过滤词汇

| 场景 | 命令 | 说明 |
|------|------|------|
| 历史/集群侧日志（含已删除 Pod） | `ldbg logs query <service> --since 4h -q Exception --json` | 查集群日志库（保留 30 天） |
| **拦截期间**本服务的新日志 | `ldbg logs local --level error --json` | 本机文件；拦截激活时可省略服务名 |
| 聚合验收（修复后错误归零） | `ldbg logs stats "by (level) count() as c" --since 10m --json` | 机器可读的验证信号 |
| 探索可查什么 | `ldbg logs fields` / `ldbg logs values service` | 字段与取值自省 |

- 时间窗口：`--since 5m/30m/1h/4h/8h/12h/24h/2d/7d/…`，或 `--from/--to`（RFC3339）。
- 过滤：`-q 关键字`（多词 AND、`"短语"` 保持整体、`-i` 忽略大小写）、`--level`、`--pod`、
  `-c 容器`；`logs query` 的 `-q` 含 `:` 或 `|` 时按原生 LogsQL 透传（如 `-q trace_id:abc`）。
- 拦截期间集群侧对本服务是空窗 —— `logs query` 的 `hint` 会提示改用 `logs local`；
  堆栈跟踪在 `logs local` 中完整归并，`-q` 能匹配到堆栈帧。

### 分工

开发者负责 IDE 断点与单步；ClaudeCode 负责 `up/test/status/logs query/logs local`、
读堆栈、改代码并迭代 —— 共享同一个 ldbg 会话，互不干扰。

---
