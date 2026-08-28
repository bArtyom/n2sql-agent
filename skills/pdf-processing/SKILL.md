---
name: pdf-processing
description: 处理 PDF 文档问题，判断原生文字、扫描页面和版面图片的差异
license: MIT
allowed-tools:
  - document_info
  - document_read
  - document_summary
---
# PDF 处理

当用户询问 PDF 的解析、扫描件、图片或表格时，先确认文档 ID 和处理状态。

## 处理原则

1. 优先使用文档已有的原生文字层。
2. 只有原生文字缺失或页面明确标记为 OCR 时，才解释 OCR 结果。
3. 需要完整概括时使用 `document_summary`，不要循环读取整篇正文。
4. 回答只能依据工具返回的内容；资料不足时明确说明。
