# 项目与默认平台 API Key

llmHub 项目访问密钥和平台 API Key 是两种不同的凭证。业务项目通过 `lh_...` 项目访问密钥调用 llmHub；管理员在页面中直接粘贴 OpenAI、DeepSeek 等平台颁发的 API Key。服务器将平台密钥保存到私有 Bifrost，自动生成内部引用。llmHub 配置、版本历史、返回值和请求审计均不保存 / 回显 API Key 明文。

## 控制台配置

1. 在私有 Bifrost 中启用要使用的平台（包含网络地址 / 自定义供应商等设置）。生产配置必须启用持久化 `config_store` 并配置持久加密密钥；仓库 Compose 模板已配置这两项。
2. 在 llmHub 的“设置 → 默认平台 API Key”中，填写平台标识和该平台颁发的 API Key。限额池可留空，自动使用该模型的默认限额设置。
3. 在“项目 → 编辑 → 项目专用平台 API Key”中，为需要独立凭证的平台填写 API Key。项目限额池留空时继承该平台的全局默认池；全局没有指定池时使用模型配置的池。留空不是关闭限流。
4. 已有平台密钥输入框显示“已配置，留空保持不变”，不会填入原始密钥。填写新 API Key 即替换此绑定；移除平台行恢复默认继承，其他平台不受影响。

页面只显示“平台 API Key”“项目专用”“使用默认 API Key”等业务术语。`key_name` 仅为兼容旧配置和内部转发保留，不是让用户填写 Bifrost 生成的访问密钥。

私有 Bifrost 启用管理员认证时，在 llmHub 服务端设置 `LLMHUB_BIFROST_USERNAME` / `LLMHUB_BIFROST_PASSWORD`，使用 Bifrost 管理员账户。它们不在浏览器填写，也不应提交到 Git；Compose 环境模板已列出。认证或保存失败时页面给出不包含密钥的错误信息，原业务配置保持不变。

实际选择顺序按 Profile 的供应商逐项确定：

```text
project.credentials[provider]
    -> default_credentials[provider]
        -> Profile.key_name + Profile.pool_id
```

这里的箭头仅表示“未配置时继承”，不是请求失败时自动切换密钥。项目专用供应商密钥被吊销、名称错误或余额不足时，不会偷偷使用 default。场景已配置的模型备用路由仍按原规则处理；若备用路由使用另一供应商，就按该供应商重新解析项目 / default 配置。

没有新增配置的旧数据库继续沿用 Profile 默认值，不需要迁移或重新签发项目密钥。全局 default 也是按供应商划分，而不是用同一个 API Key 调用所有平台。

## 配置 API

读取 `GET /api/llmhub/config`，在其完整 `config` 中合并下面的字段后，连同读取到的 `version` 通过 `PUT /api/llmhub/config` 保存。下面是字段片段，不是可独立导入的完整配置；所有 `pool_id` 必须已存在于 `pools` 中。

```json
{
  "default_credentials": [
    { "provider": "openai", "key_name": "shared-default", "pool_id": "openai-shared" },
    { "provider": "deepseek", "key_name": "shared-default", "pool_id": "deepseek-shared" }
  ],
  "projects": [
    {
      "id": "storepilot",
      "name": "StorePilot",
      "enabled": true,
      "monthly_budget_usd": 50,
      "credentials": [
        { "provider": "openai", "key_name": "storepilot-prod", "pool_id": "openai-shared" }
      ]
    }
  ]
}
```

同一列表中平台不能重复。平台标识使用实际 Bifrost 推理配置名称，包括已登记的自定义供应商名称，不是官网报价目录的显示名称。`pool_id` 可省略或为空。已有 `key_name` 引用仍兼容；直接保存真实平台密钥时，用写入专用字段 `api_key` 替代 `key_name`：

```json
{ "provider": "openai", "api_key": "YOUR_PLATFORM_API_KEY" }
```

该片段同样需合并进完整配置并携带当前版本。`api_key` 只能提交，读取配置不会返回它。保存前验证全部字段和版本，服务器为新密钥生成引用，再由 Bifrost API 保存；失败 / 并发冲突会尝试清理已确认创建的新密钥。两个服务没有分布式事务，网络中断或进程崩溃时可能存在未绑定的新密钥，需在私有 Bifrost 中检查清理。替换后的旧密钥保留，避免影响已派发请求；不再使用时可由管理员清理。

保存成功只代表格式正确且 Bifrost 已保存，不证明密钥余额、模型权限或平台实际联通；这些需要后续小流量联通测试。页面错误位于对应输入框下，并明确包含平台行号和字段名。

## 限额、预测与审计

- 创建多个 API Key 不代表有多份额度。同一供应商账户 / 组织的共享额度，仍选择同一个限额池；只有实际独立的配额组才配置独立池。
- 平台密钥保存使用 8 秒总超时，失败清理最多 3 秒；网络调用不持有全局配置锁，不阻塞配置读取。未启用 Bifrost 持久化的临时实例不适合保存生产密钥。
- 每次派发先解析有效密钥及池，再进行 FIFO 调度、预算预留和成本预测。价格仍由模型 Profile 决定；本功能不支持不同供应商账户的合同价覆盖。
- 预测历史按项目、场景、供应商、模型、密钥名称和限额池等维度隔离。更换密钥引用或池不会沿用旧缓存命中证据；也不承诺不同密钥之间的供应商缓存共享。
- 请求记录的价格快照保存实际 `key_name`、`pool_id` 和 `applied_price.credential_source`，来源分别为 `project`、`default`、`profile_default`。历史记录不会随配置变化而被改写。
- 排队期间有效密钥 / 池 / 来源发生变化，该请求在派发前返回 `409 credentials_changed`，不产生模型调用和预算扣款；业务端重新提交后使用新配置。已经派发的请求继续使用原快照。Bifrost 内部同名密钥轮换无法由引用变化检测到，轮换时可改名以隔离预测历史。

Bifrost 只应通过私有地址访问。业务项目不能直接访问 Bifrost 控制台或选择其他项目的上游凭证。
