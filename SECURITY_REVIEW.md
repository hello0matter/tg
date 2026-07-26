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

## Telegram 凭证与会话

- 系统级 Telegram API Hash 存入加密 `secrets`，API ID 留在普通设置；GET 设置接口只返回 `hasApiHash` 状态，不回传 Hash。
- 账号可选择完整的独立 API ID/Hash 覆盖。运行时只会选择完整账号凭证或完整全局凭证，不会跨来源拼接一对凭证。
- 旧版本账号已有的 API ID/Hash 自动视为独立覆盖，不会因配置全局凭证而改变登录应用身份。
- 每个账号继续使用以账号 ID 命名的独立 AES-GCM 加密 Session 文件。共享客户端应用凭证不会共享 Telegram 登录 Session。

## AI 与账号池新增边界

- AI 默认关闭，启动、登录和对话发现不会触发 AI 请求。只有显式启用 AI 的线路会把规则处理后的消息正文发送到用户配置的 OpenAI 兼容 URL。
- AI API Key 使用现有 AES-GCM/DPAPI 密钥链加密，普通 settings JSON 和 GET API 不返回 Key。配置第三方 URL 等同于授权该服务接收 Key 和被处理的消息内容，必须选择可信服务。
- AI 响应按固定 JSON 结构校验，正文被提示词明确标记为不可信数据；AI 失败默认进入人工审核。AI 仍可能误改内容，不能替代价格、收款、法律承诺等高风险信息的人工复核。
- 账号池只选择已在线且本身能访问目标会话的账号。Flood Wait 按账号冷却并持久退避，不会切换账号来绕过 Telegram 限制。
- 队列使用持久 `random_id` 调用 Telegram 发送 API，降低发送成功后本地状态未落盘造成的重复发送风险；失败任务仍需在面板中监控。

## Connector 扩展安全要求

- Connector 必须显式注册后才能创建或调度对应平台账号；未知 `platform` 不会回退到任意适配器。
- 队列按 `platform` 隔离领取，防止一个平台的 worker 误消费另一个平台的任务。
- Connector 的公开配置与加密凭证分开存储。密钥字段不得进入账号列表、活动日志或前端回传数据。
- 新 Connector 应优先使用平台官方 Bot/Webhook/Business API，不应通过个人号 UI 自动化规避登录保护、风控或速率限制。
