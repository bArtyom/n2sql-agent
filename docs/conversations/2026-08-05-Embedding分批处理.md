# Embedding 分批处理

## 背景

`text-embedding-v4` 单次最多接收 10 条文本。原 worker 将一个文档切出的所有 chunk 一次性发送，导致较长文档调用百炼 `/embeddings` 返回 HTTP 400。

## 已完成

- `internal/worker` 按每批 10 个 chunk 调用 Embedding 服务。
- 所有批次成功后才一次性写入 `document_chunks`，中途失败不会保存半成品。
- 错误信息带有失败批次范围，例如 `embed batch 10-10`，便于定位。
- 新增 11 个 chunk 的分批测试和批次失败不落库测试。

## 配置决策

- 批次大小暂不放入 `.env`，固定为 10，确保兼容 `text-embedding-v4`，也兼容批次上限更高的模型。
- `qwen3.7-text-embedding` 虽支持最多 20 条，但换模型不能替代通用分批逻辑。

## 验证

- `go test ./...` 通过。

## 下一步

- 重新启动后端并重新上传或重试失败文档。
- 如仍失败，保留上游响应正文，进一步区分模型权限、API Key、输入限制等错误。
