import DOMPurify from "dompurify";
import { marked } from "marked";

// Render only completed answers. Streaming text is kept plain until the
// response is complete, so an unfinished code fence cannot disturb the UI.
export function renderAnswerMarkdown(markdown: string): string {
  const html = marked.parse(markdown, {
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
