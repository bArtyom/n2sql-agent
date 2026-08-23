# WeKnora 风格 PDF 版面解析 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 完成原生 PDF、扫描 PDF 和混合 PDF 的逐页解析，并把文字、表格和图片区域统一接入现有 Chunk/图片子 Chunk 链路。

**Architecture:** `documentextractor` 负责 PDF 页面状态、版面区域契约和最终 `ParseResult` 合并；`documentocr` 负责页数、原生页文本、页面渲染和 OCR；PaddleOCR-VL 作为可选 `PDFLayoutAnalyzer`。普通页不经过远程模型，扫描/低文本页才进入布局或整页 OCR fallback。

**Tech Stack:** Go、现有 `documentextractor`/`documentocr` 包、Poppler `pdftotext`/`pdftoppm`/`pdfinfo`、现有 PaddleOCR-VL `/layout-parsing` HTTP 适配、PostgreSQL 现有 ParseResult/ImageAsset/图片子 Chunk 链路。

**Spec:** `docs/superpowers/specs/2026-08-23-pdf-layout-parser-design.md`

## Global Constraints

- 普通文字页优先使用 PDF 内置文本，不调用 OCR/VLM。
- 只有空文本、低文本或明确含图片的候选页才渲染/布局分析。
- 原图和裁剪图只进入 ImageAsset/资源存储，不进入 Chunk 正文或 Embedding 正文。
- 布局失败降级整页 OCR；单页失败不丢弃其他页面。
- 图片 OCR/Caption 子 Chunk 失败不能回滚已经成功的正文 Chunk。
- 不修改 Worker 的解析算法；Worker 只消费统一 `ParseResult`。

---

### Task 1: 定义页级状态和版面区域契约

**Files:**
- Create: `internal/documentextractor/pdf_layout.go`
- Test: `internal/documentextractor/pdf_layout_test.go`

**Interfaces:**
- Produces `PDFPageState`, `PDFPageBlock`, `PDFPageInspector`, `PDFEmbeddedImageExtractor`, `PDFLayoutAnalyzer` and pure page classification helpers for the PDF parser.

- [ ] **Step 1: Write the failing tests**

```go
func TestClassifyPDFPage(t *testing.T) {
    tests := []struct {
        name      string
        text      string
        hasImage  bool
        wantOCR   bool
        wantLayout bool
    }{
        {name: "rich text", text: strings.Repeat("正文 ", 80)},
        {name: "empty page", wantOCR: true, wantLayout: true},
        {name: "sparse page with image", text: "标题", hasImage: true, wantOCR: true, wantLayout: true},
    }
    for _, tt := range tests {
        state := ClassifyPDFPage(1, tt.text, tt.hasImage, 100)
        if state.NeedsOCR != tt.wantOCR || state.NeedsLayout != tt.wantLayout {
            t.Fatalf("%s: state=%#v", tt.name, state)
        }
    }
}

func TestPDFPageBlockKindsAreStable(t *testing.T) {
    block := PDFPageBlock{Page: 2, Kind: PDFBlockFigure, Order: 3, Bounds: [4]int{1, 2, 3, 4}}
    if block.Kind != PDFBlockFigure || block.Page != 2 || block.Order != 3 {
        t.Fatalf("block=%#v", block)
    }
}
```

- [ ] **Step 2: Run the focused tests and verify they fail**

Run: `go test ./internal/documentextractor -run 'TestClassifyPDFPage|TestPDFPageBlockKindsAreStable' -count=1`

Expected: FAIL because the page-state types and classifier do not exist.

- [ ] **Step 3: Implement the minimal contracts**

```go
const (
    PDFBlockText   = "text"
    PDFBlockTable  = "table"
    PDFBlockFigure = "figure"
)

type PDFPageState struct {
    Number      int
    NativeText  string
    TextRunes   int
    HasImage    bool
    NeedsOCR    bool
    NeedsLayout bool
}

type PDFPageBlock struct {
    Page     int
    Kind     string
    Order    int
    Text     string
    Image    []byte
    MIMEType string
    Bounds   [4]int
    Source   string
}

type PDFPageInspector interface {
    InspectPages(context.Context, []byte) ([]PDFPage, error)
}

type PDFEmbeddedImageExtractor interface {
    ExtractPageImages(context.Context, []byte, int) ([]ImageAsset, error)
}

type PDFLayoutAnalyzer interface {
    AnalyzePage(context.Context, PDFPage) ([]PDFPageBlock, error)
}
```

`ClassifyPDFPage` counts trimmed runes, marks an empty page as OCR/layout work, and marks a low-text page with an image as OCR/layout work. A rich text page never requests whole-page OCR.

- [ ] **Step 4: Run the focused tests and verify they pass**

Run: `go test ./internal/documentextractor -run 'TestClassifyPDFPage|TestPDFPageBlockKindsAreStable' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/documentextractor/pdf_layout.go internal/documentextractor/pdf_layout_test.go
git commit -m "feat: add PDF page layout contracts"
```

### Task 2: Add page-level inspection without OCR

**Files:**
- Create: `internal/documentocr/page_inspector.go`
- Modify: `internal/documentocr/service.go`
- Modify: `internal/documentocr/page_text.go`
- Modify: `internal/documentocr/renderer.go`
- Test: `internal/documentocr/page_inspector_test.go`

**Interfaces:**
- Implements `documentextractor.PDFPageInspector` through `Service.InspectPages(ctx, pdf) ([]documentextractor.PDFPage, error)` without calling the OCR provider.
- Produces `Service.RenderPages(ctx, pdf, pageNumbers) ([]PageImage, error)` so only candidate pages are rendered.

- [ ] **Step 1: Write failing tests**

Create a fake page counter, page-text extractor, renderer and OCR provider. Assert that `InspectPages` returns pages in numeric order, includes native text, and provider call count remains zero. Assert `RenderPages` only asks the renderer for requested pages.

- [ ] **Step 2: Run focused tests and verify they fail**

Run: `go test ./internal/documentocr -run 'TestInspectPages|TestRenderPages' -count=1`

Expected: FAIL because the inspection and selective rendering methods do not exist.

- [ ] **Step 3: Implement page inspection and selective rendering**

Add a `PageCounter` interface and a Poppler-backed implementation using `pdfinfo`. Add a renderer method that passes explicit `-f/-l` ranges and filters output to requested page numbers. `InspectPages` only calls page counting and `ExtractPageText`; it creates `documentextractor.PDFPage{Number, Text}` with no OCR flag and does not call `Provider.Recognize`.

- [ ] **Step 4: Run focused tests and the existing OCR tests**

Run: `go test ./internal/documentocr -run 'TestInspectPages|TestRenderPages|TestService' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/documentocr/page_inspector.go internal/documentocr/service.go internal/documentocr/page_text.go internal/documentocr/renderer.go internal/documentocr/page_inspector_test.go
git commit -m "feat: inspect PDF pages before OCR"
```

### Task 3: Add PaddleOCR-VL page layout adapter

**Files:**
- Modify: `internal/documentextractor/provider_parsers.go`
- Modify: `internal/documentextractor/provider_parsers_test.go`

**Interfaces:**
- `PaddleOCRVLParserEngine.AnalyzePage(ctx, page PDFPage) ([]PDFPageBlock, error)` implements `PDFLayoutAnalyzer`.

- [ ] **Step 1: Write failing adapter tests**

Use an HTTP test server returning the existing `/layout-parsing` response shape. Assert the request uses `fileType=1`, `useLayoutDetection=true`, and `restructurePages=true`. Assert returned Markdown becomes a text/table block and returned image data becomes a figure block with page number and MIME type.

- [ ] **Step 2: Run the test and verify it fails**

Run: `go test ./internal/documentextractor -run 'TestPaddleOCRVLParserAnalyzesPage' -count=1`

Expected: FAIL because `AnalyzePage` does not exist.

- [ ] **Step 3: Implement the adapter**

Reuse the current provider transport and response decoder. Send one rendered page as an image. Convert non-empty Markdown to `PDFBlockText`, detect Markdown table syntax and use `PDFBlockTable` when present, and convert returned image payloads to `PDFBlockFigure`. Preserve provider name in `Source`; never put image bytes in the text block.

- [ ] **Step 4: Run adapter and provider parser tests**

Run: `go test ./internal/documentextractor -run 'TestPaddleOCRVLParser' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/documentextractor/provider_parsers.go internal/documentextractor/provider_parsers_test.go
git commit -m "feat: analyze PDF pages with PaddleOCR-VL"
```

### Task 4: Refactor the PDF parser into native, mixed and OCR paths

**Files:**
- Create: `internal/documentextractor/pdf_parser.go`
- Modify: `internal/documentextractor/parser.go`
- Modify: `internal/documentextractor/extractor.go`
- Modify: `internal/documentextractor/extractor_test.go`

**Interfaces:**
- `pdfParserEngine` consumes optional page inspection, layout analysis, scanned OCR and image extraction dependencies.
- Existing `ExtractResult` and `ParseResult` signatures remain unchanged.

- [ ] **Step 1: Write failing parser tests**

Add tests for:

```go
func TestPDFParserOCRsOnlySparsePages(t *testing.T)
func TestPDFParserKeepsNativePagesInOrder(t *testing.T)
func TestPDFParserMergesLayoutTextTableAndFigure(t *testing.T)
func TestPDFParserFallsBackToWholePageOCRWhenLayoutFails(t *testing.T)
```

The fake inspector returns a rich page, an empty page and a sparse page. The fake analyzer returns text/table/figure blocks. Assertions verify OCR is called only for candidate pages, Markdown order is stable, images are returned, and metadata includes `ocr_pages`, `layout_mode` and `figure_count`.

- [ ] **Step 2: Run the tests and verify they fail**

Run: `go test ./internal/documentextractor -run 'TestPDFParser(OCRsOnlySparsePages|KeepsNativePagesInOrder|MergesLayoutTextTableAndFigure|FallsBackToWholePageOCRWhenLayoutFails)' -count=1`

Expected: FAIL because the parser has no page-level orchestration.

- [ ] **Step 3: Implement the parser orchestration**

The parser must:

1. inspect every page’s native text;
2. optionally call `PDFEmbeddedImageExtractor.ExtractPageImages` for each page and use the returned assets to set `HasImage`;
3. classify each page using `ClassifyPDFPage`;
4. keep rich pages without OCR;
5. render and analyze only candidate pages;
6. merge text/table blocks by page and `Order`;
7. append embedded and figure assets to `ParseResult.Images` without adding their caption/OCR to Markdown;
8. fall back to page OCR when layout analysis is unavailable or fails;
9. return `parser_mode=native|mixed|ocr|layout`, `text_source`, `ocr_pages`, `layout_mode`, `figure_count` and failure metadata.

When optional page inspection is unavailable, preserve the existing whole-document native extraction and empty-document OCR fallback. This keeps existing deployments functional while the configured page-aware service enables mixed-PDF behavior.

- [ ] **Step 4: Run parser tests and all extractor tests**

Run: `go test ./internal/documentextractor -count=1`

Expected: PASS, including existing force-scanned, empty-PDF and image tests.

- [ ] **Step 5: Commit**

```bash
git add internal/documentextractor/pdf_parser.go internal/documentextractor/parser.go internal/documentextractor/extractor.go internal/documentextractor/extractor_test.go
git commit -m "feat: parse mixed PDFs page by page"
```

### Task 5: Wire page inspection and layout dependencies in the server

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `cmd/server/main.go`
- Modify: `internal/documentextractor/parser.go`
- Modify: `internal/documentextractor/extractor.go`

**Interfaces:**
- Existing `DOCUMENT_PARSER_PADDLEOCR_VL_URL` enables the layout analyzer.
- Existing OCR settings enable page rendering/provider fallback.
- `PDF_IMAGE_BIN` optionally selects the Poppler `pdfimages` binary for embedded PDF images; empty means embedded-image extraction is unavailable.
- No new required environment variable; missing layout/OCR/image services use safe fallback behavior.

- [ ] **Step 1: Write failing wiring/config tests**

Assert that the default extractor receives the page-aware OCR service when `OCR_MODEL` is configured, and receives the PaddleOCR-VL layout analyzer when `DOCUMENT_PARSER_PADDLEOCR_VL_URL` is configured. Assert empty configuration still builds a native-only extractor.

- [ ] **Step 2: Run wiring tests and verify they fail**

Run: `go test ./cmd/server ./internal/config -run 'Test.*PDF|Test.*Parser|TestLoad' -count=1`

Expected: FAIL because the new dependency bundle is not wired into the registry.

- [ ] **Step 3: Implement dependency wiring**

Add a focused `PDFParserDependencies` struct and a constructor that keeps existing constructors as thin wrappers. Pass the configured PaddleOCR-VL engine as `PDFLayoutAnalyzer` and the OCR service as page inspector/OCR provider. Do not read environment variables from the parser package.

- [ ] **Step 4: Run server/config and full tests**

Run: `go test ./cmd/server ./internal/config ./internal/documentextractor ./internal/documentocr -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go cmd/server/main.go internal/documentextractor/parser.go internal/documentextractor/extractor.go
git commit -m "feat: wire PDF page analysis dependencies"
```

### Task 6: Verify image sub-Chunks, diagnostics and documentation

**Files:**
- Modify: `internal/worker/worker_test.go`
- Modify: `internal/documentextractor/extractor_test.go`
- Modify: `agent.md`
- Modify: `docs/conversations/2026-08-23-WeKnora-PDF-OCR-VLM与统一结构化切分.md`
- Modify: `PROJECT_STATUS.md`

**Interfaces:**
- Consumes the finalized `ParseResult.Images` and metadata from Task 5.
- Produces regression coverage and updated learning notes; no new runtime interface.

- [ ] **Step 1: Add end-to-end parser-to-chunk tests**

Use a fake layout analyzer returning one figure and a fake image enricher returning OCR/Caption. Assert the worker stores one normal text path and two image-derived child chunks, while parser metadata remains available for diagnostics.

- [ ] **Step 2: Run focused worker tests and verify behavior**

Run: `go test ./internal/worker -run 'TestEmbeddingHierarchicalProcessorIndexesImageOCRAndCaptionChunks|Test.*PDF' -count=1`

Expected: PASS.

- [ ] **Step 3: Record the final PDF pipeline**

Append a note explaining native, scanned and mixed pages, the page-level threshold, the layout-to-image-child path, and the fallback semantics. Update project status with the completed PDF parsing milestone.

- [ ] **Step 4: Run final verification**

Run: `go test ./...`; `go vet ./...`; `git diff --check`

Expected: all commands exit successfully.

- [ ] **Step 5: Commit**

```bash
git add internal/worker/worker_test.go internal/documentextractor/extractor_test.go agent.md docs/conversations/2026-08-23-WeKnora-PDF-OCR-VLM与统一结构化切分.md PROJECT_STATUS.md
git commit -m "docs: record complete PDF parsing pipeline"
```

## Self-review

- Spec coverage: Tasks 1–2 cover page state and selective rendering; Task 3 covers PaddleOCR-VL layout; Task 4 covers mixed-page orchestration and metadata; Task 5 covers server wiring; Task 6 covers image chunks, diagnostics, tests and notes.
- Dependency direction: `documentextractor` owns neutral PDF contracts; `documentocr` depends on those contracts; no parser-to-OCR package cycle is introduced.
- Image coverage: `PDFEmbeddedImageExtractor` handles native PDF image objects; `PDFLayoutAnalyzer` handles figures inside scanned page images; these are separate because a native PDF already exposes image objects while a scanned page does not.
- Fallback coverage: missing layout service, failed layout page, failed OCR page and missing OCR configuration each have an explicit fallback or error path.
- No new model call for rich native pages: the parser only renders/analyzes classified candidates.
