# 交付与恢复记录

## 原任务中断

上次 Codex 任务最终状态为 `failed`，错误原文：

```text
Selected model is at capacity. Please try a different model.
```

这是执行记录中的模型容量错误。该记录不能证明应用 UI 没有其他问题，也没有证据表明 llmHub 代码导致 Codex 死锁。源码拉取和 GitHub fork 已完成，控制层六个源文件留在未提交的 `hub/` 中，前端与部署目录当时为空。

用户随后再次反馈：点击旧会话即卡死，强制退出应用。该现象与模型容量错误分别记录，尚无足够证据确定 UI 卡死根因。旧会话标题约 7,700 字符；尝试通过应用工具缩短标题失败，返回 `no rollout found for thread id`，没有修改会话历史或本地数据库，也没有宣称应用问题已修复。后续工作在当前会话继续，不再打开旧会话，并减少单次工具输出。

只读检查应用日志发现，2026-09-30 15:27:18 UTC 对旧会话加载记录有 `Conversation state not found`；16:20:30 UTC 有成功归档记录。会话状态异常是可核查线索，而不是已确认的 UI 卡死根因；没有取得 renderer 卡死堆栈，不把标题长度或模型容量错误当作定论。

恢复过程中采取的措施：读取已有成果而不重新克隆；分阶段执行；限制命令输出；使用临时 Go 缓存；测试以本地模拟供应商为主；所有回归脚本都有时限；持续记录可继续的位置。客户端模型服务是否有容量不是项目代码可以控制的，因此不能承诺该类错误永不再发生。

## 实现位置

| 目录 | 内容 |
| --- | --- |
| `hub/internal/llmhub` | 配置验证、SQLite、共享 FIFO 调度、认证、路由、预算、SSE 和统计 |
| `hub/cmd/llmhub` | 生产入口与生命周期 |
| `hub/cmd/demo` | 仅本机使用的无费用模拟入口 |
| `hub/sdk` | Node / Python 接入客户端与测试 |
| `ui/llmhub` | 独立中文控制台 |
| `deploy/llmhub` | seed、Bifrost 配置、Compose、Dockerfile、Nginx、环境模板 |
| `scripts/llmhub-check.mjs` | 有总时限的验证脚本 |
| `tests/e2e/llmhub/console.mjs` | 模拟环境浏览器回归 |
| `tests/e2e/llmhub/native.mjs` | 真实 Bifrost 二进制与本地模拟供应商的链路回归 |
| `.github/workflows/llmhub.yml` | 不访问模型 API 的 CI |

## 已验证

- Go 编译、race 回归与 vet。
- 队列容量、取消、并发、共享 RPM / TPM、冷却、重启、关闭。
- 原子预算预留、幂等结算、异常中断恢复、单实例锁、保留期不清空月预算。
- 项目认证、权限隔离、撤销密钥、配置版本冲突、备用模型切换。
- OpenAI 协议路径、模型别名、SSE 拆包 / 中断、图片参数约束、请求验证。
- 官方 Bifrost v2.2.4 macOS arm64 二进制：llmHub → Bifrost → 本地模拟供应商，验证项目密钥、场景别名、供应商选钥、普通 / SSE 请求、实际 usage 与价格快照结算。
- Node / Python 客户端。
- React 构建、TypeScript、格式检查。
- Playwright 桌面 1440×1000 与手机 390×844，九个页面、项目 CRUD、错误令牌重试、密钥、图片、SSE 与页面溢出检查。
- Bifrost 配置通过仓库官方 Draft 2019-09 JSON Schema；Compose 配置通过语法校验。
- Docker Hub 确认 `maximhq/bifrost:v2.2.4` 的 amd64 / arm64 镜像存在。

## 峰谷计价增量

已增加每周时段完整单价、IANA 时区、独立节假日日历时区、跨午夜和夏令时处理、缓存输入单价、服务端当前价格 API，以及场景级可选 `lowest_cost` 路由。默认仍保持配置顺序；价格在队列准入后冻结，每次重试重新定价，结算和历史记录沿用快照。价格变化导致重新选路时，已使用的重试次数不会被重置。

新增测试覆盖时段精确边界、周末 / 公休日、跨日 / 跨周重叠、夏令时、排队期间重新定价 / 换路、SSE 跨边界结算、缓存用量、峰时预算、快照重启 / 幂等结算和重试上限。官方 Bifrost v2.2.4 的本地 OpenAI / DeepSeek 模拟链路验证了普通和 SSE 峰时计费，并检查缓存字段缺失时的保守估算。浏览器回归验证时段编辑、重叠错误修正后再次保存、当前价格 / 节假日刷新、成本路由和桌面 / 手机布局。

DeepSeek 示例核对日期为 2026-10-01，价格来源与年度日历来源记录在 `README.md`。后续已新增下面记录的官方价格同步能力；年度节假日日历仍需管理员确认。网关计算值不替代供应商账单，没有质量 / 延迟评分，也不会自动把请求推迟到谷时。

## 官网同步增量

服务端默认每 6 小时检查 DeepSeek / OpenAI / Anthropic 的官方价格和模型公告，具备持久化到期时间、固定 HTTPS 白名单、20 秒 / 2 MiB 上限、单实例工作线程、取消、失败退避、手动检查和变更记录。首次确认后可跟随明确报价；新模型不自动加入路由。DeepSeek 峰谷矩阵保留已批准年度日历，OpenAI 固定 standard 档位；多维 / 不明确报价转人工核对。来源失败 / 过期 / 条件改变会暂停官方跟随档案的派发，历史账单保留来源指纹和准入价格。

解析测试同时覆盖合成故障页面和开发时下载的真实公开官网页面；常规 CI 仅用本地合成数据，没有外网依赖或付费模型调用。变化记录、配置版本、报价采纳在单一事务中提交；已覆盖故障回滚、重启保留、并发管理员编辑、报价变化、年度日历确认和 URL / 重定向 / 响应体限制。修改限于独立 `hub/` 与 companion UI，未修改上游 provider wire 层，因此上游付费 provider harness 不适用于这些管理 API；通过 llmHub 本地 mock / native 链路和浏览器回归验证。

2026-10-01 最终验证：完整 `llmhub-check.mjs`、真实 Bifrost v2.2.4 本机模拟链路、十个页面的桌面 / 手机 Playwright 回归全部通过。控制台测试覆盖官网失败状态、公告模型、异步检查、条件确认后采纳、检查间隔保存及还原；后端覆盖排队期间改价的拒绝派发和过期官方档案的批准备用路由。预览真实抓取成功获得 DeepSeek 2、OpenAI 40、Anthropic 19 个价格表条目，以及相应官方公告 / 模型目录；三家价格和公告当前均无抓取错误。默认手动档案保持原价，并未擅自开启官方跟随。

当前预览继续使用 `/private/tmp/llmhub-preview-pricing.db`；更新前备份为 `/private/tmp/llmhub-before-catalog.db`。只读取公开官网，不使用供应商密钥、不产生真实模型费用；原有项目、配置、密钥和记录保留。

官方 v2.2.4 的模拟 DeepSeek 普通 / SSE 响应没有保留原生缓存命中字段。该链路下缓存成本标记为估算并按未缓存输入计价，不宣称等同于供应商实际折后账单；峰谷价格和预算仍按对应时段应用。普通 OpenAI 规范化缓存字段的精确结算已另行验证。

## GLM / MiniMax / Kimi / Gemini / Qwen 官网监测

2026-10-01 增加五家固定官方来源，与原三家共用每 6 小时的服务端检查、公告发现、变更审计和失败退避。旧数据库自动补齐来源，原验证 / 报价 / 历史记录保留；来源地址变化会要求重新验证。支持目录供应商别名 `zai` / `moonshot` / `dashscope`，不更改现有 Bifrost 推理供应商配置。

真实公开页面解析与预览后台抓取均成功：GLM 21、MiniMax 11、Kimi 4、Gemini 30、Qwen 135 个报价条目，共新增 201 个。全部八家价格和模型公告当前无抓取错误。新增来源均保留多维计费条件并要求人工维护业务单价；Gemini / Qwen 不展示假定的统一最低价或零价。国际站 USD、国内人民币、套餐、Vertex AI 与 Developer API 不混用；新模型不会自动加入路由。

最终验证通过完整 Go race / vet、Node / Python SDK、UI 构建 / 类型 / 格式检查、五家真实下载页面解析、本机官方 Bifrost v2.2.4 模拟推理链路和十页桌面 / 手机 Playwright 回归。覆盖跨列 / 多行表头、阶梯 / 优先价格、缓存维度、促销、免费 / 空报价、币种变化、MDX 非字面量拒绝、来源迁移及明细按需渲染。官网页面中的脚本 / MDX 从不执行；仍保留单请求 20 秒 / 2 MiB 限制，不调用付费模型 API。

本地预览沿用 `/private/tmp/llmhub-preview-pricing.db`，升级前备份为 `/private/tmp/llmhub-before-five-providers.db`。升级前后业务配置指纹一致；测试仅操作并清理自己的 QA 对象。

本地预览更新前以 SQLite 在线备份保留了原有配置、项目密钥和记录。demo 新增可选 `-db` 参数，已有数据库不会被 seed 覆盖或重复签发演示密钥；相关重启回归已通过。默认不传 `-db` 时仍是退出即清理的临时演示。

## 项目与 default 上游密钥

2026-10-01 增加按供应商的 `Project.credentials` 和 `Config.default_credentials`，选择顺序为项目专用、全局 default、旧 Profile 默认值。真实 API Key 仍由 Bifrost 管理，llmHub 只保存命名引用及对应限额池。控制台项目编辑和设置页均可配置，请求详情显示实际密钥来源、名称和池；移除项目覆盖后恢复继承，不重签业务项目密钥。详细操作与字段片段见 [上游密钥配置](credentials.md)。

派发、队列、成本预测统一解析有效绑定；按实际配额决定共享或独立限额池，不把新建 API Key 当成新增配额。缓存预测隔离不同密钥 / 池的证据；历史价格与选钥快照不可变。排队期间绑定变化返回 `409 credentials_changed`，不派发或扣预算；错误项目绑定不会自动使用 default。

通过完整 Go race / vet、Node / Python SDK、UI 类型 / 构建 / 格式检查、真实 Bifrost v2.2.4 本地模拟链路及十页桌面 / 手机 Playwright 回归。覆盖项目/default/旧 Profile 选钥、普通和 SSE、缺失密钥返回 400 且未调用其他密钥、独立池不被 default 池阻塞、预测历史隔离、排队期间轮换、重复供应商校验、保存与移除继承，以及请求审计来源展示。

本次仅修改独立 `hub/` 与 companion 控制台，未更改上游 provider wire 层；上游付费 provider harness 不适用，选钥路径已用真实 Bifrost 加本地模拟供应商验证。没有使用真实供应商凭证或产生模型费用。本地预览数据库升级前备份为 `/private/tmp/llmhub-before-project-credentials.db`，升级和 QA 清理后的业务配置与备份逐项相同，原有项目及密钥保留。

## 配置文案与 API Key 直填

2026-10-01 配置文案修正：项目 / 默认平台 API Key 页面直接接收平台密钥，内部自动登记到私有 Bifrost，不再要求手填内部名称。错误提示包含行号与具体字段，并显示在对应输入框下；限额池可留空，继承默认设置而非关闭限流。API Key 密码输入、已配置留空保留、不回显明文、版本冲突与失败清理均有回归。真实 Bifrost v2.2.4 已验证自动登记、SQLite 持久化、普通 / SSE 选钥及进程重启后仍使用新密钥。本地预览升级前备份为 `/private/tmp/llmhub-before-api-key-ux.db`。

## 尚未验证的环境项

本机 Docker daemon 没有运行，未实际构建或启动 Compose 容器。上游要求 Go 1.27，本机为 Go 1.26.5，因此没有编译上游 Bifrost dev 树。控制层使用独立 Go 1.26 模块并已编译测试。

没有使用真实供应商凭证，没有产生真实模型调用费用，也没有把 StorePilot 等现有项目切到新网关。上线前仍需配置真实模型 / 当前价格 / 供应商配额，并在目标服务器验证 Bifrost 镜像和小流量供应商调用。不要把模拟测试通过等同于供应商账户联通或目标机器容量验证。

## GitHub 交付状态

Fork 为 `pcyan3166/llmHub`，上游 remote 保留 `maximhq/bifrost`。本地实现已提交为 `dde49bf`，分支 `codex/llmhub-control-plane`。

初次 HTTPS 推送缺少本机凭证，连接器创建分支返回 403。用户提供的截图确认 `pcyan3166` 下的 ChatGPT Codex Connector 已有代码读写权限与全部仓库访问权；因此不能将连接器错误归因于用户未授权。当前任务的连接器却仍只返回 `ssuo95918-wq` 账号及其安装信息，绑定差异尚未修复，没有继续要求用户重复授权。

随后使用本机已有 SSH 认证，GitHub 返回 `Hi pcyan3166! You've successfully authenticated`，通过 SSH 成功推送 `codex/llmhub-control-plane`。`origin` 保留 HTTPS 拉取地址，推送地址改为 `git@github.com:pcyan3166/llmHub.git`。后续推送可直接执行：

```bash
git push --set-upstream origin codex/llmhub-control-plane
```
