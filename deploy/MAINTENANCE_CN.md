# NAS 与东京共用代理源码

唯一代理仓库为 `huaye37/CLIProxyAPI-YEUTECH`，维护分支为
`yeutech-capability-v15`。东京不维护第二份长期 fork。上游 `main` 用于观察变化，
每日维护合并最新正式 release 标签，在隔离工作树处理冲突、适配和验证后再安全快进维护分支。

同一个完整 Git 提交分别构建 NAS 和东京候选；两台机器允许暂时运行不同版本。
NAS 使用现有蓝绿发布和配套 API 管理站，东京使用 `deploy/claude-edge` 配置与独立回退。
任何一边构建失败都必须逐项报告，不能把另一边成功当作双端完成。

| 项目 | NAS | 东京 |
| --- | --- | --- |
| 代理源码与上游修复 | 共用维护分支 | 共用维护分支 |
| 供应商、账号、并发和内存 | NAS 现有配置 | Claude 专用配置，凭据仅驻留东京 |
| 请求头最小化和排空入口 | 默认关闭，按需配置 | 环境变量显式启用 |
| 模型目录 | 维护分支目录及已验证渠道 | 随候选冻结目录，安装和回退与镜像配对 |
| 安装 | 用户在平台触发 | 用户在同一平台独立触发 |

控制器和候选编排的唯一维护位置是 API 管理站仓库；不要在本仓库的历史
`deploy/update-controller` 副本中再次实现。当前管理站维护工作树为同级
`.worktrees/api-platform-tokyo-20261015`，分支 `codex/remote-node-management`。
后续移动时以该仓库的维护分支和已验证生产源码为准，不从较旧的主工作树覆盖。

从管理站运行 `python3 scripts/stage-unified-proxy.py --source <干净代理工作树>`，
它执行共享源码验证并准备两端候选，不调用安装或重启生产服务。
现有 `scripts/nas-release.sh` 是历史发布入口，不能用于自动候选准备。

更新器只管理版本、验证、发布和回退；请求头处理留在代理可选模块。
不随更新新增供应商、伪造调用成功或改变 OAuth 授权。账号权限和真实可用性
仍须按必要的最小调用独立验证，模型目录出现不等于已授权。

## 2026-10-15 事故与后续维护约束

当晚 Claude 授权和短请求均正常，但 NAS 到东京的直连 SSH 吞吐降到约
30–40 KB/s。无模型参与的 8 MiB 传输耗时约 196 秒，短请求也受同一连接拖累；
管理站调用验证的 20 秒超时进一步表现为 HTTP 499。不能把这类取消直接判断为
账号封禁，也不能仅凭长上下文解释全部耗时。

部署已改为 NAS → 香港受限 SSH 中转 → 东京，压缩在 NAS 到东京的内层 SSH
连接完成，香港只转发加密连接。8 MiB 多样化测试包上线后用时 4.391 秒，
NAS 主代理的 Opus 短流式调用首段 1.380 秒。长会话推理耗时须另行验证。
源配置与详细证据见 `deploy/claude-edge/README.md` 的 2026-10-15 节。

每日维护必须保留并核对以下约束：

- `nas-tunnel-relay.compose.yaml` 继承公共 `nas-tunnel.compose.yaml`；正式 NAS
  `compose.yaml` 使用前者，不能拿直连模板覆盖。`nas-relay.ssh_config.example`
  保留香港跳板与内层压缩。普通模型升级不重写隧道、SSH 密钥、主机密钥或账号。
- 香港账号只允许转发东京 SSH 端口、禁止 shell 和远程监听；东京 API 只在回环
  监听，OAuth 与上游调用仍留在东京。不能为通过测试放宽这些限制。
- v8 配置的客户端密钥位于 `access.api-keys`。更新器的健康检查须优先读取它，
  仅在字段缺失时兼容旧顶层列表；不得从顶层供应商分组取密钥，显式空列表不得
  回退旧密钥。空值、重复字段、别名或不支持的布局应安全失败且不回显凭据。
- 健康检查修复位于权威 API 管理站的 `updater/bluegreen.py`，不复制到历史
  `deploy/update-controller`。控制器发布前记录实时模块摘要，使用
  `scripts/nas-deploy-node-controller.py` 校验 `expected-live.json` 后原子替换，
  有更新任务或源码漂移时停止。禁止旧控制器包覆盖节点管理、额度和蓝绿修复。
- `20b4a41d` 修复的配置/授权快照竞态已经通过重复回归；旧 v8.0.29 阻塞记录是
  历史经过，必须结合最新适配台账判断，不能永久锁定在旧结论，也不能削弱测试。
- 每次代理 HEAD 或配套管理站内容变化后，重新执行统一候选准备，核对 NAS、
  东京与配套管理站完整提交/摘要。仅改部署文档也可能改变候选身份，不能拿旧
  manifest 冒充当前提交的成套候选。生产身份与候选身份分别报告。

日常任务仍只准备代理及配套候选，由用户触发安装；一次临时更新授权不改变此
长期边界。发现上游实现与上述约束冲突时，先语义合并与验证，不通过覆盖生产
配置来消除差异。网络路径变化或出现大请求异常时，使用 `transport_probe.py`
做小规模无模型传输复验；短模型请求和真实长会话验收分别记录。

## 2026-10-18 Codex / Grok 搜索与上下文统计隔离

Grok 托管搜索的 `usage.input_tokens` 是服务端多步推理的累计用量，不能当成
当前对话的上下文长度。现场已出现刚压缩后约 19K 的客户端估算，接一次搜索就
返回 1,015,785 输入 token 并再次压缩；19K 不是包含全部工具声明的上游精确值。
禁止靠抬高窗口、关闭压缩或改小真实 usage 掩盖问题。

本机 Codex 使用已有的独立搜索功能：`[features] standalone_web_search = true`，
provider 保留 `supports_standalone_web_search = true`。这样搜索是 `web.run`
客户端工具，经 `/v1/alpha/search` 单独请求，不再随 Grok 的整段对话托管执行。
此开关作用于本机 Codex 的所有模型，不更改其他消费者的搜索能力或代理目录。

真实 Codex 回合还发现 xAI 将 `custom` 的 code-mode `exec` 转成 `function_call`
却不还原，客户端报 `tool exec invoked with incompatible payload`。因此目录生成器
按真实供应商 `.type/.owned_by == xai` 将本机模型 `tool_mode` 设置为 `direct`，
使用普通函数工具；不得继续用 `code_mode_only` 假装该自定义协议已打通。此次
只改变两个 Grok 条目的工具模式，其他 13 个条目、窗口、速度、推理档位均未变。
更新 `refresh-yeutech-model-catalog` 的运行副本，避免定时刷新把错误模式恢复。

独立搜索使用现有 Codex 订阅搜索服务，**不是 Grok 原生 X 搜索**；Grok 推理仍
使用原 Grok 账户。必须明确区分搜索与推理的额度来源，不声称搜索消耗 SuperGrok
额度或新增 Grok 原生能力。当前实测的独立搜索服务型号为 `gpt-6.1-sol`；桥接
只在明确 Codex 的 `/v1/alpha/search` 中把非 GPT 型号改为该搜索服务型号。
`/v1/responses`、压缩、Live、历史、推理模型、请求参数及响应 usage 全部原样。
可通过 `YEUTECH_CODEX_SEARCH_MODEL` 指定另一个经过实测的 Codex 搜索型号，
不得仅因模型目录有名称就改路由；搜索失败显式返回，不自动退回托管搜索。

安装时三个模块必须成套复制：`codex-yeutech-auth-bridge.mjs`、
`codex-yeutech-errors.mjs`、`codex-yeutech-search.mjs`。先备份当前配置与模块，
核对 `/_yeutech/health` 空闲，再对桥接进程发送 SIGTERM 平滑结束，等待现有
LaunchAgent 自动拉起；不得使用强制 kickstart 或打断已有请求。
配置对新加载的会话生效；不得恢复用户已停止的会话或篡改旧 token 记录。
旧会话保留的累计计数可能让它在下次继续时先压缩一次，这不等于再次搜索误触发。

回归命令：

```sh
CODEX_TEST_BIN=<installed-codex-engine> CODEX_TEST_CATALOG=<current-catalog.json> \
  node --test --test-concurrency=1 scripts/test-codex-standalone-search.mjs \
  scripts/test-codex-yeutech-auth-bridge.mjs
```

引擎测试只使用回环模拟服务和临时会话：复现旧累计计数触发压缩、验证独立搜索
无误压缩，并验证真实超阈值仍压缩。真实调用和本机生效回执另行记录，不能用此
模拟测试宣称整个生产链路已经验收。代理、东京及配套管理站仍由用户触发安装。

## 2026-10-24 内置 OpenAI 通道与本机桥接

当前源码已迁入唯一单仓 `yeutech/apps/model`；配套管理站为 `yeutech/apps/api`。
上文旧独立仓库路径和历史工作树仅作历史记录，不可据此覆盖单仓运行源码。

旧桥接未处理 WebSocket upgrade，内置 OpenAI 通道的握手会被剥掉 Upgrade
后作为普通 GET 转发，导致上游 HTTP 400。新桥接只在本机对 Responses 握手
返回 HTTP/1.1 426，使实际 Codex 引擎回退到已有 POST/SSE；不修改模型、请求体、
客户端功能开关、账号身份或历史 provider ID。

隔离端口真实 Gemini 4 回合已返回 OK/turn.completed，账号和用量可读，模型列表
保留五个家族。使用的进程级配置如下，尚未写入用户全局默认配置：

```toml
model_provider = "openai"
openai_base_url = "http://127.0.0.1:18401/v1"

[features]
enable_request_compression = false
```

请求体压缩与上下文压缩是两回事。上述配置只关闭线上传输的 zstd，避免现有
内存门禁对压缩请求按完整解压预算预留而返回 `503/proxy_memory_busy`。保留上下文
窗口、80% 压缩阈值、实际 usage、模型偏好、Fast 和独立搜索。不能放宽服务器
内存保护，也不能把这个 503 解释成账号封禁或之前的 GET/400。

本机安装器现在先验证桥接健康且 `activeRequests=0`；忙碌时不复制文件、不发
信号。已注册的 LaunchAgent 保留原配置，只发 SIGTERM 平滑排空并由 KeepAlive
拉起，禁止 bootout 或 kickstart -k。新版本回读须为
`20261024-http-fallback-v1`。三模块继续成套安装。不存在安全空闲窗口时保留
候选；不得为完成安装强行停止会话。

安装后再由主会话合并用户级配置中的上述字段（不是覆盖整个 `[features]`），
保留 `openai-http` 与 `yeutech` 配置供历史会话使用。最终 Dot 侧栏和新会话实际
调用由客户端重启后的用户验收确认。源码测试、隔离真实调用、本机安装、Dot
可见性及 NAS/东京发布必须分别报告。
