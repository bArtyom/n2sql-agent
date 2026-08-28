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

- `n2sql-agent` 是唯一的独立实现目录；`WeKnora` 主要作为产品与 RAG 参考，`deer-flow` 主要作为 Agent Runtime 参考；都不直接修改或复制其源码。
- 当前目标是通用文档知识库问答，不引入 SQL/schema/N2SQL 问答主题。
- 后端使用 Go，前端使用 Vue 3 + TypeScript + Vite，数据使用 PostgreSQL + pgvector，PostgreSQL 由 Docker Compose 管理。
- 模型 API Key 只放后端 `.env`，不进入浏览器、数据库、响应或日志。
- 开发采用“学习优先、功能切片优先”：先做一个用户可见的最小闭环，再做必要的构建、静态检查和冒烟验证；用户已明确暂缓大规模评测和测试套件。

## 当前代码状态

- GraphRAG 第一版已接入：`graph_extract` 异步任务从文本 Chunk 抽取实体/关系，Neo4j 保存实体、关系和 Chunk 引用；查询侧抽取问题实体做一跳召回，并与向量/关键词候选统一融合、Rerank、上下文扩展和引用。Neo4j 通过 `NEO4J_ENABLE` 可选开启，未配置或调用失败时回退原 Hybrid RAG。当前待补已有文档回填、删除/重索引清理和真实 Neo4j 冒烟评测。

- 最新提交：`0aab837 fix: fence agent worker leases`；Agent/RAG 主线最近完成 Worker 租约 fencing，避免旧 Worker 在任务被接管后继续续租或覆盖新 Worker 的终态。
- 当前工作区正在收口 DeerFlow/LangGraph 风格的统一 Agent checkpoint；README.md 保留用户本地启动记录改动，不参与本切片提交。
- 第一阶段知识库问答底座已完成：知识库、文档上传和 Worker、Markdown/TXT/PDF 提取、OCR 最小骨架、父子 chunk、embedding、混合检索、Rerank、引用和普通 RAG/SSE。
- Agent Runtime 最小闭环已完成：Tool Registry、Function Calling、受限 ReAct、最大步数/超时/取消、工具失败安全降级、SSE 事件、上下文摘要、会话历史、幂等和运行摘要。
- Agent 异步运行已接入主聊天链路：标准 Agent 提交接口先持久化 `agent_runs` 并返回 `run_id`，后台 Worker 领取并执行请求快照，执行事件经进程内 Hub 由独立 SSE 连接流式转发；会话保存、审批等待和错误事件均在 Worker 执行上下文中完成。Worker 已增加 5 分钟租约、约 100 秒心跳和过期 `running` 任务回收；旧的同步 POST SSE 逻辑仍保留为无持久化依赖时的回退路径。
- Agent 运行新增按知识库隔离的只读状态接口 `GET /api/knowledge-bases/{id}/agent-runs/{runID}`，返回状态、尝试次数、错误和时间信息，不暴露请求快照；SSE 负责实时事件，状态接口负责刷新和最终状态确认。
- Agent 运行最终 `Response` 已持久化到 `agent_runs.response`；当 SSE Hub 事件过期但任务已完成时，前端可通过状态接口恢复最终回答、引用、统计和轨迹。
- Agent 中间事件支持可选 Redis 短期流：配置 `AGENT_STREAM_REDIS_URL` 后使用 Redis Stream 的有界长度与 TTL，SSE 可跨实例重放并持续订阅；未配置时回退 PostgreSQL 事件表。最终答案、运行状态和会话消息仍持久化到 PostgreSQL。
- Agent SSE 已增加事件版本、稳定事件 ID、`Last-Event-ID` 续传和 `stream_replay_gap` 恢复；前端遇到事件窗口缺口时改读 PostgreSQL 最终答案。Agent 恢复统一由 `agent_checkpoints` 提供，不再依赖多个 checkpoint 表拼接。
- 工具安全重试原则已落地：普通工具默认允许失败后反馈模型重试；实现 `RequiresApproval()` 的副作用工具默认禁止自动重试，只有同时显式实现 `Retryable() bool { return true }` 才允许，便于未来接入业务幂等键后安全恢复。
- Agent checkpoint 已统一为 `agent_checkpoints`：一个快照同时保存消息、压缩摘要和 pending tool calls；Worker 在模型调用前、工具决策后、工具结果写入后和最终答案边界保存。小结果以内联方式保存，大结果通过临时文件引用外置；任务失败接管时读取同一份快照，任务成功后保留为下一轮会话隐藏状态，清空/删除会话时统一清理。
- Agent SSE gap 恢复增强：事件过期后先查询 Run 状态；若仍为 `running`，前端不再携带过期的 `Last-Event-ID`，而是重新订阅 Redis 当前尾部继续接收后续事件；终态任务仍从 PostgreSQL 恢复最终答案，避免慢前端把长任务误判为失败。
- Agent Worker 租约 fencing 已补齐：`agent_runs` 增加 `lease_token`，每次领取任务生成新令牌；续租、成功、失败和取消都必须匹配当前令牌，旧 Worker 即使延迟恢复也不能续租或覆盖新 Worker 的终态。租约过期回收和终态更新会清空令牌。
- Agent Worker 租约丢失主动停止已补齐：心跳续租失败后立即取消本次执行上下文，旧 Worker 不再继续调用模型或工具；数据库 token fencing 继续作为最终写入保护。
- Agent Worker 已区分取消原因：用户取消仍进入 `canceled`；心跳导致的 `ErrLeaseLost` 不写终态，保留运行记录等待租约回收后由下一个 Worker 接管，避免把故障恢复误判成用户主动停止。
- Agent Worker 已增加最大接管次数：租约过期的 Run 最多尝试 3 次；超过次数后进入 `failed` 并记录 `worker lease expired: maximum attempts reached`，避免故障任务无限循环。
- Agent Run 已采用 DeerFlow 风格的失败状态：`agent_runs.status` 区分 `succeeded`、`failed`、`timeout`、`canceled`，`stop_reason` 保存有限的停止原因，`error_message` 保存可读错误；运行时仍可在内存中使用细粒度 `FailureCategory`，但不对外持久化。
- Agent + RAG 支持 `knowledge_policy`：默认 `knowledge_base_preferred` 保持开放 Agent 行为；`knowledge_base_only` 只允许知识库证据，无引用时由后端统一拒答，适合测评知识库拒答率。
- Agent 主线能力已收口：只读工具失败会把结构化错误反馈给模型进行纠错，副作用工具失败不自动重试；Worker 通过租约过期回收恢复崩溃任务；子 Agent 由共享调度器异步执行，所有子任务进入终态后唤醒父 Agent。
- 工具策略层已解耦：`knowledge_base_preferred` 可注入额外工具，`knowledge_base_only` 后端不注册额外工具；子 Agent 继承父 Agent 的业务工具，但不继承 `delegate_research`，避免递归创建子 Agent。
- Agent 循环检测采用两阶段保护：第一次重复工具调用发布 `loop_detected` 并把警告反馈给模型，模型仍重复时才停止本轮，接近 DeerFlow 的 warning/hard-stop 机制。
- 旧的 `agent_run_contexts`、`agent_thread_contexts`、`agent_run_decisions` 和工具专用 checkpoint 逻辑已从当前实现删除；迁移 `000079` 会删除旧表并创建统一的 `agent_checkpoints`，不兼容旧 checkpoint payload。
- Agent checkpoint 写入已改为可降级：保存失败不会让已成功的工具调用和最终问答失败；后续接管时按统一快照是否存在和工具安全策略处理。
- Agent Worker checkpoint 读取也已可降级：恢复存储暂时不可用时记录结构化警告并从空 checkpoint 开始执行，普通只读工具按安全策略重新调用，不再因为恢复元数据故障直接终止主问答。
- Agent 只读工具已覆盖 `knowledge_search`、`document_list`、`document_info`、`document_read`；文档正文读取受知识库、文档、chunk 数量和字节数限制。
- 异步文档摘要工具 `document_summary` 已接入标准 Agent：后台生成时返回任务状态并立即结束本轮 Agent，不把 pending 占位结果再次交给模型，避免重复工具调用和步数超限；摘要完成后由后续提问读取缓存。
- Agent 会话记忆改造第一步：历史语义摘要模型调用失败时最多重试 2 次；上下文取消会立即停止重试，最终仍由现有抽取式摘要兜底，不阻断正常问答。
- Agent Runtime 增加重复工具调用保护：按工具名和规范化后的 JSON 参数去重，同一轮重复调用会安全结束，避免重复检索和步数浪费。
- Agent Runtime 增加运行内上下文预算：单轮模型上下文超过 64KB 时保留系统提示、当前问题和最近工具结果，省略较早内容，避免多步检索结果累积撑爆上下文。
- 检索已具备向量 + PostgreSQL 关键词、RRF 融合、关键词阈值、可选 Rerank、Query Rewrite、缓存、父块上下文去重、HNSW 和检索统计；`retrieval-eval` 还支持人工标注目标文档并输出文档命中率、首次命中排名和 MRR，开始建立可解释的召回质量基线。
- 知识库文档切分开始对齐 WeKnora 自适应策略：Markdown 标题优先保存结构路径，普通文本识别中文/英德文章节、数字和罗马编号、全大写标题、视觉分隔线、PDF 分页符等启发式结构，并保护代码围栏；无结构文档回退原递归切分。
- 文档结构路径已独立为 `heading_path` 元数据：父子 chunk、向量/关键词检索结果、文档原文接口、历史消息引用和前端引用卡片均透传展示；正文不再混入路径前缀，`000044` 迁移会清理旧版本已经写入正文的路径前缀。
- 固定“协作研究”Multi-Agent 模式和 A2A 异步任务模式已全部删除；前端现在只有标准 Agent 对话，后端只保留标准 Agent 的持久化 Worker/SSE。普通 Agent 通过只读 `delegate_research` 工具按需委派子 Agent；子 Agent 只暴露 `knowledge_search`，不会递归委派或执行副作用工具。
- 旧 `internal/multiagent`、`internal/a2a`、相关路由、任务存储、后台循环、指标和配置已清理；新增迁移会删除已有数据库中的 `a2a_tasks` 表。
- 动态子 Agent 第一阶段已完成：`agent_runs` 增加 `parent_run_id` 与 `run_kind`；标准 Worker 会把父 Run 数据库 ID传入 Agent 请求，`delegate_research` 可同步创建并持久化 child Run，保存子 Agent 结果并更新 succeeded/failed/canceled 状态。父工具完成事件会透传有界的 child Run 标识、状态、步数和事件摘要；子 Run 当前仍在父 Run 的工具调用内执行，独立事件流和独立 Worker 调度后置。
- 动态子 Agent 并发调度已补齐：Agent Engine 原有同轮只读工具并行执行能力继续保留；新增进程级共享 `BoundedChildScheduler`，所有父 Run 的子 Agent 共用最多 3 个执行槽位，槽位满时等待，父 context 取消时等待中的子任务立即退出，避免无限 goroutine 和模型并发失控。子 Run 仍保留 PostgreSQL 持久化，暂不拆成独立数据库 Worker。
- 动态子 Agent 事件关联已补齐：子 Agent 关键事件通过 `child_event` 进入父 Run 的现有事件存储、Redis/SSE 和前端轨迹，事件携带 `child_run_id`、`parent_run_id`、子事件类型和有界摘要；不会把完整工具结果重复写入父事件。前端不新增 child SSE，而是在父 Run 结束后拉取安全的父子执行树并用折叠视图展示。
- 父子 Run 查询已补齐：新增 `GET /api/knowledge-bases/{id}/agent-runs/{runID}/children`，按 `parent_run_id` 递归返回最多 8 层安全执行树，包含状态、尝试次数、时间、错误和最终 response，不暴露请求快照、租约 token；数据库查询按知识库隔离。
- 子 Agent checkpoint 复用已补齐：持久化 child Run 使用“父 Run + 研究问题”生成稳定 ID，重试时通过同一个 child Run 读取最新 attempt 的 `agent_checkpoints`；子 Agent Engine 从统一快照恢复消息和 pending tool calls，临时大结果文件只作为快照中的外部内容引用。
- 子 Agent 失败处理已改为 DeerFlow 风格：如果已经拿到检索资料但最终总结失败，返回 `failed + partial_result + resume_available` 以及有界资料预览，让父 Agent 自己判断继续回答还是重新委派；部分失败结果不写入 checkpoint，避免恢复时被误当成成功结果。只有父 context 取消时才继续向上抛出错误；重试仍复用同一 child Run 和最新安全 checkpoint。
- 前端已支持会话、引用卡片与懒加载原文、检索统计、Agent 工具轨迹折叠、断线恢复、正文分页预览、固定起步问题和按需生成追问建议。
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
- 最新文档切分质量切片：保留原始上传文件和文档记录，只清理旧 chunk 中混入正文的结构路径前缀；新切分统一保护 Markdown 代码块、表格和 LaTeX，生成切分策略、结构数量、保护块数量、Chunk 长度分布、短块/超长块统计并保存到文档元数据；正文预览接口和前端预览面板展示这些诊断信息及 heading path。
- 最新结构路径 Embedding 切片：真实标题路径作为临时 Embedding 上下文，和文档标题、正文一起发送给向量模型；“第 1 段”等虚拟定位路径只用于引用和预览，不进入向量。关键词检索新增独立 `heading_search` tsvector，真实路径按较低权重参与召回，不修改正文 `content_search`。
- 最新文档重新索引切片：新增 `POST /api/knowledge-bases/{id}/documents/{documentID}/reprocess`，重新排队已有文档的切分、特殊内容保护、标题路径 Embedding 和向量写入；复用原始文件，不重复上传，处理中任务拒绝重复排队，前端资料架增加“重处理”入口。
- 最新检索可解释性切片：关键词结果保留独立的 `headingScore`，用于区分正文命中和标题路径命中；标题路径分数进入历史引用快照和前端 Source 类型，便于后续评测与调权。
- 最新 RAG 评测切片：`retrieval-eval` 报告增加标题路径命中数量、首个相关文档的匹配类型和标题路径分数，评测结果可以区分“正文命中”和“章节标题命中”，不调用聊天模型。
- 最新引用解释切片：引用卡片同时展示正文关键词分数和标题路径分数；`hybrid` 结果表示向量与关键词两路召回合并，用户可以看到结构路径是否参与了命中。
- 最新检索调试解释切片：搜索接口支持可选 `debug=true`，后端对最终有界结果生成 `explain` 数组，说明向量、关键词、标题路径和融合分数如何共同形成命中原因；默认关闭，不改变 Agent/RAG 主流程。
- 最新严格证据门禁切片：严格知识库模式不再把 `document_list`、`document_info` 等元数据调用当作回答依据，只有知识检索、正文读取和文档摘要结果才能解除拒答，降低知识库拒答率评测中的“假有证据”。
- 最新预生成摘要切片：文档索引任务成功后异步触发摘要生成，用户第一次询问“总结这篇文档”时优先读取已缓存摘要，不再把摘要模型调用放进交互 Agent 步数预算。摘要保存到 `documents.summary` 后，再生成一个 `chunk_kind=summary` 的向量/关键词检索 Chunk；正文读取、邻居扩展和再次摘要读取都过滤该类型，避免摘要递归和正文污染。摘要生成或摘要向量化失败不会让原始文档索引失败，后续仍可通过 `document_summary` 重试。
- 最新摘要引用隔离切片：摘要 Chunk 与正文第 0 段使用不同来源身份；正文保持 `document_id:position`，摘要使用 `document_id:position:summary`。Agent 工具事件去重、会话来源快照、trace source keys 和前端来源合并均使用该稳定身份，避免摘要与正文互相覆盖。
- 最新 Hybrid 摘要隔离切片：RRF 向量/关键词融合也按 `chunk_kind` 区分来源身份，避免摘要和正文在融合阶段提前合并；摘要可以独立参与召回、排序和引用。
- 最新摘要证据标注切片：摘要结果注入模型上下文时增加 `[文档摘要]` 标签，前端引用卡片和详情面板显示“文档摘要”，与原文段落区分，降低把概览内容当成逐字原文的风险。
- 最新存量摘要回填切片：服务启动后扫描当前用户已有文档，为索引成功且摘要未完成的文档异步排队生成摘要；跳过仍在处理、摘要处理中和已完成摘要的文档，不阻塞服务启动，也不影响原始 RAG。
- 最新摘要索引可靠性切片：新增独立的 `summary_index_status`；摘要文本保存成功但向量化或 Chunk 写入失败时，不再重复调用摘要模型，后续只重试摘要 Embedding/索引写入，减少成本并保持摘要缓存可用。
- 最新摘要索引重试修复：缓存摘要进入只索引重试队列后，Worker 直接执行 Embedding/Chunk 写入，不重复抢占 `processing` 状态；增加测试确保聊天模型调用次数为 0。
- 最新摘要检索评测切片：运行时统计 `summary_candidates`，评测报告统计 `summary_hits`，可以观察摘要是否进入候选以及是否命中相关答案，避免只看最终回答而无法判断摘要索引的实际效果。

## 明确后置或不做

- Redis 事件流已作为 Agent 短期事件能力接入；Redis-backed 任务队列、完整 MCP 规范、复杂沙箱、生产级认证和多租户仍后置。
- 暂不实现成功回答后的编辑问题/覆盖式重新生成；这是已确认的范围决定，不能自行恢复。
- 暂不把大规模评测、真实压测和完整测试套件作为 Agent/RAG 功能推进的阻塞项；只保留与改动匹配的必要验证。
- OCR 只保留最小接入和一次验证，不扩展成完整文档解析平台。

## 下一步工作方式

后续按双主线推进：Agent Runtime 先读 DeerFlow，再用 WeKnora 校准工具、会话和用户可见交互；RAG 以 WeKnora 的文档处理、混合检索、引用和知识库交互为主。每轮选择一个小而完整的功能切片，完成后端链路、必要前端反馈、测试和学习记录；不要因为参考仓库已有复杂能力就直接扩展成完整协议或大规模系统。

下一轮候选：验证父子执行树在历史消息恢复时的展示，并继续完善 Agent 任务级的失败分类与可观测性。

## 交付要求

- 修改前先检查 `AGENTS.md`、相关计划、当前实现和参考功能。
- 技术问题先改写成更清晰的问题再回答；开发相关问答和关键决策追加到 `docs/conversations/`。
- 代码保持职责清晰、错误显式、输入和权限有边界；前端改动做真实浏览器冒烟。
- 完成一个可验证切片后运行匹配的验证，检查 `git diff`，创建独立的可读提交。

## OCR 与图片文档解析（2026-08-22）

- 对齐 WeKnora 的第一步图片解析：上传入口、LocalFileStore、数据库 content_type 约束现在支持 PNG、JPEG、WEBP。
- 图片文档进入 Worker 后复用现有 Vision/OCR 服务，识别出的文本继续走原有结构化切分、Embedding、关键词检索和引用链路；原始图片仍保存在上传目录，不把图片 Base64 写入 Chunk 正文。
- OCR 请求新增 MIME 类型，避免把 PNG/WEBP 错误地伪装成 JPEG；扫描 PDF 仍由 `pdftoppm` 按页渲染并保持页序后 OCR。
- 当前仍是“文本可检索”的最小闭环，图片资源独立引用、混合 PDF 按页判断、表格/图片多模态回答和多解析器引擎注册属于下一步。
- 图片引用预览已接入：图片检索结果增加 `assetUrl`，前端可通过 `GET /api/knowledge-bases/{id}/documents/{documentID}/asset` 查看原图；接口重新校验知识库归属，只允许图片 MIME，使用 `Content-Disposition: inline` 返回。
- 统一解析边界已开始落地：新增 `ParseResult`、`ParserEngine` 和 `ParserRegistry`；现有 `simple`、`office`、`pdf`、`image_ocr` 引擎通过注册表选择，旧 `Extract()` 入口保持不变，新增 `ExtractResult()` 暴露 Markdown、图片资源和解析元数据。
- 解析资源持久化已接入：新增 `documents.parser_metadata` 和 `document_assets`；Worker 在 Chunk 成功后保存解析器元数据、独立图片文件和资源记录，重处理会幂等替换旧资源并清理旧嵌入图片文件。
- DOCX/PPTX 会从 `word/media`、`ppt/media` 提取 PNG/JPEG/WEBP 内嵌图片；检索结果增加 `assetUrls`，新增资源列表和按资源 ID 的受权限保护预览接口。
- 混合 PDF 已按页处理：`pdftotext` 先尝试提取单页文字，只有文字为空的页面才走图片 OCR；扫描页会保留页码、OCR 文本和原始页面图片，配置项为 `OCR_TEXT_BIN`。
- 解析引擎支持显式选择：默认按 MIME 自动选择 `simple`、`office`、`pdf`、`image_ocr`；配置 `DOCUMENT_PARSER_ENGINE` 后按引擎名称强制选择，名称不存在或不支持该 MIME 时明确失败，便于后续接入 MinerU、PaddleOCR-VL、远程 OCR 等引擎。
- 知识库解析规则已接入：`PATCH /api/knowledge-bases/{id}` 可保存 `parser_engine_rules`，例如 `pdf -> mineru`、`docx -> office`；Worker 领取任务时读取规则并按文件扩展名/MIME 解析。另提供受允许主机限制的统一 HTTP Parser Engine，远程服务只需返回 `markdown/images/metadata` 即可复用后续索引链路。
- 解析器目录已接入 `GET /api/parser-engines`：返回已注册引擎、支持的 MIME、可用状态和不可用原因；例如未配置 OCR 时 `image_ocr` 会显示不可用，而不会等上传后才失败。
- 已接入真实 provider 协议：`DOCUMENT_PARSER_MINERU_URL` 对接 MinerU `/file_parse` multipart，`DOCUMENT_PARSER_PADDLEOCR_VL_URL` 对接 PaddleOCR-VL `/layout-parsing` JSON；两者都转换为统一 `ParseResult`，保留页码、图片和 parser metadata，并沿用允许主机校验。
- WeKnora Cloud 解析器已接入：使用后端环境变量中的 App ID/API Key 生成签名，提交 `/reader` 后轮询任务结果；密钥不进入知识库配置或前端，结果中的图片和 Markdown 同样进入统一资源链路。

## 独立 DocReader 解析边界（2026-08-24）

- 新增 `proto/docreader/v1/docreader.proto` 和 Go gRPC 代码；DocReader 使用 server-streaming 返回 `STARTED`、Markdown 分块、图片分块、`COMPLETED`/`FAILED` 事件。
- 新增 `cmd/docreader` 独立解析进程，并抽出 `internal/documentruntime` 共享解析器装配；主服务与 DocReader 使用同一套 simple/office/PDF/OCR/MinerU/Paddle 配置，避免规则分叉。
- 主服务新增 `DocReaderExtractor`：Worker 只通过窄接口调用远程解析；客户端检查事件序号、图片结束标记和总大小，并对通信级错误有限重试。DocReader 不可用时可按配置回退本地解析，健康远程解析器返回的格式错误不会被静默吞掉。
- `.env` 可配置 `DOCUMENT_READER_GRPC_URL`、`DOCREADER_ADDRESS`、超时、重试次数和本地回退。未配置时保持原来的进程内解析，配置后可把多个 DocReader 放到 gRPC 负载均衡地址后独立扩容。

## PDF 版面块与解析尝试历史（2026-08-24）

- PDF 解析结果已增加统一 `PDFLayoutBlock`/`PDFSpan` 树：块和 Span 保存页码、类型、阅读顺序、文本和坐标；PaddleOCR-VL `prunedResult.parsing_res_list` 的 text/table/figure/header/footer/footnote/formula/code 块会被转换并随 ParseResult、gRPC、文档、检索结果和引用快照贯通。
- 混合 PDF 先按页号分桶，再统一排序后合并原生文字、OCR 文字、图片资源和版面树，修复原生页与 OCR 页拼接导致的页序错乱。
- `documents.parser_layout` 持久化版面树；`document_assets` 增加 `bounds/block_order/span_id`；引用按 `[Page N]` 只返回相关页面的有界版面块，避免把整份版面树重复塞进每条引用。
- 新增 `document_parse_attempts` 表，Worker 每次解析记录 `running/succeeded/failed/canceled`、解析器、错误码、错误摘要和时间；任务表继续负责当前状态与重试调度，两者职责分离。
- 当前统一层不伪装完成复杂跨页表格合并、多栏阅读顺序和页眉页脚识别；这些能力依赖真实 DocReader/PaddleOCR-VL/MinerU 输出，服务层先负责保真保存、页序、引用和历史追踪。
- PDF 强制扫描配置已改为页级回退：有页面检查/渲染能力时，每页优先调用版面分析，单页失败再调用 OCR；只有页级能力不可用时才回退整份 OCR。当前仍不使用简单空格规则自动猜测原生表格页，避免误判普通排版；需要完整表格结构时通过解析规则选择 MinerU/PaddleOCR-VL。

## 知识库文档目录与范围检索（2026-08-23）

- 新增 `documents.folder_path` 及索引；上传、列表筛选、目录树、批量移动、目录重命名已完成。
- `folder_path` 已贯通向量/关键词检索、Hybrid 缓存、同步 Chat、流式 Chat、Agent 和子 Agent；目录范围由服务端固定，不由模型决定。
- 下一步可在前端把目录树选择直接绑定到 `folder_path`，并继续补 WeKnora 的文档处理配置与结构化解析差异。

## 上传级解析配置快照（2026-08-23）

- 上传接口支持可选 `process_config`，当前覆盖 `parser_engine_rules`；未知字段、过大 JSON 和非法规则在 Handler 与领域层双重拒绝。
- `document_processing_tasks.process_config` 保存上传时的解析配置快照：未传覆盖时冻结当时知识库规则，传入覆盖时只应用于本次任务。
- Worker 领取任务只读取任务快照并选择 `ParserEngine`，重试复用同一配置，重处理复制最近一次任务配置；这样知识库全局设置变化不会改变已经排队的任务。
- `process_config.chunking_config` 已接入父子分块：支持 chunk/overlap、父子目标长度和 auto/heading/heuristic/recursive 策略；Worker 为每个任务临时创建分块器，默认配置保持原有行为。
- `process_config.parser_engine_overrides` 已接入统一 `ParseRequest.EngineOptions`，当前可用 `pdf_force_scanned=true` 强制扫描 PDF 走 OCR，并在 parser metadata 标记强制模式。
- 文档 reprocess 接口支持 `{ "process_config": {...} }`，有配置时创建新的任务快照，空 body 时复用上次快照；XLSX 规则支持按首行表头生成 Markdown 表格。
- 已补批量 reprocess：`POST /api/knowledge-bases/{id}/documents/reprocess` 在事务中为多个文档创建任务，校验知识库归属、去重 ID、拒绝 active task，并让每个任务保留独立解析/分块配置快照。

## WeKnora 风格分块配置与上传前试切（2026-08-24）

- `process_config.chunking_config` 已补齐 `separators`、`token_limit`、`languages` 和 `enable_parent_child`；自定义分隔符会保留换行符、句号等原始字符，不会被空白清理逻辑误删。
- `token_limit` 是 tokenizer 无关的近似上限：中文按字符近似 token，英文按约 4 个字符近似 token。它会限制实际切分目标长度，并在诊断中输出 `approxTokenCount`、`maxApproxTokenCount`；正式 Embedding 服务仍以自身 tokenizer 为最终边界。
- `languages` 是切分和 token 估算的提示元数据，当前影响中日韩字符的近似统计；它不会改变正文，也不会被写进 Embedding 文本。
- `enable_parent_child=false` 时正式 Worker 使用平面 Chunk，不创建父子索引，但仍通过 `ReplaceStructured` 保存语义 `heading_path`；默认值保持父子 Chunk 行为。父子模式和非父子模式使用同一套分隔符、token 限制和策略选择。
- 新增只读试切接口 `POST /api/knowledge-bases/{id}/chunk-preview`：支持 JSON 内联文本和 multipart 文件，不写文档、Chunk、Embedding、图片资源或 PostgreSQL 诊断；返回解析器元数据、Chunk 示例、标题路径、父子关系和诊断。
- 试切结果包含平均长度、长度标准差、近似 token 数、最大近似 token 数、特殊块计数、质量问题和每个候选策略的拒绝原因；响应只保留最多 200 个 Chunk、单块最多 6000 字符，防止预览接口被大文档拖垮。
- 正式入库与试切共用 `ParserRegistry`、`SplitOptions` 和 `AdaptiveSplitter`，上传前看到的策略和长度结果就是 Worker 后续实际使用的配置，而不是另一套仅供前端展示的算法。

## 文档标签与范围检索（2026-08-23）

- 已新增知识库标签定义表 `knowledge_base_tags` 和文档标签关联表 `document_tags`；标签是元数据，不进入正文和 Embedding。
- 已新增标签 CRUD、文档标签原子替换接口，并在 HTTP 边界统一校验名称、颜色、数量和重复 ID。
- `TagIDs` 已贯通向量召回、关键词召回、Hybrid、缓存、Chat、流式 Chat、Agent 与异步子 Agent；SQL 在召回前过滤，避免内存过滤造成排序污染或权限边界泄漏。
- 标签范围写入检索缓存键；子 Agent 继承后端标签边界，不能通过工具参数扩大范围。
- 当前多标签语义是“命中任意标签”；后续如需“同时命中全部标签”，应增加显式匹配模式。
- 上传支持 `tag_ids` 并在文档创建事务内完成标签归属校验和绑定；文档列表支持标签筛选，并可和目录范围组合。
- 文档列表及 `document_list/document_info` 返回标签元数据，前端或 Agent 不需要为每个文档再次请求标签接口。

## Agent 尝试历史与父子执行树（2026-08-23）

- 新增 `agent_run_attempts` 表，持久化每次执行尝试的 `running/waiting_children/succeeded/failed/timeout/canceled/requeued` 状态、错误摘要、停止原因和时间。
- Claim、完成、等待子 Agent、租约过期恢复均在同一事务中同步更新 `agent_runs` 与 attempt 历史；Worker 崩溃后不会丢失上一轮失败原因。
- 新增尝试历史接口：`GET /api/knowledge-bases/{id}/agent-runs/{runID}/attempts`；父子执行树接口同时返回每个节点的脱敏 `attempts`。
- 旧 `agent_runs.attempt_count` 在迁移时回填为历史记录；未开始的 pending 不回填，已有执行次数的 pending 标记为 `requeued`。
- 下一步继续完善 Agent 主线的历史恢复展示和任务级可观测性，再回到 WeKnora 风格的文档处理/RAG 差异补齐。

## WeKnora 风格图片检索子块（2026-08-23）

- 图片解析结果继续统一走 `ParseResult`，但 `ImageAsset` 额外携带 `OCRText`/`Caption`，原图字节只在当前 Worker 内存中流转。
- 新增 `document_chunks.chunk_kind` 的 `image_ocr`、`image_caption` 类型和 `image_info` JSONB；图片子块复用父文档块关系，记录资源序号、文件名、页码和来源，不把 Base64 写进数据库。
- 文档 Worker 在正文父子 Chunk 成功提交、图片资源保存后只保存解析器已有的 OCR/Caption 文本，并把模型增强派发给独立图片任务 Worker；图片语义索引失败不回滚已经成功的正文索引，重新处理时旧图片任务和子块随资产级联清理。
- 图片子块进入现有向量、tsvector 关键词和 Hybrid 融合检索，命中结果携带 `imageInfo`、原图 `assetUrls`，引用读取接口支持 `kind=image_ocr/image_caption`。
- 新增可选 `ImageEnricherService`：复用视觉模型为嵌入图片补 OCR；配置 `IMAGE_CAPTION_PROMPT` 后才额外请求图片描述，留空时不增加第二次视觉调用。图片原图仍通过受知识库权限保护的资源接口返回。

## 图片 OCR/Caption 独立异步任务（2026-08-24）

- 正文文档 Worker 不再同步调用图片 OCR/Caption；正文 Chunk、解析元数据和图片资源保存成功后，成功钩子只向统一 `document_postprocess_tasks` 幂等派发任务。

### 统一知识后处理队列（2026-08-24）

- 新增 `document_postprocess_tasks` 统一任务表，任务类型覆盖 `document_summary`、`summary_index`、`image_ocr`、`image_caption`、`recommended_query`，并预留 `follow_up`、`faq`、`wiki` 类型。
- 任务统一记录状态、尝试次数、错误、租约、模型供应商/模型名、输入输出 Token、估算成本和耗时；使用 `FOR UPDATE SKIP LOCKED` 领取，租约过期后可被其他 Worker 接管。
- 摘要生成、摘要向量化、图片 OCR、图片 Caption、文档推荐问题现在由同一个 `postprocess.Runner` 调度，任务之间通过成功后的后续任务派发衔接。摘要文本先落库，摘要索引失败时只重试 Embedding/Chunk 写入。
- 图片模型输出在索引前先写入任务结果，Embedding 或 Chunk 写入失败时重试会复用 `result_text`，不会重复调用 OCR/Caption 模型。
- 新增状态接口：`GET /api/knowledge-bases/{id}/documents/{documentID}/postprocess-tasks`；旧图片任务表由迁移 `000064_unify_document_postprocess_tasks` 合并并删除。
- 统一并发和重试配置：`POSTPROCESS_TASK_CONCURRENCY`、`POSTPROCESS_TASK_MAX_ATTEMPTS`、`POSTPROCESS_TASK_LEASE`。
- 每个图片资源最多拆成两个独立任务：`task_kind=image_ocr` 和 `task_kind=image_caption`。两者有独立状态、尝试次数、结果缓存、错误信息和租约，不会因为 Caption 失败而重复 OCR。
- 统一后处理 Worker 使用 `FOR UPDATE SKIP LOCKED` 领取任务；默认启动 `POSTPROCESS_TASK_CONCURRENCY=2` 个执行槽位。租约过期会重新回到 pending，失败按 `POSTPROCESS_TASK_MAX_ATTEMPTS` 重试，超过次数进入 `dead_letter`。
- OCR/Caption 模型结果先写入任务的 `result_text`，再单独生成 Embedding 并通过 `UpsertImageChunk` 写入对应图片子 Chunk。Embedding 或数据库写入失败时，下一次重试可以复用已经完成的模型结果。
- `UpsertImageChunk` 只替换同一图片、同一类型的旧结果，并锁定文档分配引用位置，不会删除其他图片或另一种类型的子 Chunk；图片任务可以并发执行。
- 统一状态接口：`GET /api/knowledge-bases/{id}/documents/{documentID}/postprocess-tasks`，用于查看所有后处理分支的状态、模型信息、Token、成本和耗时。Caption 只有配置 `IMAGE_CAPTION_PROMPT` 或解析器已经提供 Caption 文本时才会派发。
- 这一步对应 WeKnora 的“原图资源 + OCR 子块 + Caption 子块 + 父块关联”核心模式；后续可继续补图片级精准 URL、OCR/VLM 异步任务状态和复杂表格/版面引用。

## WeKnora 风格 PDF 逐页解析（2026-08-23）

- 已完成原生 PDF、扫描 PDF 和混合 PDF 的逐页路由：先读 PDF 内置文字，富文本页直接保留，空/低文本页才渲染和 OCR。
- 已接入可选 PaddleOCR-VL 版面分析：text/table 进入 Markdown，figure 进入图片资源；布局失败按页降级整页 OCR。
- 已接入可选 Poppler `pdfimages` 提取原生 PDF 内嵌图片，并复用图片资源、OCR/Caption 子 Chunk 和 Hybrid 检索链路。
- ParseResult metadata 记录页面数量、OCR 页、布局模式、图片数量和失败页；Worker 只消费统一 ParseResult，不感知 PDF 解析细节。

## 2026-08-24 评测与存储运维基础

- 评测增加数据集版本、知识库/模型配置快照、Run/Case 耗时、Token、估算成本、失败题数和单题状态。
- 评测 Worker 现在按题重试，默认最多 2 次，单题失败会持久化后继续其他题；评测结果接口返回这些统计。
- 模型 runtime 统一观测 Chat、Embedding、Rerank、OCR 的调用成功率、耗时和 Token；进程指标保持低基数聚合。
- 新增 `internal/blobstore` 的 `Put/Open/Delete` 边界和有上限读取辅助；当前适配本地文件，后续可替换对象存储。
- 图片 OCR/Caption 的独立队列与并发 Worker 已存在，本次复用该链路并补上模型调用观测。

## 2026-08-24 评测质量指标与严格拒答

- RAG 单题结果新增 `faithfulness`、`answer_relevance`、`citation_recall`、`citation_precision`；它们先采用无模型调用的确定性基线，不冒充 LLM Judge。
- 评测 Worker 将数据集 qrels 转换为 `expected_relevant`，并把单题 `refused/correct_refusal` 写入 `evaluation_case_results`；Run 累计 `correct_refusals`、`false_refusals`、`unsupported_accepts`。
- 评测查询接口现在直接返回严格拒答的 `recall`、`refusal_rate`、`accuracy`，以及相关题/无关题和三类拒答计数。
- 没有参考答案的题仍然统计 RAG 质量基线；BLEU/ROUGE 仍只在提供参考答案时统计。拒答题不参与正常答案质量均值。
- 新增迁移 `000066_add_evaluation_refusal_metrics`；完整 `go test ./...` 已通过。
- 评测 Run 查询新增多题 `metrics` 汇总：检索、BLEU/ROUGE、RAG 质量指标分别使用独立分母；失败题和拒答题不会错误污染正常答案质量均值。
- 汇总计算集中在 `internal/retrievaleval/metric_summary.go`，HTTP 层只负责转换持久化 Case 结果；服务重启后仍可从数据库中的 Case 结果重建汇总。

## 2026-08-24 评测入口分层

- 评测入口分为：已有知识库快速回归、固定知识库快照对比、临时知识库完整数据集评测、离线检索评测和完整 RAG 评测。
- 当前已完成已有知识库上的 RAG 评测和离线检索指标基础；临时知识库导入、自动解析/切分/Embedding、PID 到多 Chunk 映射尚未完全接入。
- `knowledge_base_snapshot` 当前保存评测时的快照信息，但检索仍需进一步绑定不可变文档/Chunk 版本，才能保证跨 Run 的严格可复现。
