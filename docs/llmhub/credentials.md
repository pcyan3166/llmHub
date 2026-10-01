# 项目与 default 上游密钥

llmHub 项目密钥和供应商 API Key 是两种不同的凭证。业务项目继续使用自己签发的 `lh_...` 项目密钥；真实供应商密钥只在私有 Bifrost 中保存，llmHub 保存 `key_name` 引用，不复制密钥明文。

## 控制台配置

1. 在 Bifrost 控制台为供应商添加密钥并命名，例如 `shared-default` 和 `storepilot-prod`。名称必须与 llmHub 引用完全一致。
2. 在 llmHub 的“系统设置 → Default 上游密钥”中，为每个供应商设置默认密钥名称及上游限额池。
3. 在“项目 → 编辑”中添加项目专用供应商密钥及限额池。移除某个供应商的项目配置后，该供应商恢复 default 继承；其他供应商不受影响。

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

同一列表中供应商不能重复，每个绑定必须包含供应商、密钥名称和限额池。供应商标识使用实际 Bifrost 推理配置名称，包括已登记的自定义供应商名称，不是官网报价目录的显示名称。服务端验证配置格式和限额池引用；Bifrost 是否真的存在该密钥、它能否访问指定模型，需要通过实际联通测试确认。

## 限额、预测与审计

- 创建多个 API Key 不代表有多份额度。同一供应商账户 / 组织的共享额度，仍选择同一个限额池；只有实际独立的配额组才配置独立池。
- 每次派发先解析有效密钥及池，再进行 FIFO 调度、预算预留和成本预测。价格仍由模型 Profile 决定；本功能不支持不同供应商账户的合同价覆盖。
- 预测历史按项目、场景、供应商、模型、密钥名称和限额池等维度隔离。更换密钥引用或池不会沿用旧缓存命中证据；也不承诺不同密钥之间的供应商缓存共享。
- 请求记录的价格快照保存实际 `key_name`、`pool_id` 和 `applied_price.credential_source`，来源分别为 `project`、`default`、`profile_default`。历史记录不会随配置变化而被改写。
- 排队期间有效密钥 / 池 / 来源发生变化，该请求在派发前返回 `409 credentials_changed`，不产生模型调用和预算扣款；业务端重新提交后使用新配置。已经派发的请求继续使用原快照。Bifrost 内部同名密钥轮换无法由引用变化检测到，轮换时可改名以隔离预测历史。

Bifrost 只应通过私有地址访问。业务项目不能直接访问 Bifrost 控制台或选择其他项目的上游凭证。
