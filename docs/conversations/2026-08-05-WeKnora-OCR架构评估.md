# WeKnora OCR 架构评估

## 背景

当前项目已经完成普通 Markdown/TXT/PDF 文本链路，但本地 PDF 提取器不是 OCR 引擎，扫描型 PDF 只有图片时无法直接得到可检索文本。本次先参考 `/Users/lsp/project/WeKnora`，暂不修改代码。

## 已确认的 WeKnora 做法

- WeKnora 把复杂文档解析拆到独立的 `docreader` gRPC 服务，而不是在 Go 主服务中直接堆叠所有解析库。
- `docreader/parser/pdf_parser.py` 按页判断页面类型：原生文本页保留文本层；图片覆盖率高或文本极少的页面判定为 scanned，渲染成 JPEG，并通过 `ImageRef` 返回给 Go App。
- DocReader 本身不再内置 OCR/VLM 后端。OCR/VLM 由 Go App 侧调用，识别结果再和 Markdown/图片引用合并。
- `ReadStream` 用于逐张返回图片，避免多页扫描件一次性塞进单个 gRPC 消息导致内存或消息大小问题。
- Go 侧还提供 PaddleOCR-VL、MinerU 等可选解析引擎；普通链路和专用 OCR/版面解析引擎可以按配置选择。

## 对当前项目的结论

第一阶段不直接复制完整的 docreader + gRPC + 多引擎体系。最小可行演进为：

```text
上传 PDF
  -> 按页提取文本并判断是否为扫描页
  -> 扫描页渲染成图片
  -> OCRProvider 识别图片为文本
  -> 将 OCR 文本交给现有 chunker
  -> 复用现有 embedding、入库和问答流程
```

Markdown/TXT 路径不变；OCR 只作为 PDF 扫描页的 fallback。`text-embedding-v4` 负责文本向量化，不能替代 OCR；图片必须先被 OCR/VLM 转成文字。

## 待确认决策

- 第一版 OCR 采用本地 PaddleOCR/Tesseract，还是接入支持图像输入的云端 VLM/OCR 服务。
- 是否需要保留页码、图片引用和 OCR 置信度等元数据。
- 扫描页数量、渲染 DPI、并发数和单文档页数上限应配置化，避免大文件导致成本或内存失控。

## 下一步

已先完成最小结构：`internal/documentocr` 提供页面渲染器、OCR provider 和页序合并服务；可选的 `pdftoppm` 页面渲染器以及 OpenAI 兼容图片 OCR 请求已经接入扫描 PDF fallback。只有配置 `OCR_MODEL` 时才启用，未配置时普通文本流程保持不变。

仍需后续确认真实的视觉模型是否支持当前 OpenAI 兼容图片请求格式，并在安装 Poppler、配置视觉模型后做一次真实扫描件验证。

## 本地启用记录

- 已通过 Homebrew 安装 Poppler，当前 `pdftoppm` 为 `/opt/homebrew/bin/pdftoppm`。
- `.env` 已配置 `OCR_MODEL=qwen3-vl-plus` 及渲染参数；API Key 仍复用 `DASHSCOPE_API_KEY`，没有写入代码或数据库。
- 后端已重启并打印 `scanned PDF OCR enabled`，健康检查返回 `{"status":"ok"}`。
- 尚未上传真实扫描件，因此 OCR 远端调用和识别准确率仍待实际文件验证。
