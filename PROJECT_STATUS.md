# n2sql-agent 项目交接状态

> 这是新 Agent 开始工作时的第一份状态摘要。详细设计和学习笔记仍在 `docs/`；本文件只保留当前边界、已完成能力、明确后置项和下一步工作方式。

## 先读什么

1. `AGENTS.md`：必须遵守的开发规范、学习式提问、验证和 Git 约定。
2. `PROJECT_STATUS.md`：当前交接状态和范围边界。
3. `docs/plans/总体计划.md`：长期目标和阶段关系。
4. `docs/plans/第二阶段-Agent平台与后端工程化.md`：Agent 主线的实施顺序。
5. `docs/conversations/` 中与当前切片相关的最新记录：实现原因、用户确认和未决问题。
6. `README.md`：启动方式和可运行接口。

## 项目边界

- `n2sql-agent` 是唯一的独立实现目录；`WeKnora` 只作为架构和产品参考，不直接修改或复制其源码。
- 当前目标是通用文档知识库问答，不引入 SQL/schema/N2SQL 问答主题。
- 后端使用 Go，前端使用 Vue 3 + TypeScript + Vite，数据使用 PostgreSQL + pgvector，PostgreSQL 由 Docker Compose 管理。
- 模型 API Key 只放后端 `.env`，不进入浏览器、数据库、响应或日志。
- 开发采用“学习优先、功能切片优先”：先做一个用户可见的最小闭环，再做必要的构建、静态检查和冒烟验证；用户已明确暂缓大规模评测和测试套件。

## 当前代码状态

- 最新提交：`075c57d feat: track async summary progress in chat`；Agent/RAG 主线最近完成异步文档摘要、摘要状态查询，以及 pending 工具结果短路，避免模型重复调用异步工具。
- 当前工作区在该提交后保持干净。
- 第一阶段知识库问答底座已完成：知识库、文档上传和 Worker、Markdown/TXT/PDF 提取、OCR 最小骨架、父子 chunk、embedding、混合检索、Rerank、引用和普通 RAG/SSE。
- Agent Runtime 最小闭环已完成：Tool Registry、Function Calling、受限 ReAct、最大步数/超时/取消、工具失败安全降级、SSE 事件、上下文摘要、会话历史、幂等和运行摘要。
- Agent 只读工具已覆盖 `knowledge_search`、`document_list`、`document_info`、`document_read`；文档正文读取受知识库、文档、chunk 数量和字节数限制。
- 异步文档摘要工具 `document_summary` 已接入标准 Agent：后台生成时返回任务状态并立即结束本轮 Agent，不把 pending 占位结果再次交给模型，避免重复工具调用和步数超限；摘要完成后由后续提问读取缓存。
- Agent 会话记忆改造第一步：历史语义摘要模型调用失败时最多重试 2 次；上下文取消会立即停止重试，最终仍由现有抽取式摘要兜底，不阻断正常问答。
- Agent Runtime 增加重复工具调用保护：按工具名和规范化后的 JSON 参数去重，同一轮重复调用会安全结束，避免重复检索和步数浪费。
- 检索已具备向量 + PostgreSQL 关键词、RRF 融合、关键词阈值、可选 Rerank、Query Rewrite、缓存、父块上下文去重、HNSW 和检索统计。
- Multi-Agent、只读 MCP 和 PostgreSQL 持久化 A2A 已有最小可运行适配；它们不是完整官方协议或生产级多租户方案。
- 前端已支持会话、引用卡片与懒加载原文、检索统计、Agent 工具轨迹折叠、断线恢复、A2A/协作研究模式、正文分页预览、固定起步问题和按需生成追问建议。
- 最新停止生成切片：标准 Agent 流式回答期间输入栏显示“停止生成”按钮，调用 `POST /api/knowledge-bases/{id}/agent-runs/{runID}/stop` 取消执行上下文；引擎发 `run_canceled` 事件，前端标记独立 stopped 终态（保留部分内容、不显示重新生成、不写会话历史）；断线恢复与用户停止语义分离；停止按知识库 ID 隔离。
- 最新会话置顶切片：`conversations.is_pinned` 列 + 排序索引；列表按置顶优先、组内按更新时间排序；`PATCH /conversations/{id}` 支持 `{"is_pinned": true/false}`（与重命名共用端点，跨库 404）；前端会话列表按「置顶 + 今天/昨天/N 天前」分组，置顶项 📌 标记与置顶/取消置顶按钮。
- 会话体验四连切片：①自动标题——首轮问答后若仍是默认标题，用问题摘要（30 字截断）重命名，不覆盖用户已命名；②追问"换一批"——生成建议后按钮变为换一批，接口无状态直接再次调用；③会话更多菜单——"⋯"菜单（置顶/改名/清空消息/复制 Markdown/删除），清空走 `DELETE /conversations/{id}/messages`（事务删消息/摘要/幂等键，保留会话），复制为纯前端拼 Markdown；④正文内引用标记——提示词要求模型输出 `<kb doc_id pos/>`，前端渲染成可点击引用 pill（事件委托打开原文抽屉），流式显示"〔引用〕"，模型不输出标签时行为不变（来源卡片照旧）。
- 可靠性三连切片：①断流续传——未完成的标准 Agent 运行持久化到 localStorage，刷新后重建占位消息并重连 Hub 重放（后端复用已有 `GET /agent-runs/{runID}/stream`，零新增接口）；②历史消息游标分页——`GET /conversations/{id}/messages` 支持 `before_id`+`limit`（默认 50/上限 200）返回 `{messages, has_more}`，前端滚动到顶部加载更早并保持视口；③实时检索进度——普通 RAG 流式新增 `retrieval_started`/`retrieval_finished` 阶段事件，Agent 轨迹头部流式中显示"正在检索知识库…/搜索完成 · N 条引用"实时徽标。
- 最新工具结果类型化渲染：`document_list`/`document_info` 工具返回有界结构化 metadata（`documents`/`document_info`），engine 在 `tool_finished` 事件透传；前端展开工具轨迹时 document_list 渲染为表格（文档/类型/大小/状态）、document_info 渲染为键值卡片；工具完成 label 按工具名区分（知识库检索完成/查看文档列表完成等）。
- 最新会话内模型切换切片：Provider 增加服务端聊天模型 allowlist，会话保存 `chat_model`；`PATCH /conversations/{id}` 和 Agent 请求支持选择模型，运行时只接受已配置模型；前端设置抽屉可维护可选聊天模型列表，会话顶部选择器会保存并恢复当前模型。Embedding、Rerank、协作研究和 A2A 未改变。
- 最新深度思考展示切片：解析 OpenAI-compatible `reasoning_content`，标准 Agent 在 SSE 中发出有界、脱敏的 `reasoning_delta`；前端显示默认折叠的“深度思考”卡片，内容只保留在当前页面，不写入会话历史。协作研究模式不受影响。
- 最新可控思考与聊天附件切片：标准 Agent 支持 `fast/standard/deep` 思考级别，显式映射为 `reasoning_effort`；输入区支持 PNG/JPEG/WEBP 图片和 TXT/Markdown 文本附件，服务端做数量、大小、base64、UTF-8 和图片签名校验，附件只参与当前一轮模型请求，不写入历史或知识库。
- 最新会话搜索切片：`GET /api/knowledge-bases/{id}/conversations?q=...&limit=...` 搜索当前知识库内会话标题和消息内容；服务端沿用管理员隔离并限制查询长度/返回条数，前端会话栏提供 250ms 防抖搜索和无结果提示。
- 当前会话搜索范围：暂时只保留会话标题搜索，使用标题索引；历史消息跨会话搜索先搁置，避免当前版本引入较慢的消息查询链路。
- 最新会话列表分页：会话列表接口返回 `items/has_more/offset/limit`，默认前端每页加载 30 条；标题搜索和普通列表共用分页查询，前端支持“加载更多”。
- 最新工具审批切片：审批采用工具策略控制；当前只读 RAG 工具默认不弹确认，未来有写入或外部副作用的工具实现 `RequiresApproval() bool` 后才进入审批；进入审批的标准 Agent 会发送 `approval_required`，前端调用 `POST /api/knowledge-bases/{id}/agent-runs/{runID}/approval` 恢复或拒绝运行；审批状态按知识库 ID 和运行 ID 隔离，取消/结束时释放；默认 30 秒未确认会发送 `approval_expired` 并结束本轮；当前为进程内 Hub，Redis/持久化审批后置。
- 最新答案反馈切片：新增 `conversation_message_feedback` 表和消息级 `POST /api/knowledge-bases/{id}/conversations/{conversationId}/messages/{messageId}/feedback` 接口，支持 `rating=1/-1` 并按管理员与消息幂等更新；后端校验会话、知识库、消息归属及 assistant 角色；流式回答保存事件返回 `assistant_message_id`，前端当前回答也能立即显示“有帮助/需改进”。
- 最新反馈统计切片：新增 `GET /api/knowledge-bases/{id}/feedback/stats`，聚合 `total/positive/negative/positiveRate`，前端在问答台展示当前知识库满意率。
- 最新动态起步问题切片：前端根据当前知识库已完成处理的文档标题生成起步问题；选择文档范围后，推荐问题会同步聚焦到选中文档，不增加额外模型调用，模型失败不会影响问答主链路。
- 最新 @ 文档选择切片：聊天输入框输入 `@` 可按文件名搜索并选择已完成文档，已选文档以 chip 展示并可移除；发送时复用已有 `document_ids` 检索范围参数。
- 最新模型设置稳定性修复：模型服务设置面板不再因误触遮罩层而关闭，只能通过右上角关闭按钮或 Esc 退出，避免填写配置时丢失输入。
- 最新本地 Embedding 切片：支持通过 `LOCAL_EMBEDDING_BASE_URL`、`LOCAL_EMBEDDING_MODEL` 和 `LOCAL_EMBEDDING_API_KEY` 将文档/查询向量化切换到本地 OpenAI-compatible 服务；聊天仍使用远程 Provider，未配置时保持原有行为。
- 前端模型服务设置已隐藏嵌入模型输入框：Embedding 由后端本地环境变量控制，保留旧字段仅用于数据库和接口兼容。

## 明确后置或不做

- 暂不把 Redis、跨实例事件持久化、完整 MCP/A2A 规范、复杂沙箱、生产级认证和多租户引入当前主线。
- 暂不实现成功回答后的编辑问题/覆盖式重新生成；这是已确认的范围决定，不能自行恢复。
- 暂不把大规模评测、真实压测和完整测试套件作为 Agent/RAG 功能推进的阻塞项；只保留与改动匹配的必要验证。
- OCR 只保留最小接入和一次验证，不扩展成完整文档解析平台。

## 下一步工作方式

默认继续沿着 WeKnora 对照后的 Agent/RAG 用户可见能力推进：先阅读相关现有代码和 `docs/` 记录，选择一个小而完整的功能切片，再实现后端链路、前端反馈和必要文档。不要因为看到后置项就直接开始 Redis、完整协议或大规模评测。

下一轮候选：对照盘点小/中切片已完成（#1 停止生成、#2 置顶分组、#3 自动标题、#4 换一批、#7 更多菜单、#5 正文引用、#9 断流续传、#8 消息分页、#6 实时检索进度、#12 工具结果类型化渲染、#11 会话内模型切换、#13 深度思考展示、#10 聊天附件/图片、#14 会话搜索、#16 工具审批）。剩余：Web 搜索（#15，边界外）。建议下一步在真实 Provider 环境验收思考参数、视觉模型、会话搜索和审批流程，再评估供应商专属 thinking budget。

## 交付要求

- 修改前先检查 `AGENTS.md`、相关计划、当前实现和参考功能。
- 技术问题先改写成更清晰的问题再回答；开发相关问答和关键决策追加到 `docs/conversations/`。
- 代码保持职责清晰、错误显式、输入和权限有边界；前端改动做真实浏览器冒烟。
- 完成一个可验证切片后运行匹配的验证，检查 `git diff`，创建独立的可读提交。
