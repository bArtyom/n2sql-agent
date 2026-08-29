# Agent 开发原则

## 学习笔记记录规则

- 当用户说“记下来”“记录一下”或表达同类意思时，默认把本次提问、回答和核心知识点记录到个人学习笔记中，优先写入 `docs/conversations/` 下对应主题的笔记文件。
- `agent.md` 只记录项目开发规则、架构约束和 Agent 执行原则；除非用户明确指定 `agent.md`，不要把普通学习内容写入这里。
- 如果当前没有合适的主题笔记文件，再创建或选择一个清晰命名的学习记录文件，并在记录中保留日期、问题和简明答案。

## 工具执行安全边界

- 普通工具，尤其是只读的 RAG、文档查询和检索工具，可以在明确失败后反馈给模型并允许再次尝试。
- 有副作用的工具，例如写文件、修改数据库、创建订单、发送消息或调用会改变外部状态的 API，必须实现 `RequiresApproval() bool { return true }`，执行前进入用户审批流程。
- 有副作用的工具默认不可自动重试。只有工具已经通过业务幂等键、外部状态查询或其他方式证明重复执行安全时，才额外实现 `Retryable() bool { return true }`。
- 工具结果为空，或执行错误但无法判断外部操作是否已经完成时，按“不确定状态”处理；有副作用工具直接结束本轮并要求确认，不要盲目再次执行。
- checkpoint 只负责记录 Agent 从哪一步继续，不能撤销已经发生的外部副作用；断点续跑必须同时遵守工具的审批和幂等边界。

## 新工具接入清单

1. 判断工具是否只读、是否会改变外部状态。
2. 只读工具保持默认可重试；副作用工具实现 `RequiresApproval()`。
3. 如果副作用工具具备可靠幂等保障，再实现 `Retryable()`，并说明幂等键和外部状态依据。
4. 为审批前不执行、失败不自动重试、显式幂等后允许重试分别补测试。

## Worker 并发通信要点

- `context.Context` 负责控制执行生命周期：父 Agent、子 Agent、模型调用和工具调用共享或派生 context；用户取消、超时或租约续期失败时，通过 `cancel()` 让执行链路停止。
- `channel` 负责 goroutine 之间传递结果和事件：例如 Agent 通过 `agentDone <- err` 报告执行结果，事件通过 channel 交给 SSE 或事件 Hub 转发。
- Worker 通常由执行 goroutine 和心跳 goroutine 协作：执行 goroutine 处理模型/工具，心跳 goroutine 定期续租；心跳失败时取消执行 context，执行结束后再取消并等待心跳 goroutine 退出。
- `context` 解决“是否停止”，`channel` 解决“传递什么”；二者只负责进程内协作，任务最终状态仍必须写入 PostgreSQL，才能支持 Worker 崩溃后的接管和恢复。

## Git 远程推送规则

- 不使用定时任务自动推送。
- 每完成 3 次本地 commit 后，推送一次当前分支到 `origin`。
- 推送前只执行 `git push origin HEAD`，不自动 `add`、`commit`、`reset` 或处理用户未提交的改动。
- 用户明确要求立即推送时，可以提前执行一次推送；未提交的 `README.md`、`agent.md` 和学习记录不自动纳入推送。

## Agent context 树与任务树

- Go 的 `context.Context` 只负责控制正在执行的代码。异步父 Agent 和子 Agent 通常由不同 Worker 领取，因此各自拥有独立的派生 context；取消父 context 不会自动取消已经独立运行的子 Agent context。
- 数据库通过 `agent_runs.parent_run_id` 保存父子任务树，用于找出尚未领取、正在执行或等待恢复的后代任务；它不是 Go context，也不会自行触发代码停止。
- 停止父 Agent 时，先在 PostgreSQL 事务中递归把父任务及未完成后代标记为 `canceled`，再通过 Hub 找到各运行任务注册的 `cancel` 函数，取消当前进程内正在执行的父、子 Agent。两层都要做，才能同时覆盖排队任务、运行中任务和跨 Worker 接管的任务。
- `context` 解决“是否停止”，数据库任务树解决“需要停止哪些任务”，Hub 解决“当前进程里如何触发对应的 cancel”；请求本身的 `r.Context()` 只服务于停止接口的数据库操作，不是 Agent 执行 context。

## Agent Worker 池与多用户并发（2026-08-26）

- HTTP Server 的请求 goroutine 负责接收不同用户的提问；提交接口只创建独立的 `agent_runs` 记录并返回 `202 + run_id`，不在 HTTP 请求里执行模型和工具。
- `internal/agentrun.Runner.RunWorkers` 启动共享 Agent Worker 池。每个 Worker 使用同一个 PostgreSQL Store，调用 `ClaimNext` 的 `FOR UPDATE SKIP LOCKED` 领取不同的 pending Run，因此 A、B、C 三个用户的任务可以在同一进程中真正并行执行。
- 每个正在执行的 Run 仍有独立的执行 context、租约 token、心跳 goroutine、SSE 事件流和 checkpoint；并发池只负责增加消费槽位，不改变单个 Run 的重试、续租、断点续跑和父子任务状态机。
- 配置项为 `AGENT_WORKER_CONCURRENCY`，默认 1 是保守值；根据模型 API 限流、PostgreSQL 连接池、CPU 和内存评估后可以设置为 3～5。多服务进程可以使用相同配置，数据库租约和领取锁负责跨进程分配。
- “多用户并发”不等于“完整多租户 RBAC”。当前 app_users/session 已存在，但知识库仍按本地 administrator 范围管理；后续要做账号级知识库隔离时，应单独增加资源 owner、权限校验和所有查询的作用域，不要把它混入 Worker 并发改造。

## Goroutine 通信与 Agent Hub（2026-08-26）

- goroutine 可以通信，但不需要统一注册到 Hub。`channel` 适合进程内两个 goroutine 直接传递结果，例如执行 goroutine 通过 `done <- err` 通知协调 goroutine。
- `context.Context` 负责取消和生命周期，不负责传递业务结果；Worker、Agent、模型调用和工具调用通过派生 context 接收停止信号。
- Agent Hub 是面向运行事件的广播组件，主要给 SSE Handler 发送 `tool_called`、`tool_finished`、`message_delta` 等前端可见事件；它不是所有 goroutine 的通用消息总线。
- Agent Worker 发布事件时通常走 `EventSink -> EventStore/Redis Stream -> Hub -> SSE`。Hub 解决当前进程内实时广播，Redis Stream 解决跨进程和短期回放，PostgreSQL 保存最终答案、Run 状态和恢复所需数据。
- 因此，执行 goroutine 与心跳 goroutine 之间不必通过 Hub 通信：心跳失败通过 `cancel(ErrLeaseLost)` 取消执行 context；执行结果通过 Worker 返回值更新数据库；只有需要展示给浏览器的事件才发布到 Hub。
- 两个平级 goroutine 通信时，通常由创建方直接把同一个 channel 传给生产者和消费者；不是把两个 channel 注册到 Hub。只有需要一对多广播时，才使用 Hub，Hub 内部为每个订阅者维护 channel，并把同一个事件复制分发给多个订阅者。
- 一个用户请求不会启动一个 Go 进程：一个服务进程可以接收很多用户请求，Go HTTP Server 通常为每个请求创建 goroutine；每个请求创建独立的 Agent Run，Worker 池再从数据库领取 Run。Hub 当前按 `run_id + knowledge_base_id` 隔离事件流，不等于完整的账号权限隔离，后者需要额外的用户 owner/RBAC 作用域。

## 用户级知识库隔离与 RBAC 设计（2026-08-26）

- 用户级隔离不应依赖前端传来的 `user_id`，也不应只在某个 Handler 做一次判断；请求必须先由 Session Middleware 注入当前用户，随后每个资源 Store 查询都通过用户与知识库成员关系做 SQL 作用域校验。
- 推荐新增 `knowledge_base_members(knowledge_base_id, user_id, role)`，用 `owner/editor/viewer` 表达权限；知识库、文档、Chunk、会话、Agent Run、评测和后处理任务都通过 `knowledge_base_id` 继承范围。私人会话和 Agent Run 还应保存 `owner_user_id/requested_by_user_id`，防止同一知识库成员之间互相读取私人会话或运行轨迹。
- 权限分层：viewer 可查看、检索和聊天；editor 可上传、重处理和修改文档；owner 可删除知识库、修改成员和知识库配置。所有用户输入的资源 ID 都必须在数据库查询中与成员关系 JOIN，未登录返回 401，已登录但无权限返回 403 或统一 404。
- Agent Worker 不从浏览器上下文读取用户身份，而是消费数据库中已保存的 `requested_by_user_id` 和知识库范围；子 Agent 继承父 Run 的用户和知识库范围。SSE/状态/停止接口在返回或操作 Run 前仍要重新做用户授权检查。
- Hub 只按 `run_id + knowledge_base_id` 做进程内事件隔离，不承担用户权限；Redis、PostgreSQL、检索和工具层也必须继续遵守同一作用域。现有 `administrator_id` 的单管理员模型需要经过明确的数据迁移和初始用户绑定后，才能切换为真实的成员模型。

实现状态（2026-08-26）：已新增 `knowledge_base_members` 迁移、`owner/editor/viewer` 授权 Store、应用级认证/授权中间件、知识库成员管理接口，以及创建/列表/更新/删除知识库的用户作用域。知识库下的文档、检索、Agent、SSE 等路由统一先经过成员权限门禁；旧知识库由迁移或首个注册用户完成初始绑定。会话和 Agent Run 当前按知识库成员共享访问，若后续需要私人会话，再增加 `owner_user_id/requested_by_user_id` 作用域。

## RAG 结构路径与 Embedding 学习记录（2026-08-21）

- WeKnora 的做法是把文档标题、真实 Markdown heading breadcrumb 和正文临时拼成 Embedding 输入，但不修改数据库里的正文；路径通过类似 `ContextHeader` 的内存字段传递。
- 本项目增加 `HeadingPathKind`：semantic 路径进入 Embedding，virtual 路径（如“文档.md > 第 1 段”）只负责展示和定位，不进入 Embedding，避免无语义编号污染向量。
- 关键词检索也不把路径拼入 `content_search`。真实路径使用独立的 `heading_search` GIN/tsvector 字段，以较低权重参与关键词排序；正文仍由 `content_search` 主导。
- 新切分或 Embedding 策略上线后，旧向量不会自动改变，因此需要通过重新处理接口重新读取原始文件、切分并生成向量。重新处理只新增一个后台文档任务，不重复保存原始文件；同一文档已有 pending/processing 任务时由数据库唯一约束和后端状态检查拒绝重复排队。
- 检索调优不能只看最终是否命中，还要区分命中来源。`headingScore` 单独记录标题路径的关键词命中分数，和正文 `keywordScore`、向量距离、RRF `fusionScore` 分开，便于定位“正文没出现但章节标题命中”的召回原因。
- RAG 评测除了拒答率、文档召回率和 MRR，还要记录命中来源：首个相关结果的 `match_type`、`heading_score` 和每个问题的 `heading_path_hits`。这样可以判断结构化路径是否真的提升了召回，而不是只看最终答案。
- 评测指标笔记：Recall 衡量相关问题找回比例；拒答率衡量无资料问题是否正确拒绝；文档命中率衡量正确文档是否进入结果；首次命中排名越小越好；MRR 是首次命中排名倒数的平均值。`hybrid` 表示向量检索和关键词检索共同命中/融合，不是一个模型。

## RAG 文档切分学习记录（2026-08-21）

- 旧文档处理的清理原则：不删除用户上传的原始文件和文档记录，只删除/替换旧的 chunk 索引；迁移 `000044` 会把旧版本混入正文的“结构路径”提取到 `heading_path`，之后统一使用新的结构化切分。
- 新切分先识别 Markdown 标题和启发式章节，无结构时才回退递归切分。代码围栏、Markdown 表格、LaTeX 块属于特殊内容保护区：普通递归切分不能进入保护区；只有保护块本身超过 Chunk 大小上限时才允许降级切分。
- 切分诊断不是模型摘要，而是本地统计：使用的策略、结构数量、保护块数量、Chunk 数量、总字符数、最小/最大 Chunk，以及短 Chunk/超长 Chunk 数量。Worker 在替换 chunk 成功后保存 JSON 元数据，文档预览读取并展示，便于判断切分质量。

## RAG 检索解释学习记录（2026-08-21）

- 检索接口默认只返回最终结果和有限统计；调试时显式传 `debug=true`，后端才生成有界的 `explain` 数组。
- 每条解释只包含最终排名、文档/Chunk 定位、向量距离、关键词分数、标题路径分数、RRF 融合分数和命中原因，不暴露被过滤的候选正文，避免调试能力带来额外数据泄露和响应膨胀。
- `向量+关键词+标题命中` 表示该结果同时参与语义召回、关键词召回，并且标题路径也提供了证据；它是对现有 hybrid 检索结果的可解释层，不是新的模型调用。
- 严格知识库模式要区分“元数据”和“事实证据”：`document_list`、`document_info` 只能说明文档存在，不能解除拒答；`knowledge_search`、`document_read`、`document_summary` 返回有效内容后才算 grounded evidence。
- 预生成摘要采用“索引成功后异步生成”的方式：摘要先写入 `documents.summary`，再用摘要文本生成一个 `chunk_kind=summary` 的向量/关键词检索 Chunk。正文读取、邻居扩展和摘要输入只读 `chunk_kind=text`，防止摘要递归和正文污染；摘要失败不影响原始文档 RAG，后续可以通过摘要工具再次触发。
- 摘要 Chunk 与正文第 0 段可能拥有相同的 `document_id + position`，所以引用身份必须额外包含 `chunk_kind`：普通正文继续使用 `document_id:position`，摘要使用 `document_id:position:summary`。这个身份同时用于 Agent 工具事件去重、历史消息来源快照和前端来源合并，避免摘要覆盖正文引用。
- Hybrid 融合层也必须使用同一套来源身份；否则摘要和正文会在 RRF 合并阶段提前被合并，导致摘要虽然参与了召回，却无法作为独立结果返回。现在向量/关键词融合、来源收集、历史快照和前端合并统一按 Chunk 类型区分。
- 摘要结果进入模型上下文时显式加上 `[文档摘要]` 标签，前端来源卡片也显示“文档摘要”而不是“第 1 段”；这样模型和用户都能知道它是概览证据，不应误当作逐字原文。
- 预生成摘要上线后的存量文档通过启动回填补齐：只扫描当前用户可见、索引状态为 `succeeded` 且摘要状态不是 `processing/succeeded` 的文档，放入同一个异步摘要队列；回填不阻塞服务启动，摘要失败也不影响原有 RAG。
- 摘要生成状态和摘要检索索引状态分离：`summary_status=succeeded` 但 `summary_index_status=failed/none` 时，只重试已有摘要的 Embedding 和 `summary` Chunk 写入，不重新调用摘要模型；两个状态都成功后才跳过回填。
- 索引重试任务领取后不再重复执行“抢占”：入队前已将 `summary_index_status` 标记为 `processing`，Worker 直接读取缓存摘要并执行索引，避免把已领取任务误判为重复任务。
- 摘要检索可观测性：运行时新增 `summary_candidates`，统计摘要 Chunk 是否进入候选集合；评测结果新增 `summary_hits`，区分“摘要参与召回”和“正文参与召回”，用于验证预生成摘要是否真的提升检索效果。

## WeKnora 最新版对照记录（2026-08-22）

- 已从官方 `Tencent/WeKnora` GitHub 仓库重新拉取旁路副本 `C:\Project\agentProject\WeKnora-latest`，当前版本为 `0.7.2`；原有 `WeKnora` 目录保留不动。
- 最新版与本项目 RAG 主线最相关的新增方向：知识库文件夹树、分块编辑与版本历史、批量重新解析、文档自定义元数据、更多 Office/XMind/HTML 解析、BM25/稀疏检索优化、运行时队列与 Worker 治理、端到端评测。
- 对照结论：本项目已经覆盖 WeKnora Lite 的核心文档 RAG 闭环；下一阶段优先补“解析器接口 + Office 文档”“完整 RAG 评测持久化”“知识库级解析/检索配置”，文件夹、分块版本回滚、外部数据源、知识图谱和多租户暂时后置。

## RAG 评测对齐 WeKnora（2026-08-22）

- 当前 `retrievaleval` 原本只按文档 ID和向量距离阈值统计命中；这适合快速拒答/阈值扫描，但不能衡量段落排序质量，也没有生成答案评测。
- 第一阶段对齐 WeKnora：评测用例新增可选 `expected_chunk_ids`，兼容原有 `expected_document_ids`；新增段落级 `passage_recall`、`precision_at_3`、`precision_at_10`、`ndcg3`、`ndcg10`、`chunk_mrr`、`map`。
- 单题评测明细已补齐：每个阈值下的 `CaseResult` 现在直接返回上述段落指标，整批 `ThresholdResult` 再对所有已标注问题求平均；因此既能看总体趋势，也能定位具体问题的排序失败。
- WeKnora 生成指标层已开始对齐：评测用例支持 `reference_answer`，新增独立的 `GenerationMetrics` 计算器，输出 BLEU-1/2/4、ROUGE-1/2/L；计算器先独立测试，等完整 RAG 评测执行器拿到真实模型答案后再写入结果。
- 完整 RAG 评测执行器已接入：`EvaluateRAG` 对每道题调用 `rag.Answerer`，用检索来源计算 `RetrievalMetrics`，用实际答案和 `reference_answer` 计算 `GenerationMetrics`，最终按 WeKnora 的 `retrieval_metrics`/`generation_metrics` 分组汇总；它不经过 Agent Runtime，避免工具重试和子 Agent 干扰 RAG 指标。
- 段落 ID沿用稳定引用身份：正文为 `document_id:position`，摘要为 `document_id:position:summary`；评测不通过模糊正文匹配反推 passage ID，避免摘要/正文同位置冲突。
- WeKnora 的完整评测还包含标准答案、临时评测知识库、完整检索+生成、BLEU/ROUGE 和异步任务查询；本项目后续先持久化评测 Run/Case/Metric，再接生成评测和答案忠实度。
- 代码清理原则：用户已确认不需要旧版本兼容。后续功能替换时直接删除旧接口、旧字段、旧分支、旧测试和死代码；数据库迁移历史文件不删除，但运行时只保留新模型和新链路。

## RAG 评测持久化与 Worker（2026-08-22）

- WeKnora 的评测数据严格由 `queries.parquet`、`corpus.parquet`、`answers.parquet`、`qrels.parquet`、`qas.parquet` 五张表组成：问题文本、语料文本、标准答案、问题-段落关系、问题-答案关系分别独立保存。
- 本项目新增 `internal/evaluationdataset`，使用同语义 Parquet 结构，加载后先校验 ID 唯一性和所有关系是否悬空，再按 QID 稳定排序组装 QA Pair；不再用旧的“一个 JSON case 直接代表全部评测语义”。
- 本项目新增 `evaluation_runs` 和 `evaluation_case_results`：Run 保存数据集快照、配置、状态、进度、尝试次数和租约；Case 保存问题、标准答案、生成答案、检索 ID 和两组指标。单题结果使用唯一约束，Worker 重试不会重复推进进度。
- `evaluationrun.PostgresStore.ClaimNext` 使用 `FOR UPDATE SKIP LOCKED` 领取评测任务；租约过期由 `RequeueExpired` 改回 pending，终态更新必须校验 lease token，避免旧 Worker 覆盖新 Worker。
- `evaluationworker.Worker` 按题执行 RAG：每题调用 `EvaluateRAGCase`，立即持久化结果，再处理下一题；崩溃时已完成题目保留，接管时通过结果唯一约束实现幂等。当前还需要接入 HTTP 创建/查询接口、临时知识库索引和 QREL passage ID 到真实 chunk ID 的映射。
- 当前 Parquet 依赖使用 WeKnora 同源 `github.com/parquet-go/parquet-go`；本机 Go 工具链解析该版本要求 Go 1.24.9，因此 `go.mod` 的 Go 版本同步提高，后续若要保持 Go 1.22 需要改用兼容版本或独立解析适配层。
- 评测 API 已接入：`POST /api/knowledge-bases/{id}/evaluations` 创建 202 pending 任务，`GET /api/knowledge-bases/{id}/evaluations/{runID}` 查询进度和逐题结果。请求体保持五表数组语义，并要求 `passage_chunk_ids` 把 qrels 的外部 PID 映射到本项目稳定 Chunk ID。
- 服务启动时会启动独立 evaluation Worker；Worker 按题调用 RAG、逐题保存、租约过期可重领，恢复时先读取已保存 case 并跳过，避免重复调用模型。这样评测任务不占用 Agent 的步数和 SSE 长连接。

## RAG 结构化文档解析（2026-08-22）

- 原解析器只支持 TXT、Markdown 和 PDF；现在新增 DOCX 与 HTML 内容类型，并在上传存储层允许 `.docx`、`.html`。
- DOCX 解析使用标准库读取 ZIP 内的 `word/document.xml`，提取 Word 段落和 `Heading1`～`Heading6`，转换为 Markdown 标题后交给现有结构化切分器；不会把 XML 标签、样式或脚本内容送入 Embedding。
- HTML 解析保留 `h1`～`h6`、段落、列表项和引用块，转换成统一文本结构；`head`、`script`、`style` 会被过滤，避免网页脚本或样式污染知识库。
- 解析器对 DOCX 解压大小、HTML 输出大小和文档路径继续执行边界检查；损坏的 DOCX、缺少 `word/document.xml` 和空文档都会明确失败。
- 当前仍保留原始文件、文档表和后续切分/Embedding/摘要流程不变，因此新增格式只扩展“读取层”，不改变已有 Chunk ID 和引用协议。
- 表格解析保真：DOCX 的 `w:tbl` 和 HTML 的 `table/tr/th/td` 会在读取层转换成 Markdown 表格；首行作为表头，自动补齐缺失列，单元格中的 `|` 会转义。这样表格进入现有 AdaptiveSplitter 后会被识别为保护块，默认不拆散行列关系；只有整张表超过 Chunk 上限时才允许降级切分。
- 这一步不额外调用模型，也不把表格单独存成另一类 Chunk；表格仍然进入普通正文 Chunk，标题路径、Embedding 临时输入、关键词/向量检索和引用协议保持统一。
- 上传链路已同步扩展：`.html/.htm` 归一为 `text/html`，`.docx` 先校验 ZIP 文件头再归一为 OpenXML MIME；数据库新增迁移放开这两种 `content_type`。因此格式支持现在覆盖“上传入口、存储扩展名、数据库约束、读取解析”四层，而不是只有解析器单点支持。
- 切分诊断已细分保护块：除总数外，`tableBlockCount`、`codeBlockCount`、`formulaBlockCount` 分别统计 Markdown 表格、代码围栏和 LaTeX 公式，方便判断结构保护是否真正生效。
- 摘要引用读取已与正文隔离：普通引用仍通过 `chunks/{position}` 读取 `chunk_kind=text`；摘要引用通过 `chunks/{position}?kind=summary` 读取摘要 Chunk，并返回 `chunkKind`，避免摘要引用点击后被正文过滤条件误判为不存在。
- 切分质量门禁开始对齐 WeKnora：标题切分、启发式切分、递归切分按顺序尝试；当出现空结果、长文档单 Chunk、过多非尾部短 Chunk、全部远小于目标或超过目标两倍等情况时，记录 `strategyRejections` 和原因并自动降级。最终诊断增加 `qualityPassed` 与 `qualityIssues`；短文档的少量小章节保持放行，避免门禁过严。
- 文档 Worker 已对齐 WeKnora 的质量处理：父/子 AdaptiveSplitter 在所有策略都不理想但仍生成有效 Chunk 时只记录 warning，继续 Embedding 和 Chunk 写入；`qualityPassed=false` 与 `qualityIssues` 供预览、排障和评测使用。只有最终没有有效 Chunk、解析失败、Embedding 失败或数据库写入失败才终止处理。

## 评测全流程学习笔记（2026-08-22）

- 评测流程：HTTP 创建任务 → 保存 `evaluation_runs` → Worker 使用 `FOR UPDATE SKIP LOCKED` 领取 → 从数据集快照恢复 QA Case → 调用 RAG → 计算检索指标和生成指标 → 保存 `evaluation_case_results` → 更新完成进度 → 全部完成后改为 `succeeded`。
- `evaluation_runs` 保存任务级信息：数据集快照、配置、状态、总题数、已完成题数、尝试次数、租约和错误；`evaluation_case_results` 保存每道题的问题、标准答案、模型答案、检索 ID、检索指标和生成指标。
- Worker 不等所有题做完才保存，而是每题完成就写数据库。Worker 崩溃后，租约过期任务会回到 `pending`；新 Worker 读取已有 case 结果并跳过已完成题，避免重复调用模型。
- `passage_chunk_ids` 是评测数据集 PID 到本项目真实 Chunk 引用的映射。例如 WeKnora 数据集的 `pid=10` 映射为当前系统的 `document_id:position=3:5`，这样 qrels 才能和真实检索结果比较。
- `NDCG@3` 和 `NDCG@10` 衡量相关资料在检索结果中的排序质量：相关资料越靠前，分数越高。NDCG@3 只看前 3 条，更接近模型实际使用的上下文；NDCG@10 看前 10 条，用于观察更大候选范围的排序质量。
- 如果正确资料在第 1、2 名，NDCG 分数高；如果正确资料直到第 8、9 名才出现，即使最终召回了，NDCG 仍然会下降。它和 Recall 的区别是：Recall 关注“找没找到”，NDCG 关注“排得好不好”。
- 当前项目评测直接使用指定知识库，并通过 PID 映射到现有 Chunk；WeKnora 原生评测还会创建临时知识库并重新索引测试 corpus，这是后续可以继续增强的方向。

## 本轮学习记录：统一文档解析引擎

- 用户问题：为什么要统一 `ParserEngine + ParseResult`，以及如何按这个架构继续扩展图片、Office 和 OCR 解析？
- 核心设计：上传文件后由 `ParserRegistry` 选择解析引擎，所有引擎统一返回 `ParseResult`；其中 `Markdown` 进入结构化切分和 Embedding，`Images` 交给资源持久化，`Metadata` 用于引用、诊断和评测。
- 当前内置引擎：`simple` 处理 TXT/Markdown/HTML，`office` 处理 DOCX/PPTX/XLSX，`pdf` 处理普通和扫描/混合 PDF，`image_ocr` 处理 PNG/JPEG/WEBP。
- 默认选择按 MIME 自动完成；配置 `DOCUMENT_PARSER_ENGINE` 后可以显式指定引擎名称。指定的引擎不存在或不支持当前文件类型时直接返回错误，不静默换用其他引擎，方便排查配置问题。
- 这套边界的价值：后续增加 MinerU、PaddleOCR-VL、本地 OCR 或远程 OCR 时，只需实现 `ParserEngine` 并注册，不需要改动切分、Embedding、资源保存和引用展示主链路。
- 后续对照 WeKnora 又补了一层知识库级规则：`parser_engine_rules` 按文件类型选择引擎，Worker 领取任务时解析规则；规则未命中时才走 MIME 自动选择。还增加了统一 HTTP Parser Engine，远程服务返回 `markdown/images/metadata` 后直接进入现有索引链路，并用允许主机列表限制 SSRF 风险。
- 为避免用户只能手写引擎名，新增 `GET /api/parser-engines` 返回引擎目录、支持 MIME、可用状态和原因；解析设置页以后可以直接消费这个目录。
- 真实 provider 适配：MinerU 使用 `/file_parse` multipart 协议，PaddleOCR-VL 使用 `/layout-parsing` JSON + base64 文件协议；返回的 Markdown、页面图片和 metadata 都统一转换为 `ParseResult`，后续索引代码不感知供应商差异。
- WeKnora Cloud 使用独立的签名异步适配器：POST `/reader` 提交 base64 文件，按 task ID GET 轮询直到 completed/failed；签名所需 App ID/API Key 只从后端环境变量读取，不存 PostgreSQL，也不返回前端。

## 上传级解析配置快照（2026-08-23）

- 用户问题：为什么要按统一 `ParserEngine + ParseResult` 扩展解析能力，以及上传时如何只影响本次文档处理而不修改知识库全局配置？
- `documentextractor.ProcessConfig` 目前承载 `parser_engine_rules`，通过 multipart 的 `process_config` JSON 传入；Handler 使用严格 JSON 解码，未知字段和非法规则直接返回 400。
- 上传级配置先经过领域层校验，再进入文档服务；文档服务和 Postgres Store 会再次校验，避免绕过 HTTP 直接写入非法配置。
- `document_processing_tasks.process_config` 保存处理任务快照。上传未传覆盖时，任务保存当时知识库的 `parser_engine_rules`；传了覆盖时，只保存本次配置。已有排队任务由迁移脚本回填，保证 Worker 不依赖运行时知识库规则变化。
- Worker 领取任务后只读取任务快照，解析为 `ProcessConfig`，再通过 `ResolveParserEngine` 选择具体 `ParserEngine`；重试复用同一快照，重处理复制最近一次任务配置。
- 核心原则：知识库配置是默认值，上传配置是本批次覆盖，任务快照是执行时事实；解析器输出仍统一为 `ParseResult`，不会污染后续切分、Embedding、资源保存和引用链路。

## 上传级分块配置快照（2026-08-23）

- `process_config.chunking_config` 当前支持 `chunk_size`、`chunk_overlap`、`parent_chunk_size`、`child_chunk_size` 和 `strategy`。
- `chunk_size/chunk_overlap` 默认作用于用于向量检索的子 Chunk；父子大小可以单独覆盖。`strategy` 支持 `auto`、`heading`、`heuristic`、`recursive`，固定策略时不偷偷换成另一种策略，失败会进入质量诊断。
- Worker 不修改全局 Splitter，而是根据每个任务快照临时创建 `AdaptiveSplitter`；默认未配置时继续使用服务默认父块 3000/300、子块 1000/150。
- 这说明处理配置的执行边界应放在 Worker 任务，而不是 HTTP Handler 或全局单例：同一批任务可复现，重试不漂移，诊断中的 strategy 与实际执行一致。

## 解析器级开关快照（2026-08-23）

- `ProcessConfig` 新增 `parser_engine_overrides`，由任务快照传到统一 `ParseRequest.EngineOptions`，解析器不需要知道 HTTP 或数据库结构。
- 当前已实现 `pdf_force_scanned=true`：即使 PDF 能提取出少量文本，也可以按本次任务强制交给 OCR；结果 metadata 会记录 `parser_mode=ocr` 和 `ocr_forced=true`。
- Worker 通过可选的 `TaskParserExtractorWithOptions` 传递开关，旧的无配置任务仍走原有 `ExtractResultWithEngine`，默认行为不改变。
- WeKnora 对齐原则：解析器选择、解析器参数、分块参数都属于同一个处理任务快照；重试读取同一快照，不能在 Worker 执行中途重新读取全局配置。
- `POST /documents/{documentID}/reprocess` 现在接受 `{ "process_config": {...} }`；有配置时替换新任务快照，空 body 时复制最近一次快照，非法配置在 Handler、Service、Store 三层拒绝。

## 批量重解析任务（2026-08-23）

- WeKnora 风格的批量文档重解析不在 Handler 内循环调用单文档接口，而是由领域服务一次性校验并交给 Store 在同一个事务中批量入队。
- 新增 `POST /api/knowledge-bases/{id}/documents/reprocess`，请求为 `{ "document_ids": [1, 2], "process_config": {...} }`；返回 202、数量和 pending 状态。单文档 reprocess 接口仍保留，但两者最终都写入 `document_processing_tasks`。
- 服务层会拒绝空列表和非法 ID，排序并去重文档 ID；Store 会检查所有文档都属于当前知识库且当前管理员可见，任何一个不存在都整体失败，避免“部分成功”导致用户误判。
- 批量入队使用 PostgreSQL 事务：先检查选中文档和 active task，再用 active-document 唯一约束兜底竞态；每个文档插入自己的 `process_config` 快照。没有传新配置时复制该文档最近一次任务快照，不重新读取易变化的知识库全局配置。
- 这体现了处理链路的边界：HTTP 只负责解析请求，Service 负责业务校验，Store 负责事务和并发一致性，Worker 仍只消费任务快照，不需要知道这是单文档还是批量任务。
- `ParserEngineRule` 支持 `xlsx_first_row_as_header`，Worker 将文件类型规则转换成 `ParseRequest.EngineOptions`，Office Parser 真正输出 Markdown 表格；任务配置中的同名 override 优先于规则默认值。

## 文档目录范围检索学习记录（2026-08-23）

- WeKnora 的知识条目目录通过独立的 `folder_path` 元数据保存，不把目录名拼进正文或 Embedding；目录树由路径聚合生成，缺少正文的中间父目录也会自动补齐。
- 本项目新增 `documents.folder_path`，上传时保存目录路径；空字符串表示知识库根目录。路径统一为 `/` 分隔，并限制深度、单段长度和总长度，拒绝 `.`、`..` 和控制字符。
- 文档列表支持精确目录与递归子树筛选；目录树、批量移动、整棵目录重命名都有独立接口，并复用知识库归属校验。
- `retrieval.SearchOptions` 现在支持 `FolderPath` 和 `FolderRecursive`。向量召回、关键词召回、Hybrid 融合和结果缓存都会带上目录边界；显式根目录也不会和全库搜索共用缓存。
- Chat、流式 Chat、Agent 的 `knowledge_search` 和异步子 Agent 都继承请求中的目录范围；目录范围由后端固定，模型不能通过工具参数扩大搜索范围。

## 文档标签与范围检索（2026-08-23）

- WeKnora 风格的文档标签是独立元数据，不拼进正文、不进入 Embedding；它只负责给文档分类，并在召回阶段限制候选范围。
- 新增 `knowledge_base_tags` 标签定义表和 `document_tags` 多对多关联表。标签名在同一知识库内大小写不敏感唯一；删除标签会级联删除文档关联。
- 新增标签管理接口：`GET/POST /api/knowledge-bases/{id}/tags`、`PATCH/DELETE /api/knowledge-bases/{id}/tags/{tagID}`，以及 `GET/PUT /api/knowledge-bases/{id}/documents/{documentID}/tags`。PUT 是原子替换，传空数组表示清空。
- HTTP 边界会规范名称空白、颜色大小写，限制标签数量和字段长度；绑定 ID 会去重并排序，未知字段、非法颜色、空 PATCH 都直接返回 400。
- `retrieval.SearchOptions.TagIDs` 已贯通向量检索、关键词检索、Hybrid 融合、结果缓存、同步 Chat、流式 Chat、Agent 和异步子 Agent。SQL 在召回前用 `EXISTS` 过滤标签，避免先召回再内存过滤造成排序污染或越权。
- 标签范围进入缓存键，避免“同一个问题但不同标签范围”错误复用结果。Agent/子 Agent 继承后端固定的标签边界，模型不能通过工具参数扩大范围。
- 当前标签筛选语义是“命中任意一个选中标签”；如果未来需要“同时拥有全部标签”，应单独增加明确的 `match_all` 语义，不能隐式改变现有接口。
- 本次完成后通过 `go test -count=1 ./...`、`go vet ./...` 和 `git diff --check`；标签实现相关代码准备独立提交，已有未提交笔记和旧迁移清理不纳入该提交。
- 标签管理进一步闭环：上传接口支持逗号分隔的 `tag_ids`，文档创建事务先校验标签归属再写入 `document_tags`；如果标签非法，文档和处理任务一起回滚。
- 文档列表支持 `tag_ids`，并可与 `folder_path/folder_recursive` 组合；`TagReader`/`FolderTagReader` 在数据库层执行过滤，避免全量加载后内存筛选。
- 文档列表和 Agent 的 `document_list/document_info` 现在直接返回标签 ID、名称和颜色；标签展示数据和筛选数据来自同一知识库权限边界，避免前端再逐文档补查。

## Agent 尝试历史与父子执行树（2026-08-23）

- `agent_runs` 继续保存当前聚合状态；新增 `agent_run_attempts` 保存每次 Worker Claim 或同步子 Agent 执行的独立生命周期，包括 attempt 次数、状态、错误、停止原因和开始/结束时间。
- 领取任务、写入 attempt、完成任务、等待子 Agent、租约过期重排队现在在同一 PostgreSQL 事务中更新，避免出现“运行状态已变更但尝试历史没写上”的半完成状态。
- 租约过期后，当前 attempt 变为 `requeued`；达到最大重试次数时变为 `failed + orphan_recovered`。父 Agent 重新被领取时会产生下一条 attempt，而不是覆盖上一条失败记录。
- `GET /api/knowledge-bases/{id}/agent-runs/{runID}/attempts` 返回脱敏后的尝试历史；执行树接口也为父/子节点附带 `attempts`，可以查看某个子 Agent 是否经历过重试或 Worker 崩溃恢复。
- 新迁移会把已有 `agent_runs.attempt_count` 回填为历史 attempt：从未开始的 pending 不生成记录，已有次数的 pending 视为租约恢复后的 `requeued`，避免升级后旧任务历史为空。
- `CreateChild` 也使用同一尝试历史模型；同步子 Agent 的第一次执行和异步 Worker 领取的第一次执行都能被统一观察。`CreatePendingChild` 只创建待领取任务，真正执行时由 ClaimNext 创建 attempt。
- 这次切片的边界：attempt 历史只保存生命周期和错误摘要，不复制请求快照或大工具结果；请求、最终答案和 checkpoint 仍分别由 `agent_runs`、消息/响应和 checkpoint 存储负责。
## WeKnora 风格图片解析与检索（2026-08-23）

- 用户问题：WeKnora 如何处理图片，以及是否应该把图片 OCR/描述做成独立可检索内容；核心原则是原图和语义索引分离。
- WeKnora 风格链路：原图保存到文件/对象存储；解析器或 VLM 生成 OCR 文本和图片描述；两者作为 `image_ocr`、`image_caption` 子块进入向量/关键词检索；每个子块通过 `parent_chunk_id` 关联父块，并用图片元数据定位原图。
- 本项目新增 `ImageAsset.OCRText/Caption` 和 `ImageEnrichment`。`ParseResult.Images` 仍只携带当前任务的图片字节，`SaveParseResult` 负责把原图写入 `document_assets`，不把 Base64 或二进制写入 `document_chunks`。
- `document_chunks` 新增 `image_info JSONB`，`chunk_kind` 扩展为 `image_ocr`、`image_caption`；`ImageInfo` 保存 `assetIndex/filename/page/source`。图片子块使用已有父文档块关系，复用正文的权限和引用边界。
- Worker 的顺序是：解析 -> 结构化父子切分/Embedding -> 正文 Chunk 事务提交 -> 保存图片资源 -> 可选图片增强 -> 图片 OCR/Caption Embedding -> 图片子块事务写入。图片子块失败只记录 warning，不让正文索引失败。
- 图片 OCR/描述不一定要每次都调用模型：图片解析器已有 OCRText 时直接复用；只有字段缺失时才调用可选 `ImageEnricher`。`IMAGE_CAPTION_PROMPT` 为空时不发第二次视觉模型请求，避免无意义成本。
- 检索 SQL 已读取 `image_info`，图片子块会进入向量和 tsvector 关键词召回；引用接口支持 `kind=image_ocr/image_caption`，历史来源快照保留 `imageInfo`，前端可以用 `assetUrls` 展示原图。
- 安全边界：图片 MIME、大小和真实上传签名仍由上传/解析链路校验；视觉模型输出只当作不可信资料，不能变成系统指令；原图 URL 必须经过知识库归属校验。
- 验证：`go test ./...` 已通过。新增迁移 `000060_add_image_chunks`；未把既有工作区中的历史笔记、旧迁移删除和项目状态改动混入功能提交。

## 普通 PDF/DOCX 原生解析边界（2026-08-23）

- 第一阶段先固定普通文档路径：PDF 优先提取 PDF 内置文字，DOCX 解析 `document.xml` 中的段落、标题和表格；两者都不调用 OCR 或 VLM。
- PDF 之前会在原生文本提取前调用页级处理器；这会导致“明明是普通 PDF，却先渲染页面/触发 OCR”。现在改为先读原生文本，只有原生文本提取为空时才保留后续扫描 PDF fallback。
- 普通 PDF 的 `ParseResult.Metadata` 记录 `parser_mode=native_pdf`、`text_source=embedded_text`；普通 DOCX 记录 `parser_mode=native_docx`、`text_source=embedded_text`。
- DOCX 中的嵌入图片仍作为 `ImageAsset` 保存，但本阶段只保存资源，不做图片 OCR/Caption；图片多模态属于后续独立异步阶段。
- 这体现 WeKnora 的成本边界：先使用文件已有文本，只有文本层缺失或明确进入扫描/图片处理阶段时，才使用 OCR/VLM。

## WeKnora 风格 PDF 完整解析（2026-08-23）

- PDF 现在按页先读取原生文字，再根据页级文字量判断是否需要处理；默认低于 100 个 Unicode 字符的页面才进入扫描候选，富文本页不会调用 OCR/VLM。
- 逐页流程是：`pdfinfo` 统计页数 -> `pdftotext` 读取每页文字 -> 只渲染候选页 -> 优先调用 PaddleOCR-VL `/layout-parsing` -> 把 text/table 合并为 Markdown，把 figure 保存为独立 `ImageAsset`；版面服务失败时降级为整页 OCR。
- 原生 PDF 中的嵌入图片由可选的 Poppler `pdfimages` 提取；扫描页内部的图片必须由版面分析器返回区域后才能裁剪成独立图片。两条链路分开，避免把整页图片误当成内嵌图。
- `ParseResult.Metadata` 记录 `parser_mode`、`text_source`、`layout_mode`、`page_count`、`ocr_pages`、`figure_count` 和 `layout_failed_pages`，用于诊断和引用展示，不进入正文 Embedding。
- 服务端 wiring：`DOCUMENT_PARSER_PADDLEOCR_VL_URL` 自动提供页面版面分析；`OCR_MODEL` 提供页面 OCR 和图片增强；`PDF_IMAGE_BIN` 可指定 `pdfimages`，未安装时只跳过嵌入图片提取，不影响原生文字解析。
- Worker 不需要知道 PDF 的解析细节，只消费统一 `ParseResult`。版面 figure 进入已有的图片资源保存、OCR/Caption、`image_ocr/image_caption` 子 Chunk 和 Hybrid 检索链路；图片子 Chunk 失败不会回滚正文 Chunk。
- 核心成本原则：普通页不调用模型；扫描/低文本页才调用布局或 OCR；Caption 仍由 `IMAGE_CAPTION_PROMPT` 显式开启。

## 评测与存储运维观测（2026-08-24）

- 评测 Run 自动为数据集快照生成 `dataset_version`（SHA-256），并保存知识库快照、模型配置快照；同一题在不同 Chunk/模型配置下的结果不会混为一份。
- `evaluation_runs` 记录总耗时、Prompt/Completion/Total Token、估算成本和失败题数；`evaluation_case_results` 记录单题状态、实际尝试次数、耗时、Token、估算成本和错误摘要。
- 单题失败不再直接终止整个评测：默认每题最多尝试 2 次，可在评测 `config.max_case_attempts` 中配置，后端限制最多 8 次；最终失败题保存为 `status=failed`，其他题继续执行。
- 估算成本使用评测配置快照中的 `prompt_cost_per_1k_micros` / `completion_cost_per_1k_micros`；缺少费率时仍保存 Token 和耗时，成本为 0。
- 新增统一模型调用观测：Chat、Embedding、Rerank、OCR 都记录类型、供应商/模型、成功失败、错误类别、耗时和 Token；Prometheus 只保存低基数聚合，不记录提示词、正文、图片或密钥。
- 新增 `internal/blobstore` 存储边界：`Put/Open/Delete`，当前通过本地文件 Store 适配，读取支持流式打开；只有 OCR 等确实需要 `[]byte` 的路径才使用有上限的 `ReadLimited`，后续可替换 MinIO/COS/OSS。
- 现有图片 OCR/Caption 已经是独立任务队列和并发 Worker；本次没有重复实现，而是让它们复用统一模型调用观测和存储边界。

## 评测质量指标与严格拒答（2026-08-24）

- RAG 单题评测现在在原有 Recall、Precision、MRR、MAP、NDCG、BLEU、ROUGE 之外，补充 `faithfulness`、`answer_relevance`、`citation_recall`、`citation_precision`。
- 这四个指标当前是无模型调用的可复现基线：Faithfulness 按答案句子与召回资料的词元覆盖率计算；Answer Relevance 有参考答案时使用 ROUGE-L，没有参考答案时使用问题词元覆盖率；引用指标按期望 Chunk ID 与实际召回 Chunk ID 计算。
- 不能把词面启发式当作真正的 LLM Judge；后续如果接入评测模型，应增加独立 judge 模式和模型配置快照，不能改变当前免费基线。
- 严格知识库模式的评测现在区分 `expected_relevant`、`refused`、`correct_refusal`：知识库外问题正确拒答，知识库内问题拒答属于 `false_refusal`，知识库外问题被回答属于 `unsupported_accept`。
- `evaluation_case_results` 持久化单题拒答字段；`evaluation_runs` 累计相关题/无关题、正确拒答、错误拒答和无依据回答。`GET /evaluations/{runID}` 直接返回 `recall`、`refusal_rate`、`accuracy` 和分类计数。
- 没有参考答案的题仍会统计四个 RAG 质量基线；只有 BLEU/ROUGE 需要参考答案。拒答题不参与生成质量均值，避免把拒答文本当成正常答案质量。
- 迁移：`000066_add_evaluation_refusal_metrics`。本次只提交评测相关文件，保留工作区中其他文档解析和前端改动。
- Run 查询现在增加 `metrics` 汇总对象：从已经持久化的 Case 结果计算 Retrieval 平均值、BLEU/ROUGE 平均值、Faithfulness/Answer Relevance/Citation 平均值和各自样本数；不需要再次调用模型。
- 汇总规则与单批 `EvaluateRAG` 一致：检索指标只统计有相关 Chunk 标注的题，BLEU/ROUGE 只统计有参考答案的题，质量指标统计非拒答答案；失败题不进入分母。
- 聚合逻辑位于 `internal/retrievaleval/metric_summary.go`，Handler 只负责把数据库结果转换成样本，避免 HTTP 层重复维护指标公式。

## 评测入口设计（2026-08-24）

- 评测不只使用临时知识库，保留四类入口：已有知识库快速回归、固定知识库快照对比、临时知识库完整数据集评测、只测检索的离线评测。
- 已有知识库评测成本低，适合日常检查；知识库快照用于保证不同模型/检索配置之间可复现；临时知识库用于导入标准 corpus 并自动解析、切分、Embedding 和建立 PID 到 Chunk 的映射；离线检索评测不调用 Chat 模型，专门调 Recall/MRR/NDCG/MAP。
- 完整 RAG 评测在检索之后继续调用 Chat，统计 BLEU/ROUGE、Faithfulness、Answer Relevance、引用指标和严格拒答指标。
- 当前项目已支持指定已有知识库评测；`knowledge_base_snapshot` 目前主要是配置记录，真正锁定文档/Chunk 版本仍需后续实现。临时知识库和自动 PID → 多 Chunk 映射是下一阶段重点。
