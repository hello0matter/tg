# 上游项目审计摘要

审计日期：2026-07-25。结论基于仓库当前源码和提交历史的静态检查，不代表对所有历史制品或 Docker 镜像的担保。

## KurenaiRyu/im-sync-bot

结论：未发现明确的 C2、凭证外传或隐藏远程命令通道，但不建议直接作为本产品基础。

主要原因：

- 架构目标是 Telegram、QQ、Discord 互通，不是 Telegram 内部的可编辑商业镜像。
- 使用 AGPL-3.0，直接改造并分发会给商业交付带来源码开放义务。
- `HttpFileService` 存在未认证任意文件读取风险。
- 默认 Redis 暴露且容器以 root 运行。
- 历史提交出现过硬编码 GitHub token。
- TDLight 原生依赖以 Linux 为主，当前 Windows 环境无法直接启动 Telegram 主链路。
- 当前 master 可以编译，仓库自带 4 个测试均通过，但这不消除以上设计和部署风险。

## yanyuwangluo/TGForwarder

参考提交：`4945217747dbe326fc9ee1d45832e5936d933b33`，MIT License。

结论：没有在 Python 主程序中发现主动外传 Telegram 凭证或 C2 行为；可借鉴“同步对话、搜索频道、错误监控、保存转发消息 ID”的产品流程，但不适合原样部署。

主要风险与缺口：

- Flask 使用 `debug=True` 并监听 `0.0.0.0`，管理路由没有认证，局域网访问面过大。
- API ID、API Hash、手机号和 Telethon session 明文落盘。
- Web 模板依赖公共 CDN，离线时界面资源不完整，也增加供应链面。
- 没有可编排的内容替换、审核队列、话题映射、删除同步或相册聚合。
- 编辑同步采用“发送新消息再删除旧消息”，会改变目标消息 ID 和时间顺序。
- 仓库提交了与主程序无依赖关系的未签名文件 `app/._cache_SQLiteSpy.exe`，SHA-256 为 `59FD53F8F5D655BEB0D12A4FCA5A807337484BCFFD3E596E7C06246C7491D10D`。本次未执行该文件，也未将其引入 TG Workbench。

## TG Workbench 的边界

- 不执行参考仓库中的二进制、安装脚本或 Docker 镜像。
- 管理服务强制使用 loopback 地址并校验 HTTP Host。
- 前端资源编译后嵌入 EXE，运行时不从 CDN 拉取代码。
- 不提供绕过 Telegram 受保护内容或平台限流的能力。
