# Query Rewrite 与 Multi-Query

## 背景

用户的原问题有时过短、口语化或缺少检索关键词。单次向量检索可能漏掉文档，因此在现有 hybrid retrieval 之前增加可选的查询改写。

## 实现

- `retrieval.QueryRewriter` 是可选接口，原问题始终保留。
- `modelruntime.QueryRewriteService` 调用当前聊天模型，要求只返回 JSON 字符串数组。
- 每次最多增加 2 个查询变体；变体按大小写、空白归一化后去重。
- 每个查询分别走 embedding、pgvector 向量召回和 PostgreSQL 关键词召回。
- 所有候选按 `document_id + position` 去重，最后继续复用原有 rerank。
- `query_rewrite` 已接入普通搜索、RAG、标准 Agent、协作研究和 A2A；A2A 任务会持久化该开关。
- 前端问答台增加“多查询改写”复选框。

## 注意事项

1. 打开开关会额外调用一次聊天模型，并可能额外调用最多两次 embedding，所以会增加 token、延迟和费用。
2. 变体不是最终答案，只用于扩大召回；回答模型仍只接收检索结果。
3. 原问题永远先检索，模型改写失败或返回非法 JSON 时当前请求会失败，不会悄悄使用不可信的自由文本。
4. 查询变体数量、问题长度和响应大小都有上限，避免成本和上下文无限增长。
5. 变体仍继承知识库 ID、文档 ID 和 Agent 工具权限边界，不能借改写跨知识库访问。

## 验证

- `go test ./...`
- `go vet ./...`
- `git diff --check`
- `frontend/npm run build`

没有调用真实模型，未验证真实 Provider 的 JSON 遵循程度；后续可在确认 token 预算后做一次手动验收。

## 可靠性与性能优化（2026-08-13）

- 改写模型未配置、返回错误或返回空/非法变体时，不再让整个问答失败；检索自动退回原问题，并通过有限状态标记 `fallback`。
- 原问题和最多两个改写问题在 retrieval 层并行执行；每路仍受原有候选上限、文档范围和 context 取消约束，结果完成后统一去重并继续 Rerank。
- Agent Run 统计和 RAG 响应增加受限的 `query_rewrite` 状态，只记录是否启用、是否实际应用、是否降级和变体数量，不记录改写文本。
- 前端在 SSE/Agent/A2A 结果下显示检索策略状态，让用户能区分“扩大召回”和“降级为原问题检索”。

## 验证

- `go test ./...`
- `go vet ./...`
- `git diff --check`
- `cd frontend && npm run build`

没有调用真实 Provider；前端构建通过，但本轮未使用真实浏览器完成带模型的端到端请求，因此仍需配置 API Key 后手动观察一次成功改写和一次降级提示。
