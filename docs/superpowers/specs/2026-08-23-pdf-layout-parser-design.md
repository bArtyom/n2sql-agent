# PDF 原生页、扫描页与版面解析设计

## 目标

按照 WeKnora 的分层解析思路，完成 PDF 的统一解析链路：

- 原生文字页优先使用 PDF 内置文本，不调用 OCR；
- 扫描页和文字不足页按页判断，只对候选页渲染和 OCR；
- 复杂页面先做版面分析，区分文字、表格和图片区域；
- 图片区域裁剪后进入现有 `ImageAsset` 与 `image_ocr/image_caption` 子 Chunk 链路；
- 原生页、扫描页、混合 PDF 最终都输出统一 `ParseResult`。

## 非目标

- 本阶段不改 DOCX、PPTX、XLSX 的解析逻辑；
- 不把图片二进制或 Base64 写入 Chunk 正文；
- 不对所有普通 PDF 页面强制调用远程版面模型；
- 不在 OCR 结果上额外调用“摘要模型”。

## 现有基础

当前代码已经具备：

- `extractPDFText` 的整份原生文本提取；
- `PDFToTextPageExtractor` 的逐页文本提取；
- `PDFToImageRenderer` 的页面渲染；
- `documentocr.Service` 的按页 OCR 和并发控制；
- `PaddleOCRVLParserEngine` 的远程 `/layout-parsing` 适配；
- `ParseResult.Markdown/Images/Metadata`；
- 图片资源保存和图片 OCR/Caption 子 Chunk 索引。

本次改造会复用这些边界，不把 PDF 解析逻辑塞进 Worker。

## 核心数据结构

### PageState

每一页先得到一个内部状态：

```go
type PDFPageState struct {
    Number       int
    NativeText   string
    TextRunes    int
    HasImage     bool
    NeedsOCR     bool
    NeedsLayout  bool
}
```

`NeedsOCR` 的判断不使用整份 PDF 的总字符数，而使用页级信号：

- 原生文字为空；
- 原生文字低于最小页文本阈值；
- 页面存在图片且文字量明显不足。

文字充足但存在嵌入图片的原生页不需要整页 OCR，只需要提取图片资源。

### PageBlock

版面分析器输出统一区域：

```go
type PDFPageBlock struct {
    Page     int
    Kind     string // text, table, figure
    Order    int
    Text     string
    Image    []byte
    MIMEType string
    Bounds   [4]int
    Source   string
}
```

`Bounds` 使用页面图片坐标，`Order` 用于恢复阅读顺序。`figure` 区域才进入图片资源链路；`text` 和 `table` 区域进入 Markdown。

## 处理流程

```text
PDF bytes
  ↓
逐页获取原生文本
  ↓
判断每页状态
  ├── text_rich + 无复杂图片 → 原生文本
  ├── text_rich + 嵌入图片   → 原生文本 + 图片资源
  └── text_sparse/image_only → 渲染页面
                                  ↓
                              版面分析
                                  ├── text → OCR/原生文字
                                  ├── table → Markdown 表格
                                  └── figure → 裁剪图片
  ↓
按页码和区域顺序合并 Markdown
  ↓
ParseResult
  ├── Markdown → 结构化切分
  ├── Images → 保存原图/裁剪图
  └── Metadata → 解析诊断
```

### 原生文字页

先用逐页文本提取器读取文字。文字达到阈值时，直接使用原生文本；页面中的嵌入图片由图片提取器或版面解析器生成 `ImageAsset`，不对整页重复 OCR。

### 扫描页和混合页

文字为空或明显不足的页面才渲染。渲染后优先调用可选的 PaddleOCR-VL/MinerU 版面分析器：

- 文字区域生成 OCR 文本；
- 表格区域生成 Markdown 表格；
- 图片区域按坐标裁剪并生成 `ImageAsset`。

如果版面分析器未配置或调用失败，则降级为现有整页 OCR，并记录 `layout_mode=fallback`。降级结果仍可入库，但不承诺能提取页面内的独立图片区域。

扫描页只做 OCR，不做 Caption；Caption 只属于独立图片/图表区域的多模态增强。

## 去重与合并规则

- 原生文字充足的页面不再把整页 OCR 文本追加进去；
- 原生文字不足的页面，以页面区域 OCR 结果补足页面内容；
- 版面分析返回的 `text` 区域按 `Order` 合并；
- `table` 区域直接作为 Markdown 表格；
- `figure` 区域不拼进正文，单独保存并交给现有图片子 Chunk 流程；
- 页面失败时保留其他页面，最终在 metadata 中记录失败页和质量警告。

## 版面分析接口

新增可选接口：

```go
type PDFLayoutAnalyzer interface {
    AnalyzePage(context.Context, documentocr.PageImage) ([]PDFPageBlock, error)
}
```

实现顺序：

1. 复用现有 PaddleOCR-VL 远程适配，输入单页图片；
2. 保留 MinerU/其他远程解析器的扩展位置；
3. 没有布局服务时使用整页 OCR fallback。

版面分析器的失败不应让普通文字页失败，也不应清空已经提取出的原生文字。

## 元数据

最终 `ParseResult.Metadata` 至少包含：

```json
{
  "parser_mode": "native|mixed|ocr|layout",
  "text_source": "embedded_text|ocr|mixed",
  "layout_mode": "paddleocr_vl|mineru|fallback|none",
  "page_count": "12",
  "ocr_pages": "2,5",
  "figure_count": "3",
  "layout_failed_pages": "7"
}
```

元数据只用于诊断、引用和前端预览，不进入正文 Embedding。

## 错误处理

- 原生文字提取失败：进入页面渲染/布局路径；
- 单页 OCR 失败：记录页面错误，继续处理其他页；
- 版面分析失败：该页降级整页 OCR；
- 图片裁剪失败：保留页面文字，记录图片质量警告；
- 所有页面都没有可用文本：文档处理失败，并返回明确的 OCR/解析错误；
- 已有正文 Chunk 成功后，图片子 Chunk 失败仍只产生 warning，不回滚正文索引。

## 测试范围

- 全部原生文字 PDF 不调用 OCR/布局服务；
- 纯扫描 PDF 逐页 OCR，并保持页码顺序；
- 混合 PDF 只 OCR 空页/低文本页；
- 文字页中的图片不重复 OCR 正文；
- 版面分析能输出文字、表格、图片三类区域；
- 版面分析失败时能降级整页 OCR；
- 图片区域能进入 `ImageAsset` 和图片子 Chunk；
- 单页失败不会丢失其他页面；
- 解析 metadata 能准确反映 OCR 页、布局模式和失败页。
