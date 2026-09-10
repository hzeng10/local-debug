# 第三方物料与许可证

devctl 新增的 YAML 解析依赖固定为 gopkg.in/yaml.v3 v3.0.1（MIT/Apache-2.0），源码与许可证随 vendor 交付。运行 EXE 不需要 Go 或在线获取此依赖。

docs/licenses 保存了固定版本的上游 LICENSE 原文；物料准备时另将 Wintun ZIP 内的 LICENSE 提取到 licenses/wintun.txt。离线包中的外部工具保留各自许可证义务；公共发布默认只分发 devctl，外部二进制由管理员在准备阶段从官方来源获取。

| 项目 | 许可证/官方来源 |
|---|---|
| Telepresence、Kubernetes/cri-tools、Helm、Istio、crane | Apache-2.0；各官方仓库 LICENSE |
| Chisel | MIT；https://github.com/jpillora/chisel/blob/v1.10.1/LICENSE |
| sing-box | GPL-3.0；https://github.com/SagerNet/sing-box/blob/v1.12.0/LICENSE |
| Wintun | 以官方下载包内 COPYING/README 及 https://www.wintun.net/ 发布说明为准 |
| BusyBox、curl 镜像 | 镜像内各软件许可证分别适用；保留上游镜像来源与源码获取方式 |

Telepresence 离线 Chart 保留完整上游归档，派生 Chart 仅修改 test-connection hook 的 pullPolicy、affinity、tolerations，补丁实现与源版本固定在 provision/chart.go。两份 Chart 的摘要独立记录。

再次分发第三方离线包时，应同时交付上游许可证和适用的源码/源码提供方式；不要把本项目说明视为改变第三方授权条款。
