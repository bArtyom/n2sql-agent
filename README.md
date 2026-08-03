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

首版只接收 Markdown、TXT 与 PDF，单个文件不超过 10MB。源文件保存在 `UPLOAD_DIR`（默认 `./.data/uploads`），并在数据库中同时创建文档与 `pending` 处理任务。

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
