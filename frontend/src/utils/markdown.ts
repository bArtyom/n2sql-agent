import DOMPurify from "dompurify";
import { marked } from "marked";

// Inline citation markers emitted by the Agent model, e.g.
// <kb doc_id="12" pos="3"/>. Render only completed answers; streaming text
// stays plain until the response is complete, so an unfinished code fence
// cannot disturb the UI.
const KB_TAG_PATTERN = /<kb\s+doc_id="(\d+)"\s+pos="(\d+)"\s*\/?>/gi;

// Replace <kb/> markers with a safe placeholder before marked/DOMPurify run,
// because DOMPurify would drop the unknown <kb> element. The span keeps its
// data attributes so the app can wire up a click handler later.
export function renderAnswerMarkdown(markdown: string): string {
  const withRefs = markdown.replace(
    KB_TAG_PATTERN,
    (_match, docId: string, pos: string) =>
      `<span class="kb-ref" data-doc-id="${docId}" data-pos="${pos}" title="查看引用原文">引用</span>`,
  );

  const html = marked.parse(withRefs, {
    async: false,
    breaks: true,
    gfm: true,
  }) as string;

  const sanitized = DOMPurify.sanitize(html, {
    FORBID_ATTR: ["style"],
    FORBID_TAGS: ["embed", "form", "iframe", "img", "input", "object", "script", "style", "svg"],
  });

  // Links are useful in an answer, but must not replace the current app tab
  // or keep an opener reference to it.
  const container = document.createElement("div");
  container.innerHTML = sanitized;
  for (const link of container.querySelectorAll<HTMLAnchorElement>("a")) {
    link.target = "_blank";
    link.rel = "noopener noreferrer";
  }
  return container.innerHTML;
}

// Render plain text while streaming: keep the citation marker readable
// without showing the raw tag syntax.
export function plainAnswerText(content: string): string {
  return content.replace(KB_TAG_PATTERN, "〔引用〕");
}
