# n2sql-agent

`n2sql-agent` 是一个本地运行的通用文档知识库问答平台。用户可以整理资料，并基于已处理的文档获得带引用来源的回答。

当前阶段建设稳定、可扩展的工程底座与文档知识库最小闭环。

## 本地开发

准备环境：Go 1.22+、Node.js 20+、Docker Compose。

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

删除知识库会级联删除关联的文档与处理任务记录。删除知识库时的原始文件清理流程尚未实现。

上传文件使用 `POST /api/knowledge-bases/{id}/documents`，提交名为 `file` 的 `multipart/form-data` 字段：

```sh
curl -X POST http://localhost:8080/api/knowledge-bases/1/documents \
  -F 'file=@./notes.txt;type=text/plain'
```

首版只接收 Markdown、TXT 与可提取文本的 PDF，单个文件不超过 10MB。源文件保存在 `UPLOAD_DIR`（默认 `./.data/uploads`），并在数据库中同时创建文档与 `pending` 处理任务。

前端工作台会调用 `GET /api/knowledge-bases/{id}/documents` 刷新文档列表和最新处理状态。启动前端后，打开 `http://localhost:5173` 即可完成“创建知识库 → 上传资料 → 查看处理状态 → 流式提问”的操作；问答结果中的引用可以展开查看原始文本片段。

工作台左侧底部的“模型服务设置”可以读取和保存 Provider 的服务名称、Base URL、聊天模型与嵌入模型，并发起连接测试。API Key 不在页面输入，仍只需要写入后端 `.env` 的 `OPENAI_API_KEY`。

模型服务配置保存后，可调用 `POST http://localhost:8080/api/model-provider/connection-test` 检查连通性。该请求会从 `.env` 指定的环境变量（默认示例为 `OPENAI_API_KEY`）读取密钥，并向模型服务的 `{baseUrl}/models` 发起认证请求；密钥不会写入 PostgreSQL 或接口响应。

为避免 API Key 被发送给错误的地址，`.env` 中的 `MODEL_PROVIDER_ALLOWED_HOSTS` 必须列出允许测试的模型服务主机名（逗号分隔）。测试仅接受 HTTPS `baseUrl`，且不会跟随重定向。使用其他 OpenAI 兼容服务时，同时更新该列表和 `MODEL_PROVIDER_API_KEY_ENV_VAR`，再重新导出 `.env` 后启动后端。

嵌入调用测试使用 `POST http://localhost:8080/api/model-provider/embedding-test`，请求体示例：

```json
{"input":["需要转成向量的文本"]}
```

它使用已保存的 `embeddingModel` 调用 `{baseUrl}/embeddings` 并返回向量。该接口仅用于本地开发验证；调用真实模型服务可能产生费用。

知识库语义检索使用 `POST http://localhost:8080/api/knowledge-bases/{id}/search`，请求体示例：

```sh
curl -X POST http://localhost:8080/api/knowledge-bases/1/search \
  -H 'Content-Type: application/json' \
  -d '{"query":"这份资料如何启动服务？","limit":5}'
```

接口会将问题向量化，并在指定知识库内按 pgvector 距离返回最相似的文本块。目前它只返回检索结果，还没有生成最终回答。

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

## 检索阈值评测

`retrieval-eval` 用一组带 `expected_relevant` 标签的问题比较多个 pgvector 距离阈值。它只调用 Embedding 和检索，不调用聊天模型。

先执行不产生外部调用的 dry-run：

```sh
go run ./cmd/retrieval-eval \
  --cases eval/retrieval-threshold-cases.json
```

确认 `.env` 已导出、PostgreSQL 正在运行且允许消耗 Embedding 配额后，再执行真实评测：

```sh
set -a
. ./.env
set +a
go run ./cmd/retrieval-eval \
  --live \
  --cases eval/retrieval-threshold-cases.json \
  --thresholds 0.55,0.60,0.65,0.70,0.75
```

输出中的 `recall` 表示文档内问题被命中的比例，`refusal_rate` 表示文档外问题被正确拒答的比例，`false_refusals` 和 `unsupported_accepts` 分别表示误拒答和漏拒答。新增问题时复制 JSON 中的字段，并明确标注 `expected_relevant`，不要用模型自动生成标签。

## 最小 Multi-Agent 协作

`POST /api/knowledge-bases/{id}/multi-agent-chat` 提供一个非流式的进程内协作示例：`Supervisor` 先让只读 `Researcher` 调用知识库检索工具，再把带引用的研究资料交给无工具的 `Answerer` 生成最终回答。

```sh
curl -X POST http://localhost:8080/api/knowledge-bases/1/multi-agent-chat \
  -H 'Content-Type: application/json' \
  -d '{"message":"这份资料如何启动服务？","topK":5}'
```

响应中的 `steps` 会标记 `researcher` 和 `answerer` 的执行状态。资料不足时会直接返回拒答，不再额外调用回答模型。该入口暂不保存会话、不提供 SSE，也不引入 SQL/schema、Redis、MCP 或 A2A；已有的 `/agent-chat` 和 `/agent-chat/stream` 仍是带会话的主问答入口。
