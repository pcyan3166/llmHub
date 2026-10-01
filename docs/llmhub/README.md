# llmHub

基于 `maximhq/bifrost` 的轻量单机 AI 网关。业务项目只发送项目密钥和场景别名；真实供应商、模型、价格、备用路由与共享限额统一在后台配置。

## 已实现

- 项目、业务场景、模型 Profile、限额池的管理 API 和中文控制台。
- OpenAI SDK 兼容的 Chat Completions、Responses、Embeddings 与非流式 Images Generation。
- Chat / Responses SSE 透传，完成后从供应商 usage 结算；流式中断保留预估费用。
- 项目级密钥，数据库只保存 SHA-256 摘要；签发时显示一次明文，支持吊销。
- 多项目共用的 FIFO 队列、并发、60 秒滑动窗口 RPM / TPM、排队超时、取消和 429 冷却。
- 在发起调用前原子预留月预算，结束后结算；重试和备用路由独立记录。
- 项目月度用量、场景 / 模型成本明细、请求记录、每次调用价格快照、配置版本历史。
- 时区 / 周几 / 跨午夜时段价格、节假日例外、缓存输入独立单价，以及场景级当前预估成本优先路由。
- 服务端定时检查官方报价与模型公告，变更审计、管理员确认后自动跟随明确报价，以及过期 / 不明报价的派发保护。
- SQLite WAL、单实例文件锁、异常中断恢复、30 天明细清理；月度项目汇总不受明细清理影响。
- Node 与 Python 轻量客户端、本地无费用模拟环境、回归测试、CI、Docker Compose 和 Nginx 配置。

## 架构

```text
StorePilot / followagents / followmcp / followskills / stockinsky
                        |
                 llmHub :8080
        项目认证 -> 场景 -> Profile -> 共享 FIFO 调度
                        |
               预算预留 -> 供应商调用 -> 用量结算
                        |
           Bifrost 私有 /openai/v1/... 接口
                        |
               OpenAI / Anthropic / ...
```

`hub/` 是独立 Go 1.26 模块，不依赖 Bifrost Core 的构建工具链。Bifrost Core 保持上游实现；API 适配、供应商 SDK 与连接池继续由 Bifrost 负责。控制台位于 `ui/llmhub/`，沿用仓库现有 React、Vite、RTK Query、React Hook Form、Zod 与 Lucide 依赖，单独构建，不替换 Bifrost 控制台。

生产部署是两个轻量进程 / 容器：llmHub 与 Bifrost。没有 Redis、PostgreSQL、ClickHouse、向量数据库或 Prometheus Server。持久化文件为 `llmhub.db`、Bifrost 的 `config.db` 和 `logs.db`。

## 本地模拟预览

在仓库根目录执行：

```bash
npm --prefix ui ci --ignore-scripts
npm --prefix ui run build:llmhub
cd hub
GOTOOLCHAIN=local go run ./cmd/demo -listen 127.0.0.1:8090
```

打开 <http://127.0.0.1:8090>，使用演示管理员令牌 `llmhub-local-demo-admin-token`。这不是生产密钥。模拟服务只监听 loopback，不访问任何外部模型；数据存放在临时目录，退出后清除。控制台明确显示模拟环境状态。

需要跨预览重启保留配置、密钥和用量时，给 demo 传 `-db /absolute/path/to/demo.db`。已有数据库不会重新导入 seed 或重复签发演示密钥。该模式仍然只调用本机 mock，不能作为生产供应商验证。

## 生产配置

1. 将 `deploy/llmhub/environment.example` 作为本机 `.env` 的模板，填写真正的密钥和三个不同的随机管理秘密。`.env` 不提交到 Git。加密密钥应与数据库一起备份，后续不要随意更换。
2. `deploy/llmhub/seed.json` 中 `CONFIGURE_TEXT_MODEL` / `CONFIGURE_IMAGE_MODEL` 都是占位符。修改为账户真实可用的模型，并按供应商当前价格配置单价。示例单价仅供演示，不是模型官方报价。
3. 供应商 `primary` 密钥名称必须与 Profile 的 `key_name` 一致。默认 Bifrost 配置只有 OpenAI；使用 `text.quality` 前，在 Bifrost 控制台添加 Anthropic 的 `primary` 密钥，或从场景路由移除这个备用 Profile。
4. 同一个供应商账户 / 组织 / 模型限额组使用同一个 `pool_id`，即使业务项目、Profile 或 API Key 不同。按供应商真实限制设置 RPM、TPM 和并发；业务项目必须全部经过 llmHub。
5. seed 仅在数据库为空时导入。此后使用控制台或带版本的配置 API 修改配置，编辑 seed 不会覆盖现有数据库。

```bash
docker compose --env-file deploy/llmhub/.env -f deploy/llmhub/compose.yml up -d --build
```

控制台与业务 API 为 <http://127.0.0.1:8080>。Bifrost 供应商控制台为 <http://127.0.0.1:8081>，只绑定本机。远程管理可以用 SSH 同时转发 8080 / 8081；公网只通过已有 TLS Nginx 暴露 llmHub，参考 `deploy/llmhub/nginx.conf`。

Compose 示例镜像为已核实存在的 `maximhq/bifrost:v2.2.4`。可以固定为 digest；升级前先跑测试并在备用数据目录验证。模板提供总计 640 MiB 的容器内存上限与较低 worker 数，属于部署起点，实际 RSS 仍应在目标机器上测量。

本地直接运行生产服务：

```bash
cd hub
export LLMHUB_ADMIN_TOKEN="你的至少24字符管理员令牌"
go run ./cmd/llmhub -listen 127.0.0.1:8080 -db ./data/llmhub.db \
  -bifrost http://127.0.0.1:8081 -ui ../ui/llmhub/dist -seed ../deploy/llmhub/seed.json
```

`GET /health` 检查 llmHub 存储；`GET /ready` 额外检查 Bifrost 健康。健康不意味着模型账户、模型名称或余额一定可用。

## 业务接入

五个项目的源码改造位置、接口契约、场景规划、AI 执行步骤、验收和回滚见 [AI 对接文档入口](integrations/README.md)。正确名称为 followmcp；当前 seed 的历史 `followmpc` ID 需按专篇处理，本次文档修正不迁移运行数据。

先在“项目密钥”页面签发对应项目的密钥。项目身份由密钥解析，不能通过请求体冒用其他项目。模型字段填写场景别名，真实模型由场景的 Profile 路由决定。

```bash
curl http://127.0.0.1:8080/v1/chat/completions \
  -H "Authorization: Bearer $LLMHUB_PROJECT_KEY" \
  -H "Content-Type: application/json" \
  -d '{"model":"scene/text.generate","messages":[{"role":"user","content":"生成商品介绍"}]}'
```

原有 OpenAI SDK 只需替换 `baseURL`、`apiKey` 和模型字段：

```ts
const client = new OpenAI({
  baseURL: "https://你的网关域名/v1",
  apiKey: process.env.LLMHUB_PROJECT_KEY,
});
const result = await client.chat.completions.create({
  model: "scene/text.generate",
  messages: [{ role: "user", content: "生成商品介绍" }],
});
```

自带 Node 客户端需要 Node 22，支持 AbortSignal 和流式 Response：

```js
import { LLMHub } from "./hub/sdk/node/index.mjs";
const hub = new LLMHub({ apiKey: process.env.LLMHUB_PROJECT_KEY });
const text = await hub.text("text.generate", [{ role: "user", content: "生成商品介绍" }]);
const image = await hub.image("image.generate", "生成商品宣传图");
const stream = await hub.stream("text.generate", [{ role: "user", content: "你好" }]);
// Consume stream.body with your application's SSE reader.
```

Python 客户端位于 `hub/sdk/python/llmhub.py`，使用标准库，无额外依赖。Python 流式请求建议使用原有 OpenAI SDK；轻量 Python 客户端提供非流式 JSON 接口。

`GET /v1/models` 列出密钥所属项目可用的场景别名。也可以用 `X-LLMHub-Scene`，但同时传 `model` 时，两者必须指向同一个场景。

响应保持 OpenAI 兼容格式，并附加 `X-Request-ID`、`X-LLMHub-Profile`、`X-LLMHub-Model`、`X-LLMHub-Queue-MS` 和 `X-LLMHub-Price-Window`。

## 配置 API

管理 API 必须携带管理员令牌 `Authorization: Bearer ...`。令牌只驻留浏览器内存，刷新页面后需重新认证，不写入 localStorage。

| 接口 | 行为 |
| --- | --- |
| `GET /api/llmhub/config` | 获取全部配置与版本 |
| `GET /api/llmhub/pricing` | 服务端当前时间 `at` 与已解析当前价格的 `profiles`；只读，不可作为配置提交 |
| `GET /api/llmhub/catalog` | 官方来源、最近验证、下次检查、模型报价、档案验证状态和最近 1000 条变更记录 |
| `POST /api/llmhub/catalog/check` | 异步启动官网检查，返回 202；重复 / 一分钟内再触发返回 429 |
| `POST /api/llmhub/catalog/apply` | `{profile_id, hash, version, confirm_conditions:true}`：以页面指纹和配置版本防止采纳过时提案，原子采纳报价、开启跟随并记录审计 |
| `PUT /api/llmhub/config` | 提交 `{config, version}`，版本过期返回 409 |
| `GET /api/llmhub/keys` | 列出密钥摘要 |
| `POST /api/llmhub/keys` | 提交 `{project_id}`，签发并一次返回明文 |
| `DELETE /api/llmhub/keys/{id}` | 吊销密钥 |
| `GET /api/llmhub/overview?month=2026-10&project=storepilot` | 项目汇总、近期记录、成本明细与限额池状态 |

## 峰谷价格与成本路由

在“模型 Profiles → 编辑”中配置默认未缓存输入、缓存输入和输出价格，再启用“时段计价”。时段具有独立的完整单价，未命中任何时段时使用默认价格。周几采用 ISO 编号（周一 1 至周日 7），时间为 `HH:MM`，开始包含、结束不包含；结束可用 `24:00`，跨午夜时段按开始日匹配。重叠时段、重复标识、无效日期或时区会被拒绝。

时段的 `timezone` 与节假日的 `calendar_timezone` 分开设置，后者缺省继承前者。`excluded_dates` 按日历时区判断，当天全部使用默认价格，优先于每周时段。IANA 时区遵循夏令时本地钟表规则；例如秋季重复的 01:30 两次均按同一时段计价。

`deploy/llmhub/deepseek-pricing.example.json` 是可加入配置 `profiles` 的 Profile 示例，不是完整 seed。接入前创建 `deepseek-shared` 限额池，并在 Bifrost 配置 DeepSeek 的 `primary` 密钥。示例依据 2026-10-01 核对的 [DeepSeek 官方价格](https://api-docs.deepseek.com/quick_start/pricing/)：Flash 谷时输入 / 缓存输入 / 输出分别为 $0.15 / $0.003 / $0.6，峰时为 $0.3 / $0.006 / $1.2；峰时为周一至周五 UTC 01:00–04:00、06:00–10:00，排除中国公休日。示例日历来自 [国务院 2026 年放假安排](https://www.beijing.gov.cn/cs/gncs/zcwj/202603/t20260327_4568275.html)。按供应商的 weekday 规则，调休上班的周末仍是谷时。价格可在确认后跟随官方更新；年度节假日日历仍需管理员核对，不保证与供应商账单完全一致。

“模型 Profiles”列表展示服务端计算的当前价格、时段和更新时间，每 5 秒刷新。调用在排队结束、准备发起供应商请求时冻结价格，按这份快照预留预算及结算；跨越峰谷边界的长响应不会按完成时价格追溯修改。每次重试重新定价。历史记录保留时段、计价时间、单价与缓存命中数；后续改价或重启不改变已结算费用。这是网关估算的时间锚点，官方账单如采用不同时间锚点，以供应商账单为准。

缓存用量兼容 DeepSeek `prompt_cache_hit_tokens`、OpenAI `prompt_tokens_details.cached_tokens` 和 Responses `input_tokens_details.cached_tokens`。只有返回有效缓存用量且配置缓存单价时，才按“未缓存输入 × 输入价 + 缓存输入 × 缓存价 + 输出 × 输出价”结算。配置缓存价但供应商没有返回缓存用量时，按全部未缓存输入保守估算并标注“估算”。预算预留与成本路由不假设缓存一定命中。

官方 Bifrost v2.2.4 的 OpenAI 兼容转换会丢弃 DeepSeek 专有缓存字段。llmHub 对 DeepSeek Chat 改用私有 Bifrost 原生路由，仅合并输入 / 输出计数一致且有效的原始缓存 usage，随后删除内部 `extra_fields`，普通响应和 SSE 都保留客户端兼容的缓存字段。需在 Bifrost 的 `providers.deepseek` 设置 `send_back_raw_response:true`、`send_back_raw_request:false`、`store_raw_request_response:false`；示例见 `deploy/llmhub/bifrost.deepseek.example.json`。Bifrost 必须保持私有、不能直接暴露给业务客户端，并保持 `allow_per_request_raw_override:false`。没有原始缓存数据时仍保守估算，绝不由请求文本猜测实际命中数。`native.mjs` 已对官方 v2.2.4 验证普通 / SSE 命中数与私有信封移除。

### 调用前成本预测

“请求记录”右侧计算器提供成本预估，管理员接口为 `POST /api/llmhub/estimate`：

```json
{"project_id":"storepilot","scene_id":"text.generate","request":{"messages":[{"role":"user","content":"hello"}]}}
```

预估不调用供应商、不入队、不计费，也不修改配置。每个已批准且官网价格验证通过的候选返回预测费用、未命中预计费用、保守预留额、缓存概率、成本 / 缓存置信度与样本量。实际派发时重新以当前时段价格冻结预测；请求详情显示原始预测以及“实际费用减预测费用”，历史预测不会被后续改价覆盖。`lowest_cost` 路由和预算仍按保守上限，不使用预测折扣。

首版只匹配完全相同请求（忽略 `stream` 和服务端补充的流式 usage 参数），不做语义相似或部分前缀推断。证据按项目、场景、接口、Bifrost 地址、供应商、模型、`key_name` 和限额池隔离。只保存带随机持久化密钥的 HMAC 指纹和数字用量，不保存 prompt / 响应内容；SQLite 仍包含业务密钥哈希及其他敏感配置，应按现有要求保护及备份。上游密钥轮换 / 账户切换需更换 `key_name` 或使用新限额池，网关无法从同名别名感知供应商账户变更。

仅成功且用量完整的调用进入最近 32 条样本；中断、重试、失败和缺失用量不训练。输入 / 输出参考最多 30 天的相同请求历史，无历史使用保守上限。缓存只参考完成 5 秒以上、5 分钟以内的明确用量，至少 3 条才报告经平滑的命中概率；最新用量未知或证据过期时取消折扣。5 分钟是本地证据窗口，不是供应商 TTL，也不是保证缓存命中。置信度低于 10 条为低、10–29 条为中、30 条以上为高；输出波动较大时成本置信度降为低。这是启发式样本等级，不是统计覆盖率承诺。参考 [DeepSeek 缓存说明](https://api-docs.deepseek.com/guides/kv_cache/)和 [OpenAI 缓存说明](https://developers.openai.com/api/docs/guides/prompt-caching)。

用量解析也支持 Bifrost `cached_read_tokens`，并单独展示 `cache_write_tokens` / `cached_write_tokens`。写入不算作读取命中；由于尚未支持写入单价及存储费，当供应商报告非零写入时仍保留原预算预留额作为估算费用，不宣称精确结算，也不据此学习缓存折扣。缺失完整输入 / 输出用量的文本请求同样保留预留额。响应级缓存未启用，本预测不会复用生成内容。

场景 `routing_policy` 默认是 `ordered`，保持配置順序。选择 `lowest_cost` 后，每次选路和排队结束都在该场景管理员已批准的候选 Profile 中比较当前预估费用；费用包含保守输入估计和各 Profile / 请求允许的输出上限，相同费用保留原顺序。排队期间赢家变化时释放原并发槽并进入新候选的 FIFO 队列，仍受请求总超时约束；限额统计保守记录已取得的队列准入。备用切换也按剩余候选的当前价格选择，不会重试已耗尽的候选。

它不是质量或延迟自动评分。不同模型输出上限、能力和质量可能不同，管理员应先为场景选择能力满足业务需求的候选，再启用成本优先；业务调用仍只传 `scene/...`。不自动将请求推迟到谷时，没有新增异步持久化任务。

## 官方价格与模型检查

生产服务与本地 demo 都在启动时检查到期来源，随后每分钟判断持久化的到期时间。默认启用、每 6 小时抓取；控制台“官方模型与价格”可暂停、修改间隔（15 分钟至 7 天）或立即检查。独立配置字段为 `catalog: {enabled:true, interval_minutes:360}`；省略时使用默认设置。无需供应商凭证，只读取公开官方文档，不调用模型 API。测试构造 `NewServer` 不启动外网任务，生产入口显式启动后台生命周期。

当前支持 [DeepSeek 报价](https://api-docs.deepseek.com/quick_start/pricing/)及[更新日志](https://api-docs.deepseek.com/updates)、[OpenAI 标准报价](https://developers.openai.com/api/docs/pricing)及[更新日志](https://developers.openai.com/api/docs/changelog)、[Anthropic 报价](https://platform.claude.com/docs/en/about-claude/pricing)及[模型目录](https://platform.claude.com/docs/en/models/overview)。不是全供应商爬虫；未支持的供应商仍须手动维护。公告提及的模型可能是别名、预览或下线通知，不代表账户可用性；新模型只进入目录和变更记录，绝不自动加入业务路由。

新增五家公开官网来源：

| 目录供应商 | 官方报价 | 模型发布 / 目录 | 适用范围 |
|---|---|---|---|
| `glm`（也识别 `zai`） | [Z.ai](https://docs.z.ai/guides/overview/pricing) | [发布记录](https://docs.z.ai/release-notes/new-released) | 国际站 USD，不适用于智谱国内站 / Coding Plan |
| `minimax` | [MiniMax Pay as You Go](https://platform.minimax.io/docs/guides/pricing-paygo) | [模型发布](https://platform.minimax.io/docs/release-notes/models) | 国际站按量 API，不适用于订阅套餐 |
| `kimi`（也识别 `moonshot`） | [Kimi](https://platform.kimi.ai/docs/pricing/chat) | [模型目录](https://platform.kimi.ai/docs/models) | 国际站 USD，不适用于国内站 / Kimi Code |
| `gemini` | [Gemini Developer API](https://ai.google.dev/gemini-api/docs/pricing) | [发布记录](https://ai.google.dev/gemini-api/docs/changelog) | Developer API，不适用于 Vertex AI |
| `qwen`（也识别 `dashscope`） | [阿里云国际站](https://www.alibabacloud.com/help/en/model-studio/model-pricing) | [模型目录](https://www.alibabacloud.com/help/en/model-studio/models) | 各地区 / 部署模式的 USD 明细，不适用于国内人民币报价 |

这五家接入定时抓取、模型发现、价格 / 条件变化审计及失败退避；已有数据库自动补齐新增来源，保留原报价、批准记录和历史事件。新来源的缓存写入 / 存储、上下文阶梯、优先档位、促销截止日期、账户地区等尚未完整覆盖网关计费，因此均不提供自动采纳，也不自动修改已有业务单价。GLM / MiniMax / Kimi 展示官网明确列出的 USD / 1M 基础单价并保留额外维度；Gemini / Qwen 仅展示多维明细，`display_only:true` 的目录条目不能把 `input` / `output` 占位值当作可用报价。明细按需展开，避免一次渲染整站表格。不会把人民币猜算为美元，也不会拿国际站价格套用国内 API。目录别名识别不等于新增 Bifrost 原生供应商；推理仍需正确配置其自定义供应商 / API 地址。

已有手动报价和协议价不会被覆盖。在目录页核对账户、地区、处理档位和适用计费条件，然后对精确匹配的 Profile 执行“采纳并跟随”。这会设置 `follow_official:true` 并记录已批准的条件指纹。只在计费条件不变、来源验证有效时自动更新单价；报价与配置历史、审计在一个 SQLite 事务中提交，不覆盖抓取期间的管理员修改。关闭 Profile 的“跟随已确认的官方报价”可恢复手动管理。`official_terms`、`official_calendar_year`、`official_calendar_hash` 是批准凭据，不应手工伪造。

DeepSeek 会严格解析峰 / 谷与缓存命中 / 未命中的完整报价矩阵。自动更新只适用于与官方 UTC 周一至周五 01–04 / 06–10 时段一致的档案，保留已确认的 Asia/Shanghai 节假日日历。新年度或日历修改后必须再次确认完整日历，不会凭单个日期推断整年假期。OpenAI 仅自动套用无额外上下文 / 缓存写入维度的标准文本报价，派发时固定 `service_tier:"default"`，避免账户默认 Fast 档位改变单价。长上下文、缓存写入、图片等多维报价以及 Anthropic 的非精确 API 名称 / 额外收费均标记“待人工核对”，不猜测转换或覆盖价格。

来源使用固定 HTTPS 白名单，不接受管理员自定义抓取 URL、不转发密钥、不跟随重定向；每个请求最多 20 秒和 2 MiB。检查单实例、手动触发限频，价格来源失败以 5 / 10 / 20 分钟等退避（不超过正常间隔）重试，保留最后成功报价和错误。公告失败保留最后公告，按正常检查间隔再试。控制台展示最后验证时间、来源、抓取错误、模型 / 报价 / 条件变化；最近 1000 条事件持久化，重复检查不重复产生变化事件。

启用官方跟随的档案，只要抓取失败、距最近成功验证超过两倍检查间隔、条件指纹变化、报价不匹配或年度日历未确认，便不再派发新调用，返回 `503 official_price_unverified` 或在入队前选择场景中其他有效候选。排队期间再验证一次；价格刚变导致原快照不一致时拒绝该次派发，客户端可重试。已发出的调用按准入时冻结的报价、来源 URL、页面指纹及验证时间结算，历史不追溯改价。暂停定时检查不免除过期保护。手动档案继续按管理员价格运行。

定时检查只能给出有界的发现延迟，不能承诺官网调价瞬间、未公布的合同价或供应商账单绝对一致；CDN、官网结构变化、未支持收费维度和 usage 缺失均可能影响准确性。保护机制的目标是让未知 / 过期价格显式可见且不悄悄用于官方跟随的业务请求。上线前仍应核对供应商账户账单。

## 费用与限额规则

- 月预算使用 UTC 自然月；0 表示不设硬预算。其他地方展示的时间按浏览器时区。
- 文本 Token 预留为请求 JSON 字节数的两倍加固定开销，再加输出上限。该方法偏保守，可能提前排队或拒绝超大请求。它不是供应商 tokenizer，不能保证任意模型的 TPM 口径完全一致。
- 供应商报告 usage 时按实际 Token 数和本次 Profile 价格快照结算；usage 缺失或连接异常时保留预估上限。成本是网关配置的价格计算值，不代替供应商账单；已支持缓存输入独立计费，尚未单独区分推理 / 音频 / 内置搜索等特殊计费。
- 图片目前只支持 `n=1`；尺寸与质量由 Profile 固定，单张价格由管理员配置，记录为估算。不同尺寸 / 质量应建立不同 Profile / 场景。图片编辑、流式图片、音频和视频尚未接入。
- 429 根据 `Retry-After` 冷却共享限额池，再进入队列；503 按退避重试或切换备用 Profile。网络断开可能已经产生供应商费用，因此不会自动重发。RPM / TPM 历史持久化，重启不会立即清空一分钟限额；等待中的请求不持久化。
- 为保持计费边界明确，不接受绕过路由的 `provider`、`fallbacks`、`extra_body`、自带 provider key、服务端工具、媒体输入和有隐藏上下文的 `previous_response_id`。无状态工具调用与 JSON 输出可正常传递。
- Bifrost 配置使用 `max_retries=0`；不要另设透明自动 fallback / retry，避免 llmHub 无法计量隐藏调用。llmHub 是唯一重试与调度入口。
- 同一数据库仅允许一个 llmHub 实例。不要横向扩容，也不要把 SQLite 文件放到网络文件系统。
- 明细最多保留 30 天；场景 / 模型 breakdown 基于现存明细。项目月度汇总长期保留，配置历史保留在 `config_history`。不保存 prompt 或 response 内容。

## 验证与恢复

```bash
node scripts/llmhub-check.mjs
```

此脚本逐项检查 Go race、Go vet、Node / Python 客户端、前端构建、格式；每项都有总时限，超时会终止由它创建的进程组。不会运行付费供应商测试。

启动模拟服务后，用已安装的 Playwright 做 UI 回归：

```bash
node tests/e2e/llmhub/console.mjs
```

也可通过 `LLMHUB_PLAYWRIGHT_MODULE` 指定已有 Playwright 包路径。脚本 90 秒超时，覆盖桌面 / 手机、认证恢复、项目 CRUD、密钥、图片和 SSE，并输出 `test-results/llmhub/` 截图。它是独立 companion UI 的 QA，不启动 Bifrost 原有 E2E fixture 的 backing services。

使用已安装的官方 Bifrost 二进制验证真实网关链路，不需要供应商凭证：

```bash
cd hub
GOTOOLCHAIN=local go build -o /tmp/llmhub-native ./cmd/llmhub
cd ..
BIFROST_BINARY=/absolute/path/to/bifrost-http LLMHUB_BINARY=/tmp/llmhub-native node tests/e2e/llmhub/native.mjs
```

此脚本有 60 秒总时限，启动真实 Bifrost、llmHub 和本地模拟 OpenAI 服务，验证选钥、路由、普通 / SSE 调用与精确 usage 结算，退出时清理自己的服务与临时数据库。已在官方 v2.2.4 macOS arm64 二进制上通过，不能替代目标 Docker 环境或真实供应商联通验证。

详细交付与环境限制见 `status.md`。初期范围不含异步持久化任务、优先级 / 公平调度、组织 / 环境维度、告警、基于质量 / 延迟的智能路由、Prompt 版本管理、质量评估或多实例状态共享。
