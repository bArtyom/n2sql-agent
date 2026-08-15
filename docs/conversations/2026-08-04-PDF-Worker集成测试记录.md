# 2026-08-04 PDF Worker 集成测试记录

## 背景

PDF 提取器已经完成，但还需要确认它能通过 Worker 的公共入口进入文本块写入流程，而不是只在提取器单元测试中通过。

## 测试边界

- 入口：`worker.Runner.RunOnce`。
- 真实组件：`documentextractor.Extractor`、`documentchunk.Splitter`、`NewEmbeddingChunkingProcessor`。
- Stub 组件：任务队列、文本块存储和嵌入服务，避免依赖 PostgreSQL 或真实 API Key。

## 已验证行为

测试会写入一个 PDF 文件，Worker 领取 `application/pdf` 任务，提取出 `PDF worker text`，切成一个 chunk，写入对应向量，并将任务标记为 `succeeded`。

## 验证命令

```sh
go test ./internal/worker
```

后续仍需在真实环境完成上传接口、数据库 Worker 和模型服务的端到端验证。
