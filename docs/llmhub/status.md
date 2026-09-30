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
