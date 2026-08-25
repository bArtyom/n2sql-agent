# n2sql-agent

`n2sql-agent` 是一个本地运行的通用文档知识库问答平台。用户可以整理资料，并基于已处理的文档获得带引用来源的回答。

新 Agent 接手项目时，先阅读根目录的 [PROJECT_STATUS.md](PROJECT_STATUS.md)，再阅读 [AGENTS.md](AGENTS.md)。详细计划和学习/对话记录在本地 `docs/` 目录中；该目录按约定不纳入 Git。

当前阶段建设稳定、可扩展的工程底座与文档知识库最小闭环。

## 本地开发

准备环境：Go 1.22+、Node.js 20+、Docker Compose。

### Windows PowerShell 启动流程

在项目根目录 `C:\Project\agentProject\n2sql-agent` 执行：

```powershell
cd C:\Project\agentProject\n2sql-agent

# 首次启动前准备配置文件
if (!(Test-Path .env)) { Copy-Item .env.example .env }

# 启动 PostgreSQL
docker compose up -d
docker compose ps
```

确认 PostgreSQL 显示 `Up ... (healthy)` 后，加载 `.env` 并启动 Go 后端：

```powershell
Get-Content .env | ForEach-Object {
  if ($_ -match '^\s*([^#=]+)=(.*)$') {
    Set-Item -Path "Env:$($matches[1].Trim())" -Value $matches[2].Trim()
  }
}

$env:Path="C:\Program Files\Go\bin;$env:Path"

# 首次初始化数据库，或迁移文件发生变化时执行
go run ./cmd/migrate

# 启动后端
go run ./cmd/server
```

后端启动后保持这个终端运行。另开一个 PowerShell 窗口启动前端：

```powershell
cd C:\Project\agentProject\n2sql-agent\frontend
$env:Path="C:\Users\lsp24\.cache\codex-runtimes\codex-primary-runtime\dependencies\node\bin;$env:Path"
npm install       # 依赖未安装时执行一次即可
npm run dev
```

启动完成后访问 `http://localhost:5173`，后端健康检查地址是 `http://localhost:8080/health`。

修改 `.env`（例如 API Key）后，只需要停止并重新启动 `go run ./cmd/server`；前端和数据库通常不需要重启。修改数据库迁移文件或首次初始化数据库时，先执行 `go run ./cmd/migrate`。停止后端/前端可在对应终端按 `Ctrl+C`，停止 PostgreSQL 使用 `docker compose down`。

### 本地日志查看（Grafana Loki）

后端默认继续把结构化 JSON 日志输出到终端，同时追加到 `.data/logs/server.jsonl`。启动后端后，可以启动本地日志栈：

```powershell
docker compose --profile observability up -d loki alloy grafana
docker compose --profile observability ps
```

打开 `http://localhost:3000`，使用 `admin` / `admin` 登录；如果修改了 `.env` 中的 `GRAFANA_ADMIN_PASSWORD`，使用修改后的密码。Loki 已经由 Compose 自动配置为 Grafana 的默认数据源。

在 Grafana 的 Explore 中选择 Loki，可以先使用：

```logql
{service="n2sql-agent", environment="local", component="backend"} | json
```

按一次请求查询：

```logql
{service="n2sql-agent", component="backend"} | json | trace_id="你的 trace id"
```

`service`、`environment` 和 `component` 是低基数标签；`trace_id`、`request_id`、`run_id` 保留在 JSON 正文中，不作为 Loki 标签，避免日志流数量失控。后端停止时可执行：

```powershell
docker compose --profile observability down
```

这个本地栈只负责日志；现有的 `/metrics` 仍由 Prometheus 兼容接口提供，后续接入 Tempo 时可以继续复用 `trace_id` 将日志和完整 Trace 关联起来。

### 可选独立 DocReader

文档解析默认仍可在本地进程运行。需要模拟 WeKnora 的“Go 主服务 → 独立 DocReader”边界时，在 `.env` 中配置：

```env
DOCUMENT_READER_GRPC_URL=127.0.0.1:50051
DOCREADER_ADDRESS=127.0.0.1:50051
DOCUMENT_READER_FALLBACK_LOCAL=true
```

然后开两个 Go 终端，先启动解析服务，再启动主服务：

```powershell
# 终端一：独立解析进程
go run ./cmd/docreader

# 终端二：HTTP 服务和文档 Worker
go run ./cmd/server
```

上传文档后，主服务只负责把文件内容和解析配置通过 gRPC 发给 DocReader；DocReader 分块返回 Markdown、图片资源和元数据，主 Worker 继续执行结构化切分、Embedding 和入库。`DOCUMENT_READER_FALLBACK_LOCAL=true` 时，只有 DocReader 不可用、超时或内部通信失败才回退本地解析；文档本身格式错误不会被静默掩盖。生产部署可以启动多个 DocReader 实例，并在 `DOCUMENT_READER_GRPC_URL` 前放置 gRPC 负载均衡地址。

模型服务设置面板填写过程中不会因点击外部遮罩而关闭；需要点击右上角关闭按钮或按 `Esc` 退出。

### 可选本地 Embedding（Ollama）

项目支持用本地 OpenAI-compatible Embedding 服务替代远程 Embedding。聊天模型仍使用模型服务设置页中的 Provider；只有文档切块和查询向量化会走本地服务。以 Ollama 的 `qwen3-embedding:0.6b` 为例：

```powershell
ollama pull qwen3-embedding:0.6b
```

在 `.env` 中配置：

```env
LOCAL_EMBEDDING_BASE_URL=http://127.0.0.1:11434/v1
LOCAL_EMBEDDING_MODEL=qwen3-embedding:0.6b
LOCAL_EMBEDDING_API_KEY=ollama

# 如果聊天走 DeepSeek，同时把本地地址加入允许主机名
MODEL_PROVIDER_ALLOWED_HOSTS=api.deepseek.com,127.0.0.1
```

留空这三个变量时，项目才会回退到模型服务设置页保存的远程 Embedding 配置。当前前端已不再展示嵌入模型输入框；如果使用本地模型，切换 Embedding 模型后，需要重新上传文档或重新建立索引，不同模型的向量不能混用。

### 可选 GraphRAG（Neo4j）

GraphRAG 是 Hybrid RAG 的可选召回侧路，不会替换现有的向量 + 关键词检索。开启后，文档 Chunk 入库成功会异步排队图谱抽取任务，由聊天模型抽取实体/关系并写入 Neo4j；用户提问时再抽取问题实体，做一跳关联 Chunk 召回，最后和向量、关键词候选一起去重、融合、统一 Rerank 和引用。

先启动 Neo4j：

```powershell
docker compose --profile neo4j up -d neo4j
```

然后在 `.env` 中填写：

```env
NEO4J_ENABLE=true
NEO4J_URI=bolt://localhost:7687
NEO4J_USERNAME=neo4j
NEO4J_PASSWORD=你启动 Neo4j 时设置的密码
NEO4J_DATABASE=neo4j
```

修改后重启后端。GraphRAG 默认关闭；关闭时不会调用图谱抽取模型，也不会影响原有 Hybrid RAG。当前图谱召回只支持知识库和可选文档 ID 范围，文件夹、标签和元数据过滤会安全跳过图谱侧路，避免越权召回。

```sh
cp .env.example .env
set -a
. ./.env
set +a
docker compose up -d
go run ./cmd/migrate
go run ./cmd/server
```

请在同一个终端中运行以上命令，以便后续命令使用 `.env` 中导出的配置。

后端健康检查：`http://localhost:8080/health`。

知识库管理接口：

```sh
curl -X POST http://localhost:8080/api/knowledge-bases \
  -H 'Content-Type: application/json' \
  -d '{"name":"Go 学习资料","description":"后端笔记"}'

curl http://localhost:8080/api/knowledge-bases

curl -X DELETE http://localhost:8080/api/knowledge-bases/1
```

删除知识库会先确认它属于当前管理员且没有正在处理的文档任务，再级联删除关联的文档、处理任务、父子 chunk 和会话记录；提交数据库删除后，服务会清理本地原始文件并使该知识库的检索缓存失效。处理中任务会返回 `409`，源文件清理失败只记录后端结构化日志，不会恢复已提交的数据库删除。

上传文件使用 `POST /api/knowledge-bases/{id}/documents`，提交名为 `file` 的 `multipart/form-data` 字段：

```sh
curl -X POST http://localhost:8080/api/knowledge-bases/1/documents \
  -F 'file=@./notes.txt;type=text/plain'
```

首版只接收 Markdown、TXT 与可提取文本的 PDF，单个文件不超过 10MB。源文件保存在 `UPLOAD_DIR`（默认 `./.data/uploads`），并在数据库中同时创建文档与 `pending` 处理任务。

上传前可以调用只读试切接口检查分块策略。它不会写入数据库，也不会调用 Embedding：

```sh
curl -X POST http://localhost:8080/api/knowledge-bases/1/chunk-preview \
  -H 'Content-Type: multipart/form-data' \
  -F 'file=@./notes.md;type=text/markdown' \
  -F 'process_config={"chunking_config":{"chunk_size":512,"chunk_overlap":80,"separators":["\\n\\n","\\n","。","！","？"],"token_limit":512,"languages":["zh","en"],"enable_parent_child":true}}'
```

也可以用 JSON 试切 Markdown/TXT：

```json
{
  "filename": "notes.md",
  "content_type": "text/markdown",
  "content": "# 部署\n\n正文内容。",
  "process_config": {
    "chunking_config": {
      "strategy": "auto",
      "chunk_size": 512,
      "chunk_overlap": 80,
      "parent_chunk_size": 4096,
      "child_chunk_size": 384,
      "separators": ["\n\n", "\n", "。", "！", "？"],
      "token_limit": 512,
      "languages": ["zh", "en"],
      "enable_parent_child": true
    }
  }
}
```

返回的 `diagnostics` 会说明最终采用了哪种策略、是否回退、Chunk 长度统计和近似 token 统计；父子模式还会分别返回 `parentDiagnostics` 和 `childDiagnostics`。

前端工作台会调用 `GET /api/knowledge-bases/{id}/documents` 刷新文档列表和最新处理状态。启动前端后，打开 `http://localhost:5173` 即可完成“创建知识库 → 上传资料 → 查看处理状态 → 流式提问”的操作；问答结果中的引用可以展开查看原始文本片段。

图片文档的正文索引完成后，摘要、摘要索引、OCR、Caption 和推荐问题会进入统一后处理 Worker，不阻塞正文检索。可以通过下面的接口查看各分支任务状态、重试次数、模型信息和耗时：

```sh
curl http://localhost:8080/api/knowledge-bases/1/documents/9/postprocess-tasks
```

相关限流和重试配置写在后端 `.env`：

```env
POSTPROCESS_TASK_CONCURRENCY=2
POSTPROCESS_TASK_MAX_ATTEMPTS=3
POSTPROCESS_TASK_LEASE=5m
IMAGE_CAPTION_PROMPT=请描述这张图片的主要内容和结构
```

回答下方的“检索详情”可以展开本轮 RAG 流水线统计，包括向量/关键词召回、关键词阈值过滤、去重、Rerank 和最终距离过滤；这些是计数和状态，不会展示 API Key 或原始查询改写内容。统计会随助手消息保存，重新打开历史会话后仍可查看；旧消息没有统计时会正常省略该面板。

删除文档使用 `DELETE /api/knowledge-bases/{id}/documents/{documentID}`。服务端会确认文档属于当前管理员和指定知识库，删除数据库记录及其处理任务、父子 chunk，并清理该知识库的检索缓存。仍在 `pending` 或 `processing` 状态的文档暂不允许删除，会返回 `409`；原始文件清理失败会记录后端日志，但不会把已删除的文档重新暴露给应用。

工作台左侧底部的“模型服务设置”可以读取和保存 Provider 的服务名称、Base URL、聊天模型、可选聊天模型列表，以及可选的 Rerank Base URL 和 Rerank 模型，并发起连接测试。Embedding 由后端 `.env` 中的本地配置控制，前端不再要求填写嵌入模型；API Key 也不在页面输入，仍只需要写入后端 `.env`。标准 Agent 会话顶部可以从服务端配置的聊天模型列表中切换回答模型，选择会保存到当前会话。Rerank 两项同时填写时，混合召回会先扩大候选集，再调用 `qwen3-rerank` 重排；留空则不调用第二个模型。

模型服务配置保存后，可调用 `POST http://localhost:8080/api/model-provider/connection-test` 检查连通性。该请求会从 `.env` 指定的环境变量（默认示例为 `OPENAI_API_KEY`）读取密钥，并向模型服务的 `{baseUrl}/models` 发起认证请求；密钥不会写入 PostgreSQL 或接口响应。

为避免 API Key 被发送给错误的地址，`.env` 中的 `MODEL_PROVIDER_ALLOWED_HOSTS` 必须列出允许测试的模型服务主机名（逗号分隔）。测试仅接受 HTTPS `baseUrl`，且不会跟随重定向。使用其他 OpenAI 兼容服务时，同时更新该列表和 `MODEL_PROVIDER_API_KEY_ENV_VAR`，再重新导出 `.env` 后启动后端。

嵌入调用测试使用 `POST http://localhost:8080/api/model-provider/embedding-test`，请求体示例：

```json
{"input":["需要转成向量的文本"]}
```

它使用本地 Embedding 配置（未配置时回退到已保存的 `embeddingModel`）调用 `/embeddings` 并返回向量。该接口仅用于本地开发验证；调用真实模型服务可能产生费用。

知识库语义检索使用 `POST http://localhost:8080/api/knowledge-bases/{id}/search`，请求体示例：

```sh
curl -X POST http://localhost:8080/api/knowledge-bases/1/search \
  -H 'Content-Type: application/json' \
  -d '{"query":"这份资料如何启动服务？","limit":5}'
```

接口会将问题向量化，并在指定知识库内执行向量 + 关键词混合召回；如果配置了 Rerank，还会对候选片段重新排序。目前它只返回检索结果，还没有生成最终回答。

知识库问答使用 `POST http://localhost:8080/api/knowledge-bases/{id}/chat`，请求体示例：

```sh
curl -X POST http://localhost:8080/api/knowledge-bases/1/chat \
  -H 'Content-Type: application/json' \
  -d '{"message":"这份资料如何启动服务？","topK":5}'
```

普通问答接口会先检索相关文本块，再调用聊天模型生成回答，并在 `sources` 字段返回使用到的来源；它使用 JSON 一次性返回结果。

流式问答使用 `POST http://localhost:8080/api/knowledge-bases/{id}/chat/stream`。它返回 `text/event-stream`，事件顺序为 `sources`、多个 `delta`，最后是 `done`；如果流式过程中发生错误，则返回 `error` 事件。该接口适合前端使用 `fetch` 读取响应流。

聊天调用测试使用 `POST http://localhost:8080/api/model-provider/chat-test`，请求体示例：

```json
{"message":"请只回复 OK"}
```

它使用已保存的 `chatModel` 调用 `{baseUrl}/chat/completions`，并返回模型的第一条回答。该接口同样仅用于本地开发验证；调用真实模型服务可能产生费用。

另开一个终端启动前端：

```sh
cd frontend
npm install
npm run dev
```

前端默认运行在 `http://localhost:5173`，并将后端请求代理到 `http://localhost:8080`。

问答台可以切换“标准 Agent”和“协作研究”。标准 Agent 会写入当前会话，并兼容支持 `reasoning_content` 的模型：可以选择快速/标准/深度思考级别（映射为 OpenAI-compatible `reasoning_effort`），模型返回深度思考时，回答前会显示默认折叠的思考卡片；思考内容只保留在当前页面，不写入会话历史。标准 Agent 还支持本轮 PNG/JPEG/WEBP 图片和 TXT/Markdown 文本附件，附件只发送给当前模型请求，不自动写入知识库。协作研究会展示 Researcher 的检索轨迹与引用，但结果只保留在当前页面，不写入会话历史。

知识库中的图片和扫描页会保留原图资源，同时生成独立的 `image_ocr` / `image_caption` 检索块；原图存于上传资源文件，不把 Base64 或二进制写入 Chunk。OCR 结果默认使用已配置的 `OCR_MODEL`，如需为图片生成额外视觉描述，可配置 `IMAGE_CAPTION_PROMPT`，留空则不会增加第二次视觉模型调用。检索命中图片块时，结果会带 `imageInfo` 和原图资源 URL。

助手完成后的回答支持基础 Markdown（标题、列表、引用、代码块、表格和链接）。渲染前会在浏览器中清理危险 HTML；流式生成中仍显示纯文本，避免半截 Markdown 造成布局问题。

标准 Agent 的 SSE 响应会带 `X-Agent-Run-ID`。如果浏览器连接中途断开，前端会使用这个 ID 请求：

```text
GET /api/knowledge-bases/{id}/agent-runs/{runID}/stream
```

服务端会先重放已产生的事件，再推送后续事件；前端按事件 ID 去重，因此不会重新调用模型。当前事件只在本进程内保留最多 128 个运行、每个运行最多 512 个事件、默认 10 分钟；服务重启或多实例部署不会共享这段短期缓存。

## WeKnora 风格 RAG 评测

评测使用 `queries/corpus/answers/qrels/qas` 五组数据语义，同时计算检索指标（Precision、Recall、NDCG@3、NDCG@10、MRR、MAP）和生成指标（BLEU-1/2/4、ROUGE-1/2/L）。通过 API 创建任务后由后台 Worker 异步执行，结果会逐题写入 PostgreSQL，支持进度查询和 Worker 崩溃恢复。

创建评测任务：

```sh
curl -X POST http://localhost:8080/api/knowledge-bases/1/evaluations \
  -H 'Content-Type: application/json' \
  -d @eval/weknora-dataset.json
```

查询任务和逐题结果：

```sh
curl http://localhost:8080/api/knowledge-bases/1/evaluations/12
```

请求中的 `passage_chunk_ids` 用于把数据集的 corpus `pid` 映射到当前知识库的稳定 Chunk 引用，例如 `{"10":"3:5"}`。没有映射就无法将 qrels 与真实检索结果比较。

## 最小 Multi-Agent 协作

`POST /api/knowledge-bases/{id}/multi-agent-chat` 提供一个非流式的进程内协作示例：`Supervisor` 先让受最大步数限制的只读 `Researcher` 根据证据自主决定是否继续检索，再把带引用的研究资料交给无工具的 `Answerer` 生成最终回答。Researcher 只允许访问当前知识库的 `knowledge_search` 工具，并会阻止重复查询。

```sh
curl -X POST http://localhost:8080/api/knowledge-bases/1/multi-agent-chat \
  -H 'Content-Type: application/json' \
  -d '{"message":"这份资料如何启动服务？","topK":5}'
```

需要观察研究轮次时使用 SSE：

```sh
curl -N -X POST http://localhost:8080/api/knowledge-bases/1/multi-agent-chat/stream \
  -H 'Content-Type: application/json' \
  -d '{"message":"这份资料如何启动服务？","topK":5}'
```

事件包括 `research_tool_called`、`research_tool_finished`、`research_summary`、`answerer_finished` 和 `run_finished`；原有非流式接口保持不变。

响应中的 `steps` 会标记 `researcher` 和 `answerer` 的执行状态。资料不足时会直接返回拒答，不再额外调用回答模型。该入口暂不保存会话，但已提供独立 SSE 路由；同时不引入 SQL/schema、Redis 或 A2A。已有的 `/agent-chat` 和 `/agent-chat/stream` 仍是带会话的主问答入口。

## 最小 MCP 只读适配

每个知识库提供一个独立的 MCP HTTP JSON-RPC 入口：

```text
POST http://localhost:8080/api/knowledge-bases/{id}/mcp
```

当前暴露两个只读工具：`knowledge_search` 和 `document_list`。MCP 客户端可以依次调用 `server/discover`、`tools/list` 和 `tools/call`；知识库 ID 固定来自 URL，并且服务端会校验它属于当前管理员，工具参数不能改变知识库范围。适配器使用无会话的 `2026-07-28` 请求头，同时兼容旧版 `initialize` 握手，尚未实现 MCP 的资源、提示词、授权和任务扩展。

示例调用：

```sh
curl -X POST http://localhost:8080/api/knowledge-bases/1/mcp \
  -H 'Content-Type: application/json' \
  -H 'MCP-Protocol-Version: 2026-07-28' \
  -H 'Mcp-Method: tools/list' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"curl","version":"1.0"},"io.modelcontextprotocol/clientCapabilities":{}}}}'

curl -X POST http://localhost:8080/api/knowledge-bases/1/mcp \
  -H 'Content-Type: application/json' \
  -H 'MCP-Protocol-Version: 2026-07-28' \
  -H 'Mcp-Method: tools/call' \
  -H 'Mcp-Name: knowledge_search' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"knowledge_search","arguments":{"query":"这份资料如何启动服务？","limit":5},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientInfo":{"name":"curl","version":"1.0"},"io.modelcontextprotocol/clientCapabilities":{}}}}'
```

Go 侧的最小客户端位于 `internal/mcp`，可用于把同一个只读工具接入后续 Agent 编排；当前没有把 API Key 或模型调用移到浏览器。

## 最小 A2A HTTP 适配

当前提供一个项目内的最小 Agent-to-Agent HTTP 入口，用于让其他 Agent 以任务方式调用本项目的 Multi-Agent Supervisor。它不是完整的官方 A2A 实现，暂不提供分布式队列、认证或推送流；任务使用 PostgreSQL 持久化。

```text
GET  /.well-known/agent.json   查看 Agent Card
POST /api/a2a/tasks            创建任务
GET  /api/a2a/tasks/{id}       查询任务状态
GET  /api/a2a/tasks/{id}/result 获取完成结果
```

创建任务示例：

```sh
curl -X POST http://localhost:8080/api/a2a/tasks \
  -H 'Content-Type: application/json' \
  -d '{"knowledge_base_id":1,"message":"这份资料如何启动服务？","top_k":5}'
```

任务会经历 `submitted → working → completed`，失败时进入 `failed`。服务启动时使用 PostgreSQL 保存任务，后台 A2A Runner 负责领取、执行和定期清理过期终态任务；任务结果在服务重启后仍可查询。默认保留 7 天，可通过 `A2A_TASK_RETENTION` 和 `A2A_CLEANUP_INTERVAL` 调整。已有 `/metrics` 会记录 `a2a_tasks_submitted_total`、`a2a_tasks_started_total`、`a2a_tasks_completed_total`、`a2a_tasks_failed_total` 和总耗时。内存 Store 仅用于单元测试和本地简化示例。

Go pprof 诊断接口默认关闭。需要本机排查性能时，可在 `.env` 设置 `PPROF_ADDRESS=127.0.0.1:6060`，然后访问 `http://127.0.0.1:6060/debug/pprof/`。不要将该端口暴露到公网。

当前有两个可重复的本地基线 benchmark：`go test -run '^$' -bench '^BenchmarkPprofIndex$' -benchmem ./internal/diagnostics` 测量 pprof 页面边界，`go test -run '^$' -bench '^BenchmarkKnowledgeBaseSearchHandler$' -benchmem ./internal/handler` 测量知识库搜索 Handler 的 JSON/参数边界。两者都使用本地 stub，不访问 PostgreSQL、embedding 或聊天模型，不消耗 API token；它们不能代表真实检索或模型延迟。
### 文档后处理任务

文档正文索引完成后，摘要、摘要索引、图片 OCR、图片 Caption 和推荐问题进入统一的后台后处理队列。可以通过下面的接口查看一个文档的各分支状态、重试次数、模型信息、Token 统计、成本和耗时：

```text
GET /api/knowledge-bases/{id}/documents/{documentID}/postprocess-tasks
```

相关配置：

```env
POSTPROCESS_TASK_CONCURRENCY=2
POSTPROCESS_TASK_MAX_ATTEMPTS=3
POSTPROCESS_TASK_LEASE=5m
```
