# 第二阶段：Agent Runtime 主线与必要后端工程化

## 阶段目标

在第一阶段 RAG 底座上，完成一套规模可控但面试可讲的 Agent 最小闭环，并继续沿着 DeerFlow 的 Runtime 设计和 WeKnora 的产品交互演进；复杂分布式能力按风险逐步引入：

1. 能独立实现 Tool Registry、Function Calling、ReAct 和上下文管理。
2. 能演示“知识库检索工具 + 最终回答”的 Agent Run，并通过 SSE 展示事件。
3. 能处理步骤数、耗时、Token、取消、超时和工具错误等执行边界。
4. 为后续队列、重试、限流和性能工程保留清晰接口，而不是提前引入无关组件。

## 双参考阅读顺序

只按问题阅读，不从头到尾通读仓库：

- WeKnora 产品链路：Agent 循环、工具注册、会话消息、知识库工具、审批、停止生成、引用和流式事件。
- WeKnora RAG 链路：文档解析、chunk、向量/关键词检索、RRF、Rerank、引用和会话知识库交互。
- DeerFlow Runtime 链路：`backend/docs/STREAMING.md`、`backend/docs/RUN_EVENT_STREAM.md`、Run Manager、Worker lease、StreamBridge、checkpoint 和子 Agent 事件。
- DeerFlow 工具消息链路：LLM tool call、ToolMessage 配对、工具错误恢复、工具进度和上下文压缩。
- n2sql 每次只实现一个缩小版，不复制仓库结构；先写“问题—不变量—最小实现—验证”四段笔记。

每个模块阅读后都写三句话：它解决什么问题、关键不变量是什么、如果缩小到 n2sql 会保留什么。

## 开发切片与验收

### 切片一：Agent 最小运行模型

实施顺序：

1. 定义 `Agent`、`Tool`、`ToolRegistry`、`AgentRun`、`Step` 和 `Event` 的最小接口。
2. 将现有知识库检索封装为第一个只读工具，并实现工具描述和参数校验。
3. 接入模型 Function Calling：解析工具调用、执行工具、回传工具结果。
4. 实现受最大步数、总耗时、Token/费用和连续失败次数限制的最小 ReAct 循环。
5. 复用现有 SSE 通道输出 `run_started`、`tool_called`、`tool_finished`、`message_delta` 和 `run_finished` 事件。
6. 保留现有 PostgreSQL 任务表与 Worker；只补齐 Agent 必需的上下文取消、超时和错误边界，不在本切片引入 Redis。

验收：能够演示“用户问题 → Agent 决策 → 知识库检索 → 工具结果 → 最终回答”；正常、参数错误、工具失败、超时、取消和最大步数都有测试。

### 切片二：上下文、记忆与安全

- 区分系统提示、用户输入、工具描述、工具结果和历史消息。
- 实现有限窗口、历史压缩或摘要，并保留来源引用。
- 对工具结果做长度限制和错误归类，避免一条结果撑爆上下文。
- 设计 prompt injection、越权工具、敏感数据回显和 SSRF 的防护边界。

验收：能够解释短期记忆与长期记忆的区别；能够测试超长历史、恶意工具结果和无权限工具调用。

### 切片三：队列可靠性与可观测性

本切片进入第三阶段后实施：

- 定义 `TaskQueue`、`TaskHandler`、`RetryPolicy` 接口，保留 PostgreSQL 实现作为基线。
- 引入 Redis-backed queue，补最大重试、指数退避、死信、超时、并发上限和 stale claim 恢复。
- 为 HTTP、队列和 Agent Run 记录耗时、状态、错误类型和关联 ID。
- 增加基础 metrics、pprof 和可重复压测命令。

验收：能够并发提交任务，证明同一任务不会被重复执行；能够模拟 Worker 崩溃、模型超时和重复提交，并展示恢复结果。

### 切片四：性能与评测

本切片进入第三阶段后实施：

- 重点观测上传接口、检索接口、问答接口、队列吞吐和模型调用耗时。
- 以一个具体问题为目标，例如减少重复 Embedding、限制模型并发或降低检索 P95。

验收：至少有一份简短性能报告，包含基线、改动、结果和未解决瓶颈；报告中的数据能够复现。

### 切片六：长任务 Runtime 与事件恢复（DeerFlow 主线）

- 将 Agent Run、Worker、租约、心跳、取消和最终结果分开建模。
- 用短期 Redis Stream/Hub 传输 token、思考和工具进度；用 PostgreSQL 保存状态、最终回答和必要摘要。
- 增加事件游标、重放窗口、`gap` 恢复和最终答案兜底；不把每个 token 当作永久会话消息。
- 研究 checkpoint 与“从头重试”的边界：只有工具幂等和步骤状态持久化后，才允许真正断点续跑。

验收：断开 SSE 后能在保留窗口内继续；事件过期后能恢复最终答案；Worker 崩溃不会让任务永久卡住；能说明重放、重试和 checkpoint 的区别。

### 切片七：WeKnora 风格 RAG 质量闭环（WeKnora 主线）

- 继续完善文档结构、父子 chunk、混合召回、过滤、Rerank 和引用链路。
- 增加小规模 RAG 评测集，区分召回失败、引用错配、上下文超长和回答生成失败。
- 优先做用户可感知的文档选择、原文定位、总结和引用解释，不先扩展更多前端装饰。

验收：同一问题能够展示检索候选、最终引用和降级原因；文档范围过滤不越权；至少有可复现的召回/引用质量基线。

### 切片五：Multi-Agent、MCP 与最小 A2A

先做进程内协作，不先做跨机器复杂编排：

- `Supervisor`：拆分任务、选择角色、汇总结果。
- `Researcher`：调用知识库和文档工具，返回带来源的研究结果。
- `Answerer`：基于研究结果生成最终回答。

然后按顺序补协议：

1. MCP：实现一个只读 MCP Server，暴露知识库搜索和文档查询；再实现最小 Client 适配。
2. A2A：实现 Agent Card、任务创建、状态查询和结果返回，先支持 HTTP JSON，不追求完整规范覆盖。
3. 用统一的任务状态和事件模型连接单 Agent、Multi-Agent、MCP、A2A。

验收：至少两个 Agent 能通过结构化消息完成一次协作；能讲清 Function Calling 是模型调用机制、MCP 是工具/上下文接入协议、A2A 是 Agent 间通信协议。

## 面试交付物

- 一张总体架构图和一张 Agent Run 时序图。
- 一份队列故障恢复与幂等设计说明。
- 一份压测/性能优化报告。
- 一份工具权限、参数校验和资源限制设计说明；SQL 类工具后置，不作为本阶段验收内容。
- 一组 Agent 评测数据：工具成功率、平均步骤数、失败类型、P95 延迟和 Token 成本。
- 三段项目故事：任务队列可靠性、Agent 工具调用、性能或稳定性优化。

## 完成标准

完成本阶段不等于“把 WeKnora 的所有功能搬过来”，而是满足：

- 关键模块能够脱离源码独立讲解和重写。
- Go 代码保持职责分离和初学者可读：HTTP、业务、存储、Worker/定时调度有清晰边界；不同执行频率的后台工作独立调度。
- 每个功能都能用最小 Go 示例说明核心语法、调用顺序、上下文取消和退出条件。
- 关键失败路径有测试，核心链路有压测或可观测数据。
- 代码、架构图和 README 能让面试官在几分钟内看懂项目价值。
- 明确列出尚未实现的 A2A 完整规范、复杂沙箱和生产级多租户能力，不夸大项目能力。

## 进度校准（2026-08-11）

- 切片一和切片二已完成：Agent Runtime、Function Calling、ReAct、SSE、上下文摘要、工具结果边界、权限和会话能力均已落地。
- 队列可靠性切片中的日志、metrics、PostgreSQL 重试/退避/死信已提前完成，但 Redis-backed queue、stale claim 恢复、pprof 和压测仍属于后续阶段。
- 已开始切片五的最小进程内协作：当前只有 Supervisor → Researcher → Answerer，Researcher 仅使用知识库只读工具；MCP/A2A 暂不展开。
- 2026-08-11：Researcher 已升级为受最大步数限制的模型驱动多轮检索；增加无结果后继续、重复查询检测和引用去重，仍只允许当前知识库的只读工具。MCP/A2A 和完整 Deep Research 继续后置。
- 2026-08-11：Multi-Agent 已增加 `AnswerWithEvents` 和 SSE 路由，能够展示 Researcher 多轮工具调用与 Answerer 生命周期；前端已完成模式切换、研究轨迹和引用展示。
- 2026-08-11：最小只读 MCP Server/Client 已完成，提供知识库范围内的 `server/discover`、`tools/list` 和 `tools/call`；复用现有 `knowledge_search`，并新增 `document_list` 文档查询工具，不引入 SQL/schema。MCP 路由会校验知识库属于当前管理员，工具错误对外脱敏。
- 2026-08-12：最小 A2A HTTP 适配已完成，提供 Agent Card、任务创建、状态查询和结果返回；任务在进程内异步执行并复用 Multi-Agent Supervisor，不引入 Redis、SQL/schema 或完整分布式协议。
- 2026-08-12：A2A 任务已接入结构化日志和基础 metrics，记录提交、开始、成功、失败及终态耗时；失败只记录通用错误类别。
- 2026-08-12：已完成 A2A 任务持久化设计，推荐 PostgreSQL；先抽象 `TaskStore` 并迁移内存实现，再单独增加 migration、PostgresStore 和 Runner。暂不扩展完整协议、认证和跨机器调度。
- 2026-08-12：已实现 `TaskStore`、内存 Store、PostgreSQL Store 和 A2A Runner；主服务使用 PostgreSQL 持久化 A2A 任务，Runner 通过租约领取 `submitted` 或过期 `working` 任务。
- 2026-08-12：已补 PostgreSQL A2A Store 的真实集成测试代码，覆盖权限、领取租约、过期恢复、并发领取、结果保存和终态清理；待 Docker 启动后以 `TEST_DATABASE_URL` 执行。自动重试、Redis 或完整协议扩展继续后置。
- 2026-08-12：已增加默认关闭的 Go pprof 独立诊断端口，支持本机性能排查；补充 `BenchmarkPprofIndex` 作为可重复基线，完整业务压测和 profiling 报告继续后置。
- 2026-08-12：已补充 `BenchmarkKnowledgeBaseSearchHandler`，测量本地 stub 下的搜索 HTTP/JSON 边界；基线约 2.9µs/op、7.7KB/op、33 allocs/op，不代表真实 embedding、pgvector 或模型耗时。
- 用户明确跳过本轮真实评测，后续评测以小规模可控用例为主，不作为当前功能开发阻塞项。
- 2026-08-12：前端新增 A2A 异步任务模式：提交 `/api/a2a/tasks` 后轮询任务状态，完成时读取结果并复用现有引用展示；A2A 模式与普通会话隔离。
- 后续按学习优先推进：优先完善 Agent/RAG 的可见功能切片（检索参数、引用、运行轨迹、会话体验），每轮保留必要的编译/构建/冒烟检查，详细评测和完整测试集后置。
- 2026-08-12：标准 Agent 前端增加运行轨迹卡片，复用 `run_started`、`tool_called`、`tool_finished`、`message_delta`、`run_finished`、`run_failed` 和 `run_canceled` 事件；研究模式和 A2A 模式保持原有展示语义。
- 2026-08-12：增加 RAG 检索范围控制，标准 Agent 请求新增 `top_k`，协作研究继续使用 `topK`，A2A 使用 `top_k`；后端在 Handler、Service 和 scoped knowledge_search tool 三层校验/限制 1—20 的召回数量。
- 2026-08-12：标准 Agent 增加 `similarity_threshold`，按 pgvector distance 过滤证据，默认上限为 `0.65`；前端可视化调整该阈值，协作研究与 A2A 暂不扩展。
- 2026-08-12：前端引用卡片支持点击打开原文详情抽屉，展示来源文件、段落、distance 和完整文本，并保留提示注入安全说明；后端接口不变。
- 2026-08-12：前端引用体验补充复制回答和复制原文，复制失败时显示权限提示；后续学习重点回到 RAG/Agent 后端链路。
- 2026-08-12：将 pgvector distance 过滤抽到 `retrieval` 层；普通 RAG 与 Agent 使用同一套 `distance <= threshold` 语义，普通 `/chat` 和 `/chat/stream` 支持可选 `similarity_threshold`，保留旧 Answer/Stream 接口兼容。
- 2026-08-12：增加最小 hybrid retrieval：关键词命中与 pgvector 命中在 retrieval 层交错合并、按 `document_id:position` 去重，再交给现有 RAG/Agent 阈值过滤；关键词查询暂用 PostgreSQL `strpos(lower(content), lower(query))`，后续再评估全文索引和 rerank。
- 2026-08-12：混合检索结果增加 `matchType` 元数据，区分 `vector`、`keyword` 和 `hybrid`；引用卡片展示命中类型，保留现有 distance 字段兼容性。
- 2026-08-12：混合检索增加可选 Rerank：召回阶段扩大候选集，`RerankService` 调用 qwen3-rerank 返回新的相关性分数和顺序；关键词 SQL 使用 `ts_rank_cd` + 精确短语兜底，不新增业务表以外的结构。
- 2026-08-12：关键词检索迁移到物化 `tsvector` + GIN 倒排索引，并增加 `pg_trgm` GIN 索引；向量 HNSW 索引暂缓，等 embedding 模型维度固定后再用独立迁移创建，避免空表或换模型时产生错误索引。
- 2026-08-13：增加文档级检索范围。普通 RAG、标准 Agent 和搜索接口支持可选 `document_ids`，向量检索、关键词检索与 Agent 工具共享同一文档过滤边界；不传时保持全知识库检索。
- 2026-08-13：多查询改写增加失败降级和有界并行。改写器不可用、失败或返回空变体时只检索原问题；有变体时最多并行三路检索，统一合并去重后复用 Rerank。Agent Run、RAG 响应和前端只展示启用/应用/降级/变体数量，不暴露改写文本。
- 2026-08-13：标准 Agent 前端增加失败回答的“重新生成”入口。仅复用原有 SSE 接口重跑当前问题和会话，不新增数据库结构；成功回答、研究模式和会话保存失败状态不显示重试，避免重复写入。
- 2026-08-13：范围确认：不实现“成功回答后的编辑问题/覆盖式重新生成”功能。WeKnora 当前聊天主链路没有对应的通用消息编辑/回答重生成接口；后续不把它作为模仿目标，优先继续 Agent/RAG 已有能力。
- 2026-08-13：标准 Agent SSE 增加进程内断线恢复：`agentstream.Hub` 保存最近运行事件，新增 `agent-runs/{runID}/stream` 重放入口，前端保存 `run_id` 并按事件 ID 去重后自动重连；服务重启、多实例共享和持久化事件队列后置。
- 2026-08-13：问答台增加最小 Markdown 回答渲染；完成消息才执行 Markdown→HTML 和 DOMPurify 清理，流式消息保留纯文本，先覆盖常见排版，不引入公式/Mermaid 等复杂渲染。
- 2026-08-13：增加父子 chunk 检索上下文：文档处理先切父块再切子块，仅对子块生成 embedding；子块通过 `parent_chunk_id` 关联父块，命中后 RAG/Agent 使用父块上下文但引用保留子块位置；旧数据没有父块时继续走邻近 chunk 扩展。HNSW 暂缓到 embedding 维度和数据量稳定后。
- 2026-08-13：父块上下文查询改为批量加载：检索结果先收集所有子块引用，再由 PostgreSQL 单条 SQL 查询父块并映射回结果，减少父子检索的 N+1 查询；无父块的旧 chunk 继续邻近扩展。
- 2026-08-14：模型上下文增加父块去重：同一父块只注入一次，后续命中子块仍保留为独立引用；RAG 和 Agent 工具结果共用 32KB 上限，避免重复父块浪费上下文。
- 2026-08-14：embedding 维度确认稳定为 1024，新增 pgvector HNSW cosine 索引；同时将 `document_chunks.embedding` 明确为 `vector(1024)`，避免无维度列无法建立近似索引。小数据量下查询仍可能选择顺序扫描，数据增长后索引收益更明显。
- 2026-08-14：补齐文档删除闭环：按当前管理员和知识库归属校验后删除文档，数据库级联清理任务/父子 chunk，清理检索缓存和本地源文件；处理中任务返回冲突，前端增加确认删除与列表刷新。
- 2026-08-14：补齐知识库删除闭环：删除前校验当前管理员归属并锁定文档处理任务，处理中任务返回冲突；数据库级联清理文档、任务、父子 chunk 和会话，提交后清理本地源文件并使检索缓存失效，前端增加确认删除和状态反馈。
- 2026-08-14：前端补充可展开的检索流水线详情，展示向量/关键词召回、关键词阈值过滤、去重、Rerank、距离过滤和降级状态；复用既有统计字段，不增加模型调用或数据库结构。
- 2026-08-14：将 Agent 回答的检索统计和查询改写状态保存到 `conversation_messages.metadata` JSONB；历史消息读取时恢复到前端，旧消息继续兼容空 metadata。
- 2026-08-14：会话历史继续增强：Agent 从既有 `tool_finished` 事件收集去重引用，保存有界引用快照和显示安全的 Agent 步骤；前端重新打开会话时恢复引用卡片与运行轨迹。完整原文懒加载和模型 tool-call 历史重建继续后置。
- 2026-08-14：引用详情增加懒加载：后端新增知识库范围内的 chunk 读取接口，前端点击历史或实时引用时再读取完整原文，避免把长文本重复写入会话 metadata。
- 2026-08-14：Agent 轨迹增加有界工具明细：`tool_called` 保存参数预览，`tool_finished` 保存结果摘要；会话 metadata 只保留最多 32 条、每段最多 512 字节，原始工具内容仍通过引用单独查看。
- 2026-08-14：前端工具轨迹默认折叠，点击箭头查看现有参数预览和结果摘要；展开状态只存在于当前页面，后端协议保持不变。
- 2026-08-14：标准 Agent 增加知识库范围内的 `document_list` 只读工具，复用 `document.Reader` 返回文档元数据；与 `knowledge_search` 共用 ToolRegistry、Agent 循环和 SSE 事件链路。
- 2026-08-14：标准 Agent 增加 `document_info` 只读工具，支持“先列目录、再查询指定文档状态”的多工具调用链；仍只返回元数据，不读取全文。
- 2026-08-14：标准 Agent 工具失败改为安全对外提示；保留后端原始错误用于诊断，公开 SSE 事件按失败类别返回固定消息，不让模型在无证据时继续回答。
- 2026-08-14：前端统一显示 Agent 工具中文名称，协议仍保留稳定英文工具名；实时 SSE 与历史轨迹使用同一映射，兼容缺少工具名的旧记录。
- 2026-08-14：Agent 工具轨迹保存有界 `source_keys`，Handler 按回答的全局引用集合过滤后持久化；前端实时和历史轨迹都能定位到本次工具产生的引用。
- 2026-08-15：在既有 Agent 轨迹旁保存轻量运行摘要 `agent_trace.stats`，包含步骤、模型/工具调用、失败数、Token 和耗时；前端实时 `run_finished` 与历史会话共用同一展示格式。
- 2026-08-15：增加受知识库范围约束的 `document_read` 工具和文档正文预览接口；按 chunk 起始位置、数量和字节上限读取，不接受文件路径，也不把整篇文档一次性放入模型上下文。
- 2026-08-15：正文读取结果补入统一 `sources`/引用事件链路，工具轨迹、会话历史和前端来源卡片保持一致；`document_read` 使用独立命中类型，不伪造检索分数。
