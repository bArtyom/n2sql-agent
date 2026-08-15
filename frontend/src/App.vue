<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import { renderAnswerMarkdown } from "./utils/markdown";

type KnowledgeBase = { id: number; name: string; description: string };
type DocumentItem = {
  id: number;
  knowledgeBaseId: number;
  originalFilename: string;
  contentType: string;
  sizeBytes: number;
  processingStatus: "pending" | "processing" | "succeeded" | "failed" | string;
};
type DocumentPreview = {
  documentId: number;
  originalFilename?: string;
  chunks: { position: number; content: string }[];
  nextPosition: number;
  truncated: boolean;
};
type QuestionSuggestion = { id: string; text: string; category?: string };
type Source = {
  documentId: number;
  originalFilename?: string;
  position: number;
  content: string;
  contentTruncated?: boolean;
  parentContent?: string;
  parentPosition?: number;
  contextBefore?: { position: number; content: string }[];
  contextAfter?: { position: number; content: string }[];
  distance: number;
  matchType?: "vector" | "keyword" | "hybrid" | string;
  keywordScore?: number;
  fusionScore?: number;
  rerankScore?: number;
};
type ChatMessage = {
  role: "user" | "assistant";
  content: string;
  sources?: Source[];
  status?: "streaming" | "done" | "error" | "stopped";
  activity?: string;
  researchEvents?: ResearchEvent[];
  agentEvents?: AgentEvent[];
  queryRewrite?: QueryRewriteStatus;
  retrieval?: RetrievalStats;
  agentStats?: AgentRunStats;
  requestMessage?: string;
  conversationId?: number | null;
  mode?: ChatMode;
  retryable?: boolean;
  runID?: string;
  seenEventIDs?: Set<string>;
  // 只用于当前页面的展开状态，不会提交到后端。
  expandedAgentEvents?: Set<string>;
  generatedFollowUps?: QuestionSuggestion[];
  followUpLoading?: boolean;
  // 只用于当前页面：已向后端请求停止本次生成。
  stopRequested?: boolean;
};
type QueryRewriteStatus = { enabled: boolean; applied: boolean; fallback: boolean; variant_count: number };
type RetrievalStats = {
  vector_candidates: number;
  keyword_candidates: number;
  keyword_after_threshold: number;
  keyword_rejected: number;
  deduplicated_candidates: number;
  rerank_before: number;
  rerank_after: number;
  final_results: number;
  final_filtered: number;
  rerank_fallback: boolean;
};
type ResearchEvent = { type: string; round?: number; label: string; detail?: string };
type AgentEvent = {
  type: string;
  step?: number;
  label: string;
  detail?: string;
  toolCallID?: string;
  arguments?: string;
  resultSummary?: string;
  sourceKeys?: string[];
  status: "running" | "done" | "error";
};
type AgentRunStats = {
  status?: string;
  step_count?: number;
  model_calls?: number;
  tool_calls?: number;
  successful_tool_calls?: number;
  failed_tool_calls?: number;
  prompt_tokens?: number;
  completion_tokens?: number;
  embedding_tokens?: number;
  total_tokens?: number;
  duration_ms?: number;
  failure_category?: string;
};
type StoredAgentTrace = {
  run_id?: string;
  status?: string;
  stats?: AgentRunStats;
  steps?: { number?: number; kind?: string; status?: string; tool_name?: string }[];
  events?: { type?: string; step?: number; tool_call_id?: string; tool_name?: string; arguments?: string; result_summary?: string; source_keys?: string[]; status?: string }[];
};
type ChatMode = "agent" | "research" | "a2a";
type A2ATask = {
  id: string;
  status: "submitted" | "working" | "completed" | "failed" | string;
  created_at?: string;
  updated_at?: string;
  error?: string;
};
type Conversation = { id: number; knowledgeBaseId: number; title: string; isPinned: boolean; createdAt: string; updatedAt: string };
type ConversationMessage = {
  id: number;
  conversationId: number;
  role: "user" | "assistant";
  content: string;
  metadata?: { query_rewrite?: QueryRewriteStatus; retrieval?: RetrievalStats; sources?: Source[]; agent_trace?: StoredAgentTrace };
  createdAt: string;
};
type StreamPayload = {
  delta?: string;
  sources?: Source[];
  error?: string;
  answer?: string;
  content?: string;
  data?: Record<string, unknown>;
  run_id?: string;
  type?: string;
  role?: string;
  round?: number;
  step_number?: number;
  query_rewrite?: QueryRewriteStatus;
  retrieval?: RetrievalStats;
  id?: string;
};
type ModelProvider = {
  name: string;
  baseUrl: string;
  apiKeyEnvVar: string;
  chatModel: string;
  embeddingModel: string;
  rerankBaseUrl: string;
  rerankModel: string;
  enabled: boolean;
};

class APIError extends Error {
  constructor(readonly status: number, message: string, readonly payload: unknown = null) {
    super(message);
    this.name = "APIError";
  }
}

const knowledgeBases = ref<KnowledgeBase[]>([]);
const selectedKnowledgeBaseId = ref<number | null>(null);
const documents = ref<DocumentItem[]>([]);
const messages = ref<ChatMessage[]>([]);
const conversations = ref<Conversation[]>([]);
const conversationId = ref<number | null>(null);
const retrievalDetailsOpen = ref(new Set<number>());
const question = ref("");
const newKnowledgeBaseName = ref("");
const newKnowledgeBaseDescription = ref("");
const loading = ref(true);
const loadingDocuments = ref(false);
const creatingKnowledgeBase = ref(false);
const deletingKnowledgeBase = ref(false);
const uploading = ref(false);
const deletingDocumentID = ref<number | null>(null);
const streaming = ref(false);
const stopping = ref(false);
const errorMessage = ref("");
const mobileRailOpen = ref(false);
const fileInput = ref<HTMLInputElement | null>(null);
const providerSettingsOpen = ref(false);
const providerLoading = ref(false);
const providerSaving = ref(false);
const providerTesting = ref(false);
const providerMessage = ref("");
const providerMessageKind = ref<"idle" | "success" | "error">("idle");
const providerForm = ref<ModelProvider>(emptyModelProvider());
const conversationsLoading = ref(false);
const conversationCreating = ref(false);
const chatMode = ref<ChatMode>("agent");
const topK = ref(5);
const similarityThreshold = ref(0.65);
const queryRewrite = ref(false);
const keywordThreshold = ref(0.10);
const selectedDocumentIDs = ref<number[]>([]);
const selectedSource = ref<Source | null>(null);
const sourceLoading = ref(false);
const selectedDocument = ref<DocumentItem | null>(null);
const documentPreview = ref<DocumentPreview | null>(null);
const documentPreviewLoading = ref(false);
const documentPreviewPageSize = 8;
const starterQuestions = [
  "知识库里有哪些文档？",
  "请总结这套资料的主要内容。",
  "如何查看指定文档的正文？",
];
const followUpQuestions = [
  "请用一个具体例子说明刚才的结论。",
  "请指出刚才回答依据的关键原文。",
  "这个结论有哪些限制或例外？",
];
const copiedMessageIndex = ref<number | null>(null);
const copiedSourceKey = ref<string | null>(null);
let copyFeedbackTimer: number | undefined;
let documentPollTimer: number | undefined;
let a2aPollingTimer: number | undefined;

const selectedKnowledgeBase = computed(() =>
  knowledgeBases.value.find((item) => item.id === selectedKnowledgeBaseId.value) ?? null,
);
const selectedConversation = computed(() =>
  conversations.value.find((item) => item.id === conversationId.value) ?? null,
);
const readyDocumentCount = computed(
  () => documents.value.filter((item) => item.processingStatus === "succeeded").length,
);
const processingDocumentCount = computed(
  () => documents.value.filter((item) => ["pending", "processing"].includes(item.processingStatus)).length,
);

async function requestJSON<T>(url: string, options?: RequestInit): Promise<T> {
  const response = await fetch(url, options);
  const payload = await response.json().catch(() => null);
  if (!response.ok) {
    throw new APIError(response.status, payload?.error || `请求失败（${response.status}）`, payload);
  }
  return payload as T;
}

function emptyModelProvider(): ModelProvider {
  return {
    name: "",
    baseUrl: "https://api.openai.com/v1",
    apiKeyEnvVar: "OPENAI_API_KEY",
    chatModel: "",
    embeddingModel: "",
    rerankBaseUrl: "",
    rerankModel: "",
    enabled: true,
  };
}

function parseSources(value: unknown): Source[] {
  if (!Array.isArray(value)) return [];
  const parseContext = (value: unknown): { position: number; content: string }[] => {
    if (!Array.isArray(value)) return [];
    return value.filter((item): item is { position: number; content: string } => {
      if (!item || typeof item !== "object") return false;
      const chunk = item as Record<string, unknown>;
      return typeof chunk.position === "number" && typeof chunk.content === "string";
    });
  };
  return value.filter((item): item is Source => {
    if (!item || typeof item !== "object") return false;
    const source = item as Record<string, unknown>;
    if (!(typeof source.documentId === "number"
      && typeof source.position === "number"
      && typeof source.content === "string"
      && typeof source.distance === "number")) return false;
    const contextBefore = parseContext(source.contextBefore);
    const contextAfter = parseContext(source.contextAfter);
    if (contextBefore.length) (source as Record<string, unknown>).contextBefore = contextBefore;
    if (contextAfter.length) (source as Record<string, unknown>).contextAfter = contextAfter;
    return true;
  });
}

function parseAgentRunStats(value: unknown): AgentRunStats | undefined {
  if (!value || typeof value !== "object") return undefined;
  const raw = value as Record<string, unknown>;
  const boundedNumber = (candidate: unknown, maximum = 1_000_000_000): number | undefined => {
    if (typeof candidate !== "number" || !Number.isFinite(candidate) || candidate < 0) return undefined;
    return Math.min(Math.floor(candidate), maximum);
  };
  const stats: AgentRunStats = {
    status: typeof raw.status === "string" ? raw.status.slice(0, 32) : undefined,
    step_count: boundedNumber(raw.step_count),
    model_calls: boundedNumber(raw.model_calls),
    tool_calls: boundedNumber(raw.tool_calls),
    successful_tool_calls: boundedNumber(raw.successful_tool_calls),
    failed_tool_calls: boundedNumber(raw.failed_tool_calls),
    prompt_tokens: boundedNumber(raw.prompt_tokens),
    completion_tokens: boundedNumber(raw.completion_tokens),
    embedding_tokens: boundedNumber(raw.embedding_tokens),
    total_tokens: boundedNumber(raw.total_tokens),
    duration_ms: boundedNumber(raw.duration_ms, 86_400_000),
    failure_category: typeof raw.failure_category === "string" ? raw.failure_category.slice(0, 64) : undefined,
  };
  return Object.values(stats).some((item) => item !== undefined) ? stats : undefined;
}

function mergeSources(existing: Source[], incoming: Source[]): Source[] {
  const merged = new Map(existing.map((source) => [sourceKey(source), source]));
  for (const source of incoming) {
    const key = sourceKey(source);
    if (!merged.has(key)) merged.set(key, source);
  }
  return [...merged.values()];
}

function traceSources(message: ChatMessage, trace: AgentEvent): Source[] {
  const keys = new Set(trace.sourceKeys ?? []);
  if (!keys.size) return [];
  return (message.sources ?? []).filter((source) => keys.has(sourceKey(source)));
}

function sourcePreview(content: string): string {
  const normalized = content.replace(/\s+/g, " ").trim();
  return normalized.length > 118 ? `${normalized.slice(0, 118)}…` : normalized;
}

function sourceDisplayContent(source: Source): string {
  const parent = source.parentContent ? `[父块上下文]\n${source.parentContent}` : "";
  const before = (source.contextBefore ?? []).map((chunk) => `[前置上下文]\n${chunk.content}`).join("\n");
  const after = (source.contextAfter ?? []).map((chunk) => `[后置上下文]\n${chunk.content}`).join("\n");
  return [parent, before, `[命中子块]\n${source.content}`, after].filter(Boolean).join("\n");
}

function matchTypeLabel(matchType?: string): string {
  return ({ vector: "语义命中", keyword: "关键词命中", hybrid: "双路命中", document_read: "按位置读取" }[matchType || ""] || "检索命中");
}

function sourceScoreLabel(source: Source): string {
  if (source.matchType === "document_read") return "指定片段";
  if (typeof source.rerankScore === "number") return `重排 ${source.rerankScore.toFixed(2)}`;
  if (typeof source.keywordScore === "number" && source.keywordScore > 0) return `关键词 ${source.keywordScore.toFixed(2)}`;
  if (typeof source.fusionScore === "number" && source.fusionScore > 0) return `融合 ${source.fusionScore.toFixed(3)}`;
  return `距离 ${source.distance.toFixed(2)}`;
}

function renderCompletedAnswer(message: ChatMessage): string {
  return renderAnswerMarkdown(message.content);
}

function toggleRetrievalDetails(messageIndex: number) {
  const next = new Set(retrievalDetailsOpen.value);
  if (next.has(messageIndex)) next.delete(messageIndex);
  else next.add(messageIndex);
  retrievalDetailsOpen.value = next;
}

function isRetrievalDetailsOpen(messageIndex: number): boolean {
  return retrievalDetailsOpen.value.has(messageIndex);
}

async function openSource(source: Source) {
  selectedSource.value = source;
  sourceLoading.value = false;
  if (!source.contentTruncated || !selectedKnowledgeBaseId.value) return;

  const requestedKnowledgeBaseID = selectedKnowledgeBaseId.value;
  const requestedSourceKey = sourceKey(source);
  sourceLoading.value = true;
  try {
    const detail = await requestJSON<Pick<Source, "documentId" | "originalFilename" | "position" | "content" | "parentContent" | "parentPosition">>(
      `/api/knowledge-bases/${requestedKnowledgeBaseID}/documents/${source.documentId}/chunks/${source.position}`,
    );
    const fullSource: Source = { ...source, ...detail, contentTruncated: false };
    if (selectedKnowledgeBaseId.value === requestedKnowledgeBaseID
      && selectedSource.value
      && sourceKey(selectedSource.value) === requestedSourceKey) {
      selectedSource.value = fullSource;
    }
    messages.value = messages.value.map((message) => ({
      ...message,
      sources: message.sources?.map((item) => sourceKey(item) === requestedSourceKey ? fullSource : item),
    }));
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : "无法读取引用原文。";
  } finally {
    sourceLoading.value = false;
  }
}

function closeSource() {
  selectedSource.value = null;
  sourceLoading.value = false;
}

async function openDocumentPreview(item: DocumentItem) {
  if (!selectedKnowledgeBaseId.value || item.processingStatus !== "succeeded") return;
  selectedDocument.value = item;
  documentPreview.value = null;
  documentPreviewLoading.value = true;
  try {
    documentPreview.value = await requestJSON<DocumentPreview>(
      `/api/knowledge-bases/${selectedKnowledgeBaseId.value}/documents/${item.id}/preview?limit=${documentPreviewPageSize}`,
    );
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : "无法读取文档正文。";
    selectedDocument.value = null;
  } finally {
    documentPreviewLoading.value = false;
  }
}

async function loadMoreDocumentPreview() {
  const knowledgeBaseID = selectedKnowledgeBaseId.value;
  const item = selectedDocument.value;
  const current = documentPreview.value;
  if (!knowledgeBaseID || !item || !current?.truncated || documentPreviewLoading.value) return;

  const startPosition = current.nextPosition;
  documentPreviewLoading.value = true;
  try {
    const next = await requestJSON<DocumentPreview>(
      `/api/knowledge-bases/${knowledgeBaseID}/documents/${item.id}/preview?start=${startPosition}&limit=${documentPreviewPageSize}`,
    );
    if (selectedDocument.value?.id !== item.id || selectedKnowledgeBaseId.value !== knowledgeBaseID) return;
    const knownPositions = new Set(current.chunks.map((chunk) => chunk.position));
    documentPreview.value = {
      ...next,
      chunks: [...current.chunks, ...next.chunks.filter((chunk) => !knownPositions.has(chunk.position))],
    };
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : "无法继续读取文档正文。";
  } finally {
    documentPreviewLoading.value = false;
  }
}

function closeDocumentPreview() {
  selectedDocument.value = null;
  documentPreview.value = null;
  documentPreviewLoading.value = false;
}

function submitSuggestedQuestion(prompt: string) {
  if (streaming.value || !selectedKnowledgeBase.value) return;
  question.value = prompt;
  void askQuestion();
}

function shouldShowFollowUps(message: ChatMessage, index: number): boolean {
  return message.role === "assistant"
    && message.status === "done"
    && Boolean(message.content.trim())
    && index === messages.value.length - 1
    && message.mode !== "a2a";
}

function followUpQuestionContext(index: number): string {
  for (let cursor = index - 1; cursor >= 0; cursor -= 1) {
    if (messages.value[cursor]?.role === "user") return messages.value[cursor].content;
  }
  return "";
}

function followUpPrompts(message: ChatMessage): string[] {
  if (message.generatedFollowUps?.length) return message.generatedFollowUps.map((item) => item.text);
  return followUpQuestions;
}

function parseQuestionSuggestions(value: unknown): QuestionSuggestion[] {
  if (!value || typeof value !== "object") return [];
  const questions = (value as { questions?: unknown }).questions;
  if (!Array.isArray(questions)) return [];
  return questions
    .filter((item): item is QuestionSuggestion => {
      if (!item || typeof item !== "object") return false;
      const text = (item as { text?: unknown }).text;
      return typeof text === "string" && text.trim().length > 0;
    })
    .slice(0, 3)
    .map((item, index) => ({
      id: typeof item.id === "string" && item.id ? item.id : `follow-up-${index + 1}`,
      text: item.text.trim(),
      category: typeof item.category === "string" ? item.category : undefined,
    }));
}

async function generateFollowUpSuggestions(message: ChatMessage, index: number) {
  if (!selectedKnowledgeBaseId.value || message.followUpLoading || !shouldShowFollowUps(message, index)) return;
  const questionContext = message.requestMessage?.trim() || followUpQuestionContext(index);
  if (!questionContext) {
    errorMessage.value = "找不到这条回答对应的问题。";
    return;
  }
  message.followUpLoading = true;
  try {
    const payload = await requestJSON<unknown>(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/follow-up-suggestions`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ question: questionContext, answer: message.content }),
    });
    const suggestions = parseQuestionSuggestions(payload);
    if (!suggestions.length) throw new Error("模型没有返回有效的追问建议。");
    message.generatedFollowUps = suggestions;
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : "无法生成追问建议。";
  } finally {
    message.followUpLoading = false;
  }
}

function closeSourceOnEscape(event: KeyboardEvent) {
  if (event.key === "Escape") {
    closeSource();
    closeDocumentPreview();
  }
}

function sourceKey(source: Source): string {
  return `${source.documentId}:${source.position}`;
}

function toggleDocument(documentID: number) {
  if (chatMode.value !== "agent" && chatMode.value !== "research" && chatMode.value !== "a2a") return;
  selectedDocumentIDs.value = selectedDocumentIDs.value.includes(documentID)
    ? selectedDocumentIDs.value.filter((id) => id !== documentID)
    : [...selectedDocumentIDs.value, documentID];
}

async function copyText(value: string): Promise<boolean> {
  if (!value.trim() || !navigator.clipboard?.writeText) {
    errorMessage.value = "当前浏览器不支持复制，请手动选择文本。";
    return false;
  }
  try {
    await navigator.clipboard.writeText(value);
    return true;
  } catch {
    errorMessage.value = "复制失败，请检查浏览器的剪贴板权限。";
    return false;
  }
}

function showCopyFeedback(kind: "answer" | "source", key: number | string) {
  window.clearTimeout(copyFeedbackTimer);
  copiedMessageIndex.value = kind === "answer" ? key as number : null;
  copiedSourceKey.value = kind === "source" ? key as string : null;
  copyFeedbackTimer = window.setTimeout(() => {
    copiedMessageIndex.value = null;
    copiedSourceKey.value = null;
  }, 1800);
}

async function copyAnswer(message: ChatMessage, index: number) {
  if (await copyText(message.content)) showCopyFeedback("answer", index);
}

async function copySource(source: Source) {
  if (await copyText(sourceDisplayContent(source))) showCopyFeedback("source", sourceKey(source));
}

async function openProviderSettings() {
  providerSettingsOpen.value = true;
  providerLoading.value = true;
  providerMessage.value = "";
  providerMessageKind.value = "idle";
  try {
    providerForm.value = await requestJSON<ModelProvider>("/api/model-provider");
  } catch (error) {
    if (error instanceof APIError && error.status === 404) {
      const payload = error.payload;
      const apiKeyEnvVar = payload && typeof payload === "object" && "apiKeyEnvVar" in payload && typeof payload.apiKeyEnvVar === "string"
        ? payload.apiKeyEnvVar
        : emptyModelProvider().apiKeyEnvVar;
      providerForm.value = { ...emptyModelProvider(), apiKeyEnvVar };
    } else {
      providerMessageKind.value = "error";
      providerMessage.value = error instanceof Error ? error.message : "无法读取模型配置。";
    }
  } finally {
    providerLoading.value = false;
  }
}

function closeProviderSettings() {
  if (providerSaving.value || providerTesting.value) return;
  providerSettingsOpen.value = false;
}

async function saveProvider() {
  const form = providerForm.value;
  if (!form.name.trim() || !form.baseUrl.trim() || !form.chatModel.trim() || !form.embeddingModel.trim()) {
    providerMessageKind.value = "error";
    providerMessage.value = "名称、Base URL、聊天模型和嵌入模型都需要填写。";
    return;
  }
  providerSaving.value = true;
  providerMessage.value = "";
  try {
    providerForm.value = await requestJSON<ModelProvider>("/api/model-provider", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ ...form, apiKeyEnvVar: form.apiKeyEnvVar }),
    });
    providerMessageKind.value = "success";
    providerMessage.value = "模型配置已保存。";
  } catch (error) {
    providerMessageKind.value = "error";
    providerMessage.value = error instanceof Error ? error.message : "模型配置保存失败。";
  } finally {
    providerSaving.value = false;
  }
}

async function testProviderConnection() {
  providerTesting.value = true;
  providerMessage.value = "正在检查模型服务…";
  providerMessageKind.value = "idle";
  try {
    await requestJSON<{ status: string }>("/api/model-provider/connection-test", { method: "POST" });
    providerMessageKind.value = "success";
    providerMessage.value = "连接成功，模型服务可以正常响应。";
  } catch (error) {
    providerMessageKind.value = "error";
    if (error instanceof APIError && error.status === 400) {
      providerMessage.value = "没有读取到 .env 中的 API Key，请先填写 OPENAI_API_KEY。";
    } else {
      providerMessage.value = error instanceof Error ? error.message : "模型服务连接失败。";
    }
  } finally {
    providerTesting.value = false;
  }
}

async function loadKnowledgeBases() {
  loading.value = true;
  try {
    knowledgeBases.value = await requestJSON<KnowledgeBase[]>("/api/knowledge-bases");
    if (!knowledgeBases.value.some((item) => item.id === selectedKnowledgeBaseId.value)) {
      selectedKnowledgeBaseId.value = knowledgeBases.value[0]?.id ?? null;
    }
    await refreshDocuments();
  } catch (error) {
    showError(error);
  } finally {
    loading.value = false;
  }
}

async function refreshDocuments() {
  if (!selectedKnowledgeBaseId.value) {
    documents.value = [];
    return;
  }
  loadingDocuments.value = true;
  try {
    documents.value = await requestJSON<DocumentItem[]>(
      `/api/knowledge-bases/${selectedKnowledgeBaseId.value}/documents`,
    );
    scheduleDocumentPolling();
  } catch (error) {
    showError(error);
  } finally {
    loadingDocuments.value = false;
  }
}

async function loadConversation() {
  if (!selectedKnowledgeBaseId.value) {
    conversations.value = [];
    conversationId.value = null;
    messages.value = [];
    return;
  }
  conversationsLoading.value = true;
  try {
    conversations.value = await requestJSON<Conversation[]>(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/conversations`);
    const latest = conversations.value[0];
    await selectConversation(latest?.id ?? null);
  } catch (error) {
    showError(error);
  } finally {
    conversationsLoading.value = false;
  }
}

// 只刷新会话列表（标题/置顶），不重选会话、不重置当前消息视图。
async function refreshConversationList() {
  if (!selectedKnowledgeBaseId.value) return;
  try {
    conversations.value = await requestJSON<Conversation[]>(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/conversations`);
  } catch {
    // 保持现有列表，问答结果不受影响。
  }
}

async function selectConversation(id: number | null) {
  conversationId.value = id;
  retrievalDetailsOpen.value = new Set();
  messages.value = [];
  if (!id || !selectedKnowledgeBaseId.value) return;
  const stored = await requestJSON<ConversationMessage[]>(
    `/api/knowledge-bases/${selectedKnowledgeBaseId.value}/conversations/${id}/messages`,
  );
  messages.value = stored.map((message) => ({
    role: message.role,
    content: message.content,
    status: "done",
    sources: message.role === "assistant" ? parseSources(message.metadata?.sources) : undefined,
    queryRewrite: message.metadata?.query_rewrite,
    retrieval: message.metadata?.retrieval,
    agentStats: message.role === "assistant" ? parseAgentRunStats(message.metadata?.agent_trace?.stats) : undefined,
    agentEvents: message.role === "assistant" ? restoreAgentEvents(message.metadata?.agent_trace) : undefined,
    runID: message.metadata?.agent_trace?.run_id,
    mode: message.role === "assistant" ? "agent" : undefined,
  }));
}

async function createConversation() {
  if (!selectedKnowledgeBaseId.value || conversationCreating.value || streaming.value) return;
  conversationCreating.value = true;
  try {
    const created = await requestJSON<Conversation>(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/conversations`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ title: "新对话" }),
    });
    conversations.value = [created, ...conversations.value];
    await selectConversation(created.id);
  } catch (error) {
    showError(error);
  } finally {
    conversationCreating.value = false;
  }
}

async function renameConversation(item: Conversation) {
  if (!selectedKnowledgeBaseId.value || streaming.value) return;
  const title = window.prompt("新的会话名称", item.title)?.trim();
  if (!title || title === item.title) return;
  try {
    const updated = await requestJSON<Conversation>(
      `/api/knowledge-bases/${selectedKnowledgeBaseId.value}/conversations/${item.id}`,
      {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ title }),
      },
    );
    conversations.value = conversations.value.map((conversation) => conversation.id === updated.id ? updated : conversation);
  } catch (error) {
    showError(error);
  }
}

async function togglePinConversation(item: Conversation) {
  if (!selectedKnowledgeBaseId.value || streaming.value) return;
  try {
    const updated = await requestJSON<Conversation>(
      `/api/knowledge-bases/${selectedKnowledgeBaseId.value}/conversations/${item.id}`,
      {
        method: "PATCH",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ is_pinned: !item.isPinned }),
      },
    );
    conversations.value = conversations.value.map((conversation) => conversation.id === updated.id ? updated : conversation);
  } catch (error) {
    showError(error);
  }
}

async function deleteConversation(item: Conversation) {
  if (!selectedKnowledgeBaseId.value || streaming.value) return;
  if (!window.confirm(`删除“${item.title}”？这会同时删除其中的对话记录。`)) return;
  try {
    await requestJSON<void>(
      `/api/knowledge-bases/${selectedKnowledgeBaseId.value}/conversations/${item.id}`,
      { method: "DELETE" },
    );
    conversations.value = conversations.value.filter((conversation) => conversation.id !== item.id);
    if (conversationId.value === item.id) {
      await selectConversation(conversations.value[0]?.id ?? null);
    }
  } catch (error) {
    showError(error);
  }
}

async function ensureConversation(title: string): Promise<number> {
  if (conversationId.value) return conversationId.value;
  if (!selectedKnowledgeBaseId.value) throw new Error("请先选择知识库。");
  const created = await requestJSON<Conversation>(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/conversations`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ title: title.slice(0, 80) }),
  });
  conversationId.value = created.id;
  conversations.value = [created, ...conversations.value.filter((item) => item.id !== created.id)];
  return created.id;
}

function selectKnowledgeBase(id: number) {
  if (streaming.value) return;
	selectedKnowledgeBaseId.value = id;
	selectedDocumentIDs.value = [];
  closeSource();
  closeDocumentPreview();
  mobileRailOpen.value = false;
  messages.value = [];
  conversationId.value = null;
  void refreshDocuments();
  void loadConversation();
}

function clearA2APolling() {
  if (a2aPollingTimer !== undefined) {
    window.clearTimeout(a2aPollingTimer);
    a2aPollingTimer = undefined;
  }
}

async function createKnowledgeBase() {
  const name = newKnowledgeBaseName.value.trim();
  if (!name) {
    errorMessage.value = "先给知识库取一个名字。";
    return;
  }
  creatingKnowledgeBase.value = true;
  try {
    const created = await requestJSON<KnowledgeBase>("/api/knowledge-bases", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ name, description: newKnowledgeBaseDescription.value.trim() }),
    });
    knowledgeBases.value = [...knowledgeBases.value, created];
		selectedKnowledgeBaseId.value = created.id;
		selectedDocumentIDs.value = [];
    newKnowledgeBaseName.value = "";
    newKnowledgeBaseDescription.value = "";
    messages.value = [];
    closeSource();
    closeDocumentPreview();
    conversations.value = [];
    conversationId.value = null;
    documents.value = [];
    mobileRailOpen.value = false;
    await loadConversation();
  } catch (error) {
    showError(error);
  } finally {
    creatingKnowledgeBase.value = false;
  }
}

async function deleteKnowledgeBase() {
  if (!selectedKnowledgeBase.value || streaming.value || deletingKnowledgeBase.value) return;
  const target = selectedKnowledgeBase.value;
  if (!window.confirm(`删除“${target.name}”？其中的文档、索引、任务和会话都会被删除。`)) return;
  deletingKnowledgeBase.value = true;
  try {
    await requestJSON<void>(`/api/knowledge-bases/${target.id}`, { method: "DELETE" });
    selectedDocumentIDs.value = [];
    closeSource();
    closeDocumentPreview();
    messages.value = [];
    conversations.value = [];
    conversationId.value = null;
    documents.value = [];
    await loadKnowledgeBases();
    await loadConversation();
  } catch (error) {
    if (error instanceof APIError && error.status === 409) {
      errorMessage.value = "知识库中还有正在处理的文档，请稍后再删除。";
    } else {
      showError(error);
    }
  } finally {
    deletingKnowledgeBase.value = false;
  }
}

function openFilePicker() {
  fileInput.value?.click();
}

function onFileChange(event: Event) {
  const input = event.target as HTMLInputElement;
  if (input.files?.length) {
    void uploadFiles(Array.from(input.files));
    input.value = "";
  }
}

function onDrop(event: DragEvent) {
  if (event.dataTransfer?.files.length) {
    void uploadFiles(Array.from(event.dataTransfer.files));
  }
}

async function uploadFiles(files: File[]) {
  if (!selectedKnowledgeBaseId.value) {
    errorMessage.value = "请先创建或选择一个知识库。";
    return;
  }
  uploading.value = true;
  try {
    for (const file of files) {
      const body = new FormData();
      body.append("file", file);
      await requestJSON<DocumentItem>(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/documents`, {
        method: "POST",
        body,
      });
    }
    await refreshDocuments();
  } catch (error) {
    showError(error);
  } finally {
    uploading.value = false;
  }
}

async function deleteDocument(item: DocumentItem) {
  if (!selectedKnowledgeBaseId.value || streaming.value || deletingDocumentID.value !== null) return;
  if (["pending", "processing"].includes(item.processingStatus)) {
    errorMessage.value = "文档正在处理，请等待处理完成后再删除。";
    return;
  }
  if (!window.confirm(`删除“${item.originalFilename}”？这会同时删除它的索引和处理记录。`)) return;
  deletingDocumentID.value = item.id;
  try {
    await requestJSON<{ deleted: boolean }>(
      `/api/knowledge-bases/${selectedKnowledgeBaseId.value}/documents/${item.id}`,
      { method: "DELETE" },
    );
    selectedDocumentIDs.value = selectedDocumentIDs.value.filter((id) => id !== item.id);
    if (selectedSource.value?.documentId === item.id) closeSource();
    if (selectedDocument.value?.id === item.id) closeDocumentPreview();
    await refreshDocuments();
  } catch (error) {
    if (error instanceof APIError && error.status === 409) {
      errorMessage.value = "文档正在处理，请稍后再删除。";
    } else {
      showError(error);
    }
  } finally {
    deletingDocumentID.value = null;
  }
}

function scheduleDocumentPolling() {
  window.clearInterval(documentPollTimer);
  if (processingDocumentCount.value === 0) return;
  documentPollTimer = window.setInterval(() => void refreshDocuments(), 2500);
}

async function askQuestion() {
  const prompt = question.value.trim();
  if (!prompt || !selectedKnowledgeBaseId.value || streaming.value) return;
  const useResearchMode = chatMode.value === "research";
  const useA2AMode = chatMode.value === "a2a";
  let activeConversationID: number | null = null;
  try {
    if (!useResearchMode && !useA2AMode) activeConversationID = await ensureConversation(prompt);
  } catch (error) {
    showError(error);
    return;
  }
  question.value = "";
  closeSource();
  closeDocumentPreview();
  messages.value.push({ role: "user", content: prompt });
  const answer: ChatMessage = {
    role: "assistant",
    content: "",
    sources: [],
    status: "streaming",
    requestMessage: prompt,
    conversationId: activeConversationID,
    mode: chatMode.value,
    seenEventIDs: new Set(),
  };
  messages.value.push(answer);
  const answerIndex = messages.value.length - 1;
  streaming.value = true;
  try {
    if (useA2AMode) {
      await askA2ATask(prompt, answerIndex);
      return;
    }
    await streamAgentQuestion(prompt, answerIndex, activeConversationID, useResearchMode);
  } catch (error) {
    markAnswerFailure(answerIndex, error);
    showError(error);
  } finally {
    streaming.value = false;
    // 首轮问答后后端会把默认标题换成问题摘要，刷新列表让标题即时可见。
    void refreshConversationList();
  }
}

async function streamAgentQuestion(prompt: string, answerIndex: number, activeConversationID: number | null, researchMode: boolean) {
  if (!selectedKnowledgeBaseId.value) throw new Error("请先选择知识库。");
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    Accept: "text/event-stream",
  };
  if (!researchMode) headers["Idempotency-Key"] = crypto.randomUUID();
  const streamPath = researchMode ? "multi-agent-chat/stream" : "agent-chat/stream";
  const response = await fetch(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/${streamPath}`, {
    method: "POST",
    headers,
    body: JSON.stringify(researchMode
      ? { message: prompt, topK: topK.value, document_ids: selectedDocumentIDs.value, query_rewrite: queryRewrite.value, keyword_threshold: keywordThreshold.value }
      : {
          message: prompt,
          top_k: topK.value,
          similarity_threshold: similarityThreshold.value,
          document_ids: selectedDocumentIDs.value,
          query_rewrite: queryRewrite.value,
          keyword_threshold: keywordThreshold.value,
          conversation_id: activeConversationID,
        }),
  });
  if (!response.ok || !response.body) {
    const payload = await response.json().catch(() => null);
    throw new Error(payload?.error || "问答服务暂不可用");
  }
  try {
    const initialRunID = response.headers.get("X-Agent-Run-ID");
    if (initialRunID) messages.value[answerIndex].runID = initialRunID;
    await readAgentSSE(response, answerIndex, researchMode);
  } catch (error) {
    const currentAnswer = messages.value[answerIndex];
    if (!researchMode && currentAnswer?.runID) {
      try {
        await resumeAgentStream(currentAnswer, answerIndex);
      } catch {
        throw error;
      }
    } else {
      throw error;
    }
  }
  const currentAnswer = messages.value[answerIndex];
  if (currentAnswer?.status === "streaming") {
    if (!researchMode && currentAnswer.runID) {
      await resumeAgentStream(currentAnswer, answerIndex);
    }
    if (currentAnswer.status === "streaming") {
      currentAnswer.status = "error";
      currentAnswer.retryable = !researchMode;
      currentAnswer.activity = "";
      currentAnswer.content = currentAnswer.content || "流式响应提前结束，请重试。";
    }
  }
}

async function readAgentSSE(response: Response, answerIndex: number, researchMode: boolean) {
  if (!response.body) throw new Error("问答服务没有返回流式内容。");
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  while (true) {
    const { value, done } = await reader.read();
    buffer += decoder.decode(value ?? new Uint8Array(), { stream: !done });
    const blocks = buffer.split(/\r?\n\r?\n/);
    buffer = blocks.pop() ?? "";
    blocks.forEach((block) => consumeSSEBlock(block, answerIndex, researchMode));
    if (done) break;
  }
  if (buffer.trim()) consumeSSEBlock(buffer, answerIndex, researchMode);
}

async function resumeAgentStream(answer: ChatMessage, answerIndex: number) {
  if (!selectedKnowledgeBaseId.value || !answer.runID) return;
  answer.activity = "连接已断开，正在恢复 Agent 运行…";
  const response = await fetch(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/agent-runs/${encodeURIComponent(answer.runID)}/stream`, {
    method: "GET",
    headers: { Accept: "text/event-stream" },
  });
  if (!response.ok || !response.body) {
    throw new Error("无法恢复 Agent 运行，可能已超过保留时间。");
  }
  await readAgentSSE(response, answerIndex, false);
}

function markAnswerFailure(answerIndex: number, error: unknown) {
  const answer = messages.value[answerIndex];
  if (!answer) return;
  answer.status = "error";
  answer.retryable = answer.mode === "agent";
  answer.activity = "";
  answer.content = error instanceof Error ? error.message : "问答失败，请稍后重试。";
}

async function retryAnswer(answer: ChatMessage, answerIndex: number) {
  if (streaming.value || answer.mode !== "agent" || !answer.retryable || !answer.requestMessage) return;
  if (!selectedKnowledgeBaseId.value) return;
  answer.content = "";
  answer.sources = [];
  answer.agentEvents = [];
  answer.agentStats = undefined;
  answer.expandedAgentEvents = new Set();
  answer.queryRewrite = undefined;
  answer.status = "streaming";
  answer.retryable = false;
  answer.activity = "正在重新生成回答…";
  closeSource();
  closeDocumentPreview();
  streaming.value = true;
  try {
    await streamAgentQuestion(answer.requestMessage, answerIndex, answer.conversationId ?? conversationId.value, false);
  } catch (error) {
    markAnswerFailure(answerIndex, error);
    showError(error);
  } finally {
    streaming.value = false;
  }
}

// 只有标准 Agent 流式回答才有进程内 run_id 可供停止；协作研究和异步任务不显示停止按钮。
const canStopGeneration = computed(() => {
  if (!streaming.value || stopping.value) return false;
  const current = messages.value.find((message) => message.role === "assistant" && message.status === "streaming");
  return current?.mode === "agent" && Boolean(current.runID) && !current.stopRequested;
});

async function stopGeneration() {
  const current = messages.value.find((message) => message.role === "assistant" && message.status === "streaming");
  if (!current?.runID || !selectedKnowledgeBaseId.value || current.stopRequested) return;
  current.stopRequested = true;
  current.activity = "正在停止生成…";
  try {
    const response = await fetch(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/agent-runs/${encodeURIComponent(current.runID)}/stop`, {
      method: "POST",
    });
    // 运行可能刚好结束（404）：保持当前状态，让流式事件自然收尾并允许再次点击。
    if (!response.ok) current.stopRequested = false;
  } catch {
    current.stopRequested = false;
  }
}

async function askA2ATask(prompt: string, answerIndex: number) {
  if (!selectedKnowledgeBaseId.value) throw new Error("请先选择知识库。");
  const answer = messages.value[answerIndex];
  if (!answer) throw new Error("无法创建问答结果。");

  const task = await requestJSON<A2ATask>("/api/a2a/tasks", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ knowledge_base_id: selectedKnowledgeBaseId.value, message: prompt, top_k: topK.value, document_ids: selectedDocumentIDs.value, query_rewrite: queryRewrite.value }),
  });
  answer.activity = "任务已提交，等待后台 Agent 领取…";

  try {
    while (true) {
      const current = await requestJSON<A2ATask>(`/api/a2a/tasks/${encodeURIComponent(task.id)}`);
      if (current.status === "submitted") {
        answer.activity = "任务已排队，等待后台 Agent 领取…";
      } else if (current.status === "working") {
        answer.activity = "后台 Agent 正在检索并组织答案…";
      } else if (current.status === "failed") {
        throw new Error(current.error || "后台 Agent 任务失败。");
      } else if (current.status === "completed") {
        const result = await requestJSON<{ answer?: string; sources?: Source[]; query_rewrite?: QueryRewriteStatus; retrieval?: RetrievalStats }>(
          `/api/a2a/tasks/${encodeURIComponent(task.id)}/result`,
        );
        answer.content = typeof result.answer === "string" ? result.answer : "";
        answer.sources = mergeSources(answer.sources ?? [], parseSources(result.sources));
        if (result.query_rewrite) answer.queryRewrite = result.query_rewrite;
        if (result.retrieval) answer.retrieval = result.retrieval;
        answer.activity = "后台任务已完成";
        answer.status = "done";
        return;
      } else {
        throw new Error(`后台任务返回了未知状态：${current.status}`);
      }

      await new Promise<void>((resolve) => {
        a2aPollingTimer = window.setTimeout(() => {
          a2aPollingTimer = undefined;
          resolve();
        }, 1000);
      });
    }
  } finally {
    clearA2APolling();
  }
}

function recordResearchEvent(answer: ChatMessage, type: string, label: string, detail = "", round?: number) {
  answer.researchEvents ??= [];
  answer.researchEvents.push({ type, label, detail: detail.slice(0, 140), round });
  if (answer.researchEvents.length > 12) answer.researchEvents.shift();
}

const agentToolLabels: Record<string, string> = {
  knowledge_search: "检索知识库",
  document_list: "查看文档列表",
  document_info: "查看文档状态",
  document_read: "读取文档正文",
};

function displayAgentToolName(toolName: string) {
  return agentToolLabels[toolName.trim()] || "调用知识库工具";
}

function agentToolActivity(toolName: string) {
  switch (toolName.trim()) {
    case "knowledge_search":
      return "正在检索知识库…";
    case "document_list":
      return "正在读取文档列表…";
    case "document_info":
      return "正在读取文档状态…";
    case "document_read":
      return "正在读取文档正文…";
    default:
      return "正在调用工具…";
  }
}

function formatAgentDuration(durationMS?: number) {
  if (durationMS === undefined) return "";
  if (durationMS < 1000) return `${durationMS}ms`;
  return `${(durationMS / 1000).toFixed(durationMS >= 10_000 ? 0 : 1)}s`;
}

function agentTraceSummary(message: ChatMessage) {
  const stats = message.agentStats;
  if (!stats) return `${message.agentEvents?.length ?? 0} EVENTS`;
  const parts: string[] = [];
  if (stats.step_count !== undefined) parts.push(`${stats.step_count} 步`);
  if (stats.tool_calls !== undefined) parts.push(`${stats.tool_calls} 工具`);
  if (stats.total_tokens !== undefined) parts.push(`${stats.total_tokens} tokens`);
  const duration = formatAgentDuration(stats.duration_ms);
  if (duration) parts.push(duration);
  return parts.length ? parts.join(" · ") : `${message.agentEvents?.length ?? 0} EVENTS`;
}

function recordAgentEvent(
  answer: ChatMessage,
  type: string,
  label: string,
  detail = "",
  step?: number,
  status: AgentEvent["status"] = "done",
  extra: Pick<AgentEvent, "toolCallID" | "arguments" | "resultSummary"> = {},
) {
  answer.agentEvents ??= [];
  answer.agentEvents.push({ type, step, label, detail: detail.slice(0, 140), status, ...extra });
  if (answer.agentEvents.length > 12) answer.agentEvents.shift();
}

function restoreAgentEvents(trace?: StoredAgentTrace): AgentEvent[] {
  if (!trace) return [];
  const storedEvents = Array.isArray(trace.events) ? trace.events : [];
  if (storedEvents.length > 0) {
    const events: AgentEvent[] = [{ type: "run_started", label: "Agent 开始运行", detail: "从历史记录恢复", status: "done" }];
    for (const item of storedEvents.slice(0, 32)) {
      if (!item || typeof item !== "object") continue;
      const status = item.status === "failed" ? "error" : item.status === "running" ? "running" : "done";
      events.push({
        type: item.type || "tool_call",
        step: item.step,
        label: item.tool_name ? displayAgentToolName(item.tool_name) : "调用知识库工具",
        detail: item.result_summary || "工具调用完成",
        toolCallID: item.tool_call_id,
        arguments: item.arguments,
        resultSummary: item.result_summary,
        sourceKeys: Array.isArray(item.source_keys)
          ? item.source_keys.filter((key): key is string => typeof key === "string").slice(0, 20)
          : undefined,
        status,
      });
    }
    const terminalStatus = trace.status === "failed" || trace.status === "canceled" ? "error" : "done";
    events.push({
      type: trace.status === "canceled" ? "run_canceled" : trace.status === "failed" ? "run_failed" : "run_finished",
      label: trace.status === "canceled" ? "Agent 已取消" : trace.status === "failed" ? "Agent 运行失败" : "Agent 完成",
      detail: "历史运行记录",
      status: terminalStatus,
    });
    return events;
  }
  if (!Array.isArray(trace.steps) || trace.steps.length === 0) return [];
  const events: AgentEvent[] = [{ type: "run_started", label: "Agent 开始运行", detail: "从历史记录恢复", status: "done" }];
  for (const step of trace.steps.slice(0, 32)) {
    if (!step || typeof step !== "object") continue;
    const kind = typeof step.kind === "string" ? step.kind : "step";
    const status = step.status === "failed" ? "error" : step.status === "running" || step.status === "pending" ? "running" : "done";
    const label = kind === "tool_call"
      ? (typeof step.tool_name === "string" && step.tool_name ? displayAgentToolName(step.tool_name) : "调用知识库工具")
      : kind === "final_answer"
        ? "生成最终答案"
        : "模型决策";
    const detail = typeof step.tool_name === "string" && step.tool_name ? displayAgentToolName(step.tool_name) : "历史运行步骤";
    events.push({ type: kind, step: step.number, label, detail, status });
  }
  const terminalStatus = trace.status === "failed" || trace.status === "canceled" ? "error" : "done";
  events.push({
    type: trace.status === "canceled" ? "run_canceled" : trace.status === "failed" ? "run_failed" : "run_finished",
    label: trace.status === "canceled" ? "Agent 已取消" : trace.status === "failed" ? "Agent 运行失败" : "Agent 完成",
    detail: "历史运行记录",
    status: terminalStatus,
  });
  return events;
}

function finishLastAgentToolEvent(answer: ChatMessage, detail: string, status: AgentEvent["status"] = "done", toolCallID = "", sourceKeys: string[] = []) {
  const events = answer.agentEvents ?? [];
  const latest = [...events].reverse().find((item) => item.type === "tool_called"
    && item.status === "running"
    && (!toolCallID || item.toolCallID === toolCallID));
  if (latest) {
    latest.label = "知识库检索完成";
    latest.detail = detail.slice(0, 140);
    latest.resultSummary = detail.slice(0, 140);
    latest.sourceKeys = sourceKeys.slice(0, 20);
    latest.status = status;
  }
}

function agentTraceKey(trace: AgentEvent, index: number) {
  return trace.toolCallID || `${trace.type}-${trace.step ?? "unknown"}-${index}`;
}

function isAgentTraceExpandable(trace: AgentEvent) {
  return trace.type === "tool_called" || trace.type === "tool_call" || Boolean(trace.arguments || trace.resultSummary);
}

function isAgentTraceExpanded(message: ChatMessage, trace: AgentEvent, index: number) {
  return message.expandedAgentEvents?.has(agentTraceKey(trace, index)) ?? false;
}

function toggleAgentTrace(message: ChatMessage, trace: AgentEvent, index: number) {
  if (!isAgentTraceExpandable(trace)) return;
  const expanded = new Set(message.expandedAgentEvents ?? []);
  const key = agentTraceKey(trace, index);
  if (expanded.has(key)) expanded.delete(key);
  else expanded.add(key);
  message.expandedAgentEvents = expanded;
}

function consumeSSEBlock(block: string, answerIndex: number, researchMode = false) {
  const answer = messages.value[answerIndex];
  if (!answer) return;
  let event = "message";
  const dataLines: string[] = [];
  for (const line of block.split(/\r?\n/)) {
    if (line.startsWith("event:")) event = line.slice(6).trim();
    if (line.startsWith("data:")) dataLines.push(line.slice(5).trimStart());
  }
  if (!dataLines.length) return;
  try {
    const payload = JSON.parse(dataLines.join("\n")) as StreamPayload;
    if (payload.id && answer.seenEventIDs) {
      if (answer.seenEventIDs.has(payload.id)) return;
      answer.seenEventIDs.add(payload.id);
    }
    if (payload.run_id) answer.runID = payload.run_id;
    const eventData = payload.data && typeof payload.data === "object" ? payload.data : {};
    const dataString = (key: string) => typeof eventData[key] === "string" ? eventData[key] as string : "";

    switch (event) {
      case "sources":
        answer.sources = mergeSources(answer.sources ?? [], parseSources(payload.sources));
        if (payload.retrieval) answer.retrieval = payload.retrieval;
        if (payload.query_rewrite) {
          answer.queryRewrite = payload.query_rewrite;
          answer.activity = payload.query_rewrite.fallback ? "改写不可用，已使用原问题检索…" : "检索完成，正在组织答案…";
        }
        break;
      case "delta":
        answer.content += payload.delta ?? "";
        break;
      case "run_started":
        if (!researchMode) recordAgentEvent(answer, event, "Agent 开始运行", "正在分析问题", payload.step_number, "running");
        answer.activity = researchMode ? "协作研究已启动…" : "正在理解问题…";
        break;
      case "research_started":
        recordResearchEvent(answer, event, "研究员开始工作", "正在规划检索", payload.round);
        answer.activity = "研究员正在查找资料…";
        break;
      case "research_tool_called":
        recordResearchEvent(answer, event, "发起知识库检索", dataString("tool_name") || "knowledge_search", payload.round);
        answer.activity = "研究员正在查找资料…";
        break;
      case "research_tool_finished": {
        const sources = parseSources(eventData.sources);
        answer.sources = mergeSources(answer.sources ?? [], sources);
        const rewrite = eventData.query_rewrite;
        if (rewrite && typeof rewrite === "object" && (rewrite as Record<string, unknown>).enabled === true) {
          answer.queryRewrite = rewrite as QueryRewriteStatus;
        }
        recordResearchEvent(answer, event, "检索完成", sources.length ? `${sources.length} 条引用` : "继续评估当前证据", payload.round);
        answer.activity = "研究员正在判断是否需要继续…";
        break;
      }
      case "research_summary":
        recordResearchEvent(answer, event, "研究员形成摘要", dataString("content") || payload.content || "已形成阶段性结论", payload.round);
        answer.activity = "研究员已完成检索，准备交给回答者…";
        break;
      case "research_finished": {
        const sources = parseSources(eventData.sources);
        answer.sources = mergeSources(answer.sources ?? [], sources);
        recordResearchEvent(answer, event, "研究员完成", eventData.no_relevant_results ? "资料不足，安全结束" : `${answer.sources?.length ?? 0} 条引用`, payload.round);
        answer.activity = eventData.no_relevant_results ? "资料不足，正在结束…" : "回答者正在组织答案…";
        break;
      }
      case "answerer_started":
        recordResearchEvent(answer, event, "回答者开始组织答案", "基于研究摘要和原始片段", payload.round);
        answer.activity = "回答者正在组织答案…";
        break;
      case "answerer_finished":
        recordResearchEvent(answer, event, "回答者完成", "最终答案即将返回", payload.round);
        break;
      case "answerer_skipped":
        recordResearchEvent(answer, event, "回答者跳过", "知识库没有足够证据", payload.round);
        answer.activity = "资料不足，已安全结束。";
        break;
      case "tool_called":
        if (!researchMode) {
          const toolName = dataString("tool_name") || "knowledge_search";
          recordAgentEvent(answer, event, displayAgentToolName(toolName), "", payload.step_number, "running", {
            toolCallID: dataString("tool_call_id"),
            arguments: dataString("arguments"),
          });
        }
        answer.activity = agentToolActivity(dataString("tool_name"));
        break;
      case "tool_finished":
        if (!researchMode) {
          const sources = parseSources(eventData.sources);
          answer.sources = mergeSources(answer.sources ?? [], sources);
          finishLastAgentToolEvent(
            answer,
            dataString("result_summary") || (eventData.no_relevant_results === true ? "没有找到足够相关资料" : `${sources.length || "已"} 条结果已返回`),
            "done",
            dataString("tool_call_id"),
            sources.map(sourceKey),
          );
        }
        if (Object.prototype.hasOwnProperty.call(eventData, "sources")) {
          answer.sources = mergeSources(answer.sources ?? [], parseSources(eventData.sources));
        }
        answer.activity = "资料查找完成，正在组织答案…";
        break;
      case "message_delta":
        if (!researchMode && !answer.agentEvents?.some((item) => item.type === "answer_started")) {
          recordAgentEvent(answer, "answer_started", "开始生成答案", "模型正在根据检索结果组织回答", payload.step_number, "running");
        }
        answer.content += dataString("content") || payload.content || "";
        answer.activity = "正在组织答案…";
        break;
      case "run_finished":
        if (!researchMode) {
          for (const agentEvent of answer.agentEvents ?? []) {
            if (agentEvent.status === "running") agentEvent.status = "done";
          }
          const answerEvent = answer.agentEvents?.find((item) => item.type === "answer_started");
          if (answerEvent) {
            answerEvent.detail = "最终回答已生成";
            answerEvent.status = "done";
          }
          recordAgentEvent(answer, event, "Agent 完成", "本轮运行成功", payload.step_number, "done");
        }
        if (researchMode) answer.sources = mergeSources(answer.sources ?? [], parseSources(eventData.sources));
        if (researchMode && eventData.retrieval && typeof eventData.retrieval === "object") {
          answer.retrieval = eventData.retrieval as RetrievalStats;
        }
        if (payload.query_rewrite) answer.queryRewrite = payload.query_rewrite;
        const runStats = eventData.stats;
        if (!answer.queryRewrite && runStats && typeof runStats === "object") {
          const rewrite = (runStats as Record<string, unknown>).query_rewrite;
          if (rewrite && typeof rewrite === "object" && (rewrite as Record<string, unknown>).enabled === true) {
            answer.queryRewrite = rewrite as QueryRewriteStatus;
          }
        }
        if (runStats && typeof runStats === "object") {
          answer.agentStats = parseAgentRunStats(runStats);
          const retrieval = (runStats as Record<string, unknown>).retrieval;
          if (retrieval && typeof retrieval === "object") answer.retrieval = retrieval as RetrievalStats;
        }
        answer.content ||= dataString("answer") || payload.answer || "";
        answer.activity = "";
        answer.status = "done";
        answer.retryable = false;
        break;
      case "conversation_saved":
        answer.activity = "已保存到会话";
        break;
      case "conversation_replayed": {
        const replayed = eventData.response;
        if (replayed && typeof replayed === "object") {
          const replayedAnswer = replayed as Record<string, unknown>;
          answer.content = typeof replayedAnswer.answer === "string" ? replayedAnswer.answer : answer.content;
          answer.sources = mergeSources(answer.sources ?? [], parseSources(replayedAnswer.sources));
          answer.runID = typeof replayedAnswer.run_id === "string" ? replayedAnswer.run_id : answer.runID;
          const replayedStats = replayedAnswer.stats;
          if (replayedStats && typeof replayedStats === "object") {
            answer.agentStats = parseAgentRunStats(replayedStats);
            const stats = replayedStats as Record<string, unknown>;
            if (stats.query_rewrite && typeof stats.query_rewrite === "object") answer.queryRewrite = stats.query_rewrite as QueryRewriteStatus;
            if (stats.retrieval && typeof stats.retrieval === "object") answer.retrieval = stats.retrieval as RetrievalStats;
          }
          answer.agentEvents = restoreAgentEvents({
            run_id: answer.runID,
            status: typeof replayedAnswer.status === "string" ? replayedAnswer.status : "succeeded",
            stats: parseAgentRunStats(replayedStats),
            steps: Array.isArray(replayedAnswer.steps) ? replayedAnswer.steps as StoredAgentTrace["steps"] : [],
            events: Array.isArray(replayedAnswer.trace) ? replayedAnswer.trace as StoredAgentTrace["events"] : [],
          });
        }
        answer.activity = "已恢复之前的回答";
        answer.status = "done";
        answer.retryable = false;
        break;
      }
      case "conversation_save_failed":
        answer.status = "error";
        answer.retryable = false;
        answer.activity = "";
        answer.content += answer.content ? "\n\n（回答已生成，但保存会话失败）" : "回答已生成，但保存会话失败。";
        break;
      case "run_failed":
      case "error":
        // 用户主动停止后引擎已发 run_canceled；后续通用 error 事件不应覆盖已停止状态。
        if (answer.status === "stopped") break;
        if (!researchMode) {
          for (const agentEvent of answer.agentEvents ?? []) {
            if (agentEvent.status === "running") agentEvent.status = "error";
          }
          recordAgentEvent(answer, event, "Agent 运行失败", dataString("error") || payload.error || "执行失败", payload.step_number, "error");
        }
        answer.status = "error";
        answer.retryable = true;
        answer.activity = "";
        answer.content = dataString("error") || payload.error || "问答失败。";
        break;
      case "run_canceled":
        if (!researchMode) {
          for (const agentEvent of answer.agentEvents ?? []) {
            if (agentEvent.status === "running") agentEvent.status = "error";
          }
          recordAgentEvent(answer, event, "Agent 已停止", "用户停止生成", payload.step_number, "error");
        }
        answer.status = "stopped";
        answer.retryable = false;
        answer.activity = "";
        if (!answer.content) answer.content = "已停止生成。";
        break;
      case "done":
        answer.activity = "";
        answer.status = "done";
        break;
    }
  } catch {
    answer.status = "error";
    answer.activity = "";
    answer.content = "收到无法解析的流式响应。";
  }
}

function showError(error: unknown) {
  errorMessage.value = error instanceof Error ? error.message : "操作失败，请稍后重试。";
}

function statusLabel(status: string) {
  return ({ pending: "排队中", processing: "整理中", succeeded: "可检索", failed: "处理失败" }[status] || status);
}

// 会话列表按「置顶 + 日期」分组展示；后端已按 is_pinned DESC, updated_at DESC 排序，
// 所以这里只需保持遍历顺序分组，无需再排序。
const conversationGroups = computed(() => {
  const pinned: Conversation[] = [];
  const byDate = new Map<string, Conversation[]>();
  for (const item of conversations.value) {
    if (item.isPinned) {
      pinned.push(item);
    } else {
      const label = conversationDateLabel(item.updatedAt);
      const group = byDate.get(label);
      if (group) group.push(item);
      else byDate.set(label, [item]);
    }
  }
  const groups: { label: string; items: Conversation[] }[] = [];
  if (pinned.length) groups.push({ label: "置顶", items: pinned });
  for (const [label, items] of byDate) groups.push({ label, items });
  return groups;
});

function conversationDateLabel(value: string): string {
  const date = new Date(value);
  const today = new Date();
  const startOfToday = new Date(today.getFullYear(), today.getMonth(), today.getDate()).getTime();
  const startOfDay = new Date(date.getFullYear(), date.getMonth(), date.getDate()).getTime();
  const dayDiff = Math.round((startOfToday - startOfDay) / 86400000);
  if (dayDiff <= 0) return "今天";
  if (dayDiff === 1) return "昨天";
  if (dayDiff < 7) return `${dayDiff} 天前`;
  return date.toLocaleDateString("zh-CN");
}

function formatBytes(size: number) {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
  return `${(size / 1024 / 1024).toFixed(1)} MB`;
}

onMounted(() => {
  window.addEventListener("keydown", closeSourceOnEscape);
  void loadKnowledgeBases().then(() => loadConversation());
});
onUnmounted(() => {
  window.clearInterval(documentPollTimer);
  clearA2APolling();
  window.clearTimeout(copyFeedbackTimer);
  window.removeEventListener("keydown", closeSourceOnEscape);
});
</script>

<template>
  <div class="app-shell">
    <div v-if="mobileRailOpen" class="rail-scrim" @click="mobileRailOpen = false" />
    <aside class="rail" :class="{ 'rail--open': mobileRailOpen }">
      <div class="rail-head">
        <a class="brand" href="/" aria-label="返回文库首页">
          <span class="brand-mark">文</span>
          <span>文库</span>
        </a>
        <button class="icon-button rail-close" aria-label="关闭导航" @click="mobileRailOpen = false">×</button>
      </div>
      <div class="rail-kicker">LOCAL KNOWLEDGE DESK</div>
      <div class="rail-title-row">
        <span>你的资料库</span>
        <span class="count-badge">{{ knowledgeBases.length }}</span>
      </div>
      <nav class="kb-list" aria-label="知识库列表">
        <button
          v-for="item in knowledgeBases"
          :key="item.id"
          class="kb-item"
          :class="{ 'kb-item--active': item.id === selectedKnowledgeBaseId }"
          @click="selectKnowledgeBase(item.id)"
        >
          <span class="kb-dot" />
          <span class="kb-name">{{ item.name }}</span>
          <span class="kb-arrow">↗</span>
        </button>
        <p v-if="!loading && !knowledgeBases.length" class="empty-rail">还没有知识库，先在下面创建一个。</p>
      </nav>
      <form class="create-form" @submit.prevent="createKnowledgeBase">
        <label for="kb-name">新建知识库</label>
        <input id="kb-name" v-model="newKnowledgeBaseName" placeholder="例如：产品手册" maxlength="200" />
        <input v-model="newKnowledgeBaseDescription" placeholder="一句话描述（可选）" maxlength="2000" />
        <button class="create-button" type="submit" :disabled="creatingKnowledgeBase">
          <span>{{ creatingKnowledgeBase ? "创建中…" : "+ 创建资料库" }}</span>
          <span>⌘</span>
        </button>
      </form>
      <div class="rail-footer">
        <span class="live-dot" />
        <span>本地 API 在线</span>
        <span class="footer-version">v0.1</span>
      </div>
      <button class="provider-link" type="button" @click="openProviderSettings">
        <span class="provider-link-icon">⌁</span>
        <span>模型服务设置</span>
        <span class="provider-link-arrow">↗</span>
      </button>
    </aside>

    <main class="workspace">
      <header class="workspace-header">
        <div class="mobile-header-left">
          <button class="icon-button menu-button" aria-label="打开导航" @click="mobileRailOpen = true">☰</button>
          <span class="mobile-brand">文库</span>
        </div>
        <div class="breadcrumb">
          <span>工作台</span><span class="breadcrumb-slash">/</span><strong>{{ selectedKnowledgeBase?.name || "选择知识库" }}</strong>
        </div>
        <div class="header-actions">
          <button class="quiet-button" :disabled="loadingDocuments || deletingKnowledgeBase" @click="refreshDocuments">↻ 刷新</button>
          <button v-if="selectedKnowledgeBase" class="danger-button" :disabled="streaming || deletingKnowledgeBase" @click="deleteKnowledgeBase">{{ deletingKnowledgeBase ? "删除中…" : "删除资料库" }}</button>
          <span class="header-status"><span class="live-dot" /> 系统就绪</span>
        </div>
      </header>

      <div v-if="errorMessage" class="notice" role="alert">
        <span>{{ errorMessage }}</span><button aria-label="关闭提示" @click="errorMessage = ''">×</button>
      </div>

      <section v-if="selectedKnowledgeBase" class="workspace-intro">
        <div>
          <p class="eyebrow">KNOWLEDGE BASE · {{ String(selectedKnowledgeBase.id).padStart(2, "0") }}</p>
          <h1>{{ selectedKnowledgeBase.name }}<span class="title-mark">·</span></h1>
          <p class="intro-copy">{{ selectedKnowledgeBase.description || "把原始资料整理成可以被追问、引用和复用的知识。" }}</p>
        </div>
        <div class="stats-strip" aria-label="知识库统计">
          <div><strong>{{ documents.length }}</strong><span>全部文档</span></div>
          <div><strong>{{ readyDocumentCount }}</strong><span>已可检索</span></div>
          <div><strong>{{ processingDocumentCount }}</strong><span>处理中</span></div>
        </div>
      </section>

      <section v-if="selectedKnowledgeBase" class="desk-grid">
        <div class="documents-panel panel-card">
          <div class="panel-heading">
            <div><span class="section-index">01</span><h2>资料架</h2></div>
            <span class="panel-meta">{{ documents.length }} FILES</span>
          </div>
          <label class="upload-zone" :class="{ 'upload-zone--busy': uploading }" for="document-upload" role="button" tabindex="0" @dragover.prevent @drop.prevent="onDrop" @keydown.enter.prevent="openFilePicker" @keydown.space.prevent="openFilePicker">
            <input id="document-upload" ref="fileInput" class="visually-hidden" type="file" accept=".md,.txt,.pdf" multiple @change="onFileChange" />
            <span class="upload-icon">↑</span>
            <div><strong>{{ uploading ? "正在接收资料…" : "拖入文件，或点击上传" }}</strong><span>支持 Markdown、TXT、文本型 PDF · 单文件不超过 10 MB</span></div>
            <span class="upload-shortcut">⌘ U</span>
          </label>
          <div v-if="loadingDocuments" class="list-loading">正在读取资料架…</div>
          <div v-else-if="!documents.length" class="empty-documents">
            <span class="empty-glyph">∷</span><strong>这里还很安静</strong><p>上传第一份资料，Worker 会自动提取、切分并建立向量索引。</p>
          </div>
          <ul v-else class="document-list">
            <li v-for="document in documents" :key="document.id" class="document-row">
              <label v-if="document.processingStatus === 'succeeded'" class="document-select" :title="selectedDocumentIDs.includes(document.id) ? '取消限定此文档' : '只检索此文档'">
                <input type="checkbox" :checked="selectedDocumentIDs.includes(document.id)" :disabled="streaming" @change="toggleDocument(document.id)" />
                <span class="visually-hidden">选择 {{ document.originalFilename }}</span>
              </label>
              <span class="file-icon">{{ document.contentType === "application/pdf" ? "PDF" : "TXT" }}</span>
              <div class="document-copy"><strong>{{ document.originalFilename }}</strong><span>{{ formatBytes(document.sizeBytes) }} · #{{ document.id }}</span></div>
              <span class="processing-status" :class="`processing-status--${document.processingStatus}`"><i />{{ statusLabel(document.processingStatus) }}</span>
              <button
                v-if="document.processingStatus === 'succeeded'"
                class="document-preview-button"
                type="button"
                :disabled="streaming || documentPreviewLoading"
                title="查看正文片段"
                @click="openDocumentPreview(document)"
              >查看</button>
              <button
                class="document-delete"
                type="button"
                :disabled="streaming || deletingDocumentID !== null || ['pending', 'processing'].includes(document.processingStatus)"
                :title="['pending', 'processing'].includes(document.processingStatus) ? '文档处理完成后才能删除' : '删除文档'"
                @click="deleteDocument(document)"
              >{{ deletingDocumentID === document.id ? "删除中…" : "删除" }}</button>
            </li>
          </ul>
        </div>

        <div class="chat-panel panel-card">
          <div class="panel-heading">
            <div><span class="section-index">02</span><h2>问答台</h2></div>
            <span class="panel-meta">{{ chatMode === "a2a" ? "ASYNC TASK" : "STREAMING" }}</span>
          </div>
          <div class="chat-intro">
            <span class="chat-spark">✦</span>
            <div class="chat-intro-copy"><strong>从资料里找答案</strong><p>回答会标记它引用的原始段落。</p></div>
            <div class="chat-mode-switch" role="group" aria-label="问答模式">
              <button type="button" :class="{ 'chat-mode--active': chatMode === 'agent' }" :aria-pressed="chatMode === 'agent'" :disabled="streaming" @click="chatMode = 'agent'">标准 Agent</button>
              <button type="button" :class="{ 'chat-mode--active': chatMode === 'research' }" :aria-pressed="chatMode === 'research'" :disabled="streaming" @click="chatMode = 'research'">协作研究</button>
              <button type="button" :class="{ 'chat-mode--active': chatMode === 'a2a' }" :aria-pressed="chatMode === 'a2a'" :disabled="streaming" @click="chatMode = 'a2a'">异步任务</button>
            </div>
          </div>
          <p v-if="chatMode === 'research'" class="chat-mode-note">协作研究会根据证据多轮检索；本次结果只在当前页面展示，不写入会话历史。</p>
          <p v-else-if="chatMode === 'a2a'" class="chat-mode-note">异步任务会交给后台 Agent 执行；页面会自动跟踪任务状态，完成后展示答案和引用，不写入会话历史。</p>
          <div class="retrieval-controls">
            <div><strong>检索范围</strong><span>{{ selectedDocumentIDs.length ? `${chatMode === 'research' ? '协作研究' : chatMode === 'a2a' ? '异步任务' : '标准 Agent'} 仅检索 ${selectedDocumentIDs.length} 份已选文档；` : "当前模式检索整个知识库；" }}控制召回数量和证据相关度</span></div>
            <label>召回片段数<select v-model.number="topK" :disabled="streaming"><option v-for="value in [3, 5, 8, 12, 20]" :key="value" :value="value">{{ value }} 条</option></select></label>
            <label class="rewrite-control"><input v-model="queryRewrite" type="checkbox" :disabled="streaming" /> 多查询改写</label>
            <label v-if="chatMode !== 'a2a'" class="threshold-control">关键词下限
              <input v-model.number="keywordThreshold" type="range" min="0" max="0.80" step="0.05" :disabled="streaming">
              <output>{{ keywordThreshold.toFixed(2) }}</output>
            </label>
            <label v-if="chatMode === 'agent'" class="threshold-control">距离上限
              <input v-model.number="similarityThreshold" type="range" min="0.30" max="0.90" step="0.05" :disabled="streaming">
              <output>{{ similarityThreshold.toFixed(2) }}</output>
            </label>
          </div>
          <template v-if="chatMode !== 'a2a'">
            <div class="conversation-bar">
              <div class="conversation-current">
                <span class="conversation-caption">当前会话</span>
                <strong>{{ selectedConversation?.title || "还没有会话" }}</strong>
              </div>
              <button class="conversation-new" type="button" :disabled="conversationCreating || streaming || !selectedKnowledgeBase" @click="createConversation">
                {{ conversationCreating ? "创建中…" : "+ 新对话" }}
              </button>
            </div>
            <div v-if="conversations.length" class="conversation-list" aria-label="会话列表">
                <template v-for="group in conversationGroups" :key="group.label">
                  <div class="conversation-group-label">{{ group.label }}</div>
                  <div
                    v-for="item in group.items"
                    :key="item.id"
                    class="conversation-item"
                    :class="{ 'conversation-item--active': item.id === conversationId, 'conversation-item--pinned': item.isPinned }"
                    @click="selectConversation(item.id)"
                  >
                    <span class="conversation-item-title">{{ item.isPinned ? "📌 " : "" }}{{ item.title }}</span>
                    <small>{{ new Date(item.updatedAt).toLocaleDateString("zh-CN") }}</small>
                    <div class="conversation-actions">
                      <button type="button" :aria-label="item.isPinned ? '取消置顶' : '置顶会话'" :disabled="conversationsLoading || streaming" @click.stop="togglePinConversation(item)">{{ item.isPinned ? "取消置顶" : "置顶" }}</button>
                      <button type="button" aria-label="重命名会话" :disabled="conversationsLoading || streaming" @click.stop="renameConversation(item)">改名</button>
                      <button type="button" aria-label="删除会话" :disabled="conversationsLoading || streaming" @click.stop="deleteConversation(item)">删</button>
                    </div>
                  </div>
                </template>
            </div>
          </template>
          <div class="messages" aria-live="polite">
            <div v-if="!messages.length" class="chat-empty">
              <span>“</span>
              <p>问一个关于这套资料的问题，<br />让线索自己浮上来。</p>
              <div class="starter-questions" aria-label="起步问题">
                <button v-for="prompt in starterQuestions" :key="prompt" type="button" @click="submitSuggestedQuestion(prompt)">{{ prompt }}</button>
              </div>
            </div>
            <article v-for="(message, index) in messages" :key="index" class="message" :class="`message--${message.role}`">
              <div class="message-label">{{ message.role === "user" ? "你" : "文库助手" }}</div>
              <div v-if="message.role === 'assistant' && message.status === 'streaming' && message.activity" class="message-activity">
                <span class="message-activity-dot" />
                <span>{{ message.activity }}</span>
              </div>
              <div v-if="message.role === 'assistant' && message.agentEvents?.length" class="agent-trace">
                <div class="agent-trace-head"><span>Agent 运行轨迹</span><small>{{ agentTraceSummary(message) }}</small></div>
                <div v-for="(trace, traceIndex) in message.agentEvents" :key="agentTraceKey(trace, traceIndex)" class="agent-trace-row" :class="`agent-trace-row--${trace.status}`">
                  <span class="agent-trace-marker">{{ trace.status === 'done' ? '✓' : trace.status === 'error' ? '!' : '·' }}</span>
                  <button
                    v-if="isAgentTraceExpandable(trace)"
                    type="button"
                    class="agent-trace-toggle"
                    :aria-expanded="isAgentTraceExpanded(message, trace, traceIndex)"
                    :aria-label="isAgentTraceExpanded(message, trace, traceIndex) ? '收起工具详情' : '展开工具详情'"
                    @click="toggleAgentTrace(message, trace, traceIndex)"
                  >
                    <span aria-hidden="true">›</span>
                  </button>
                  <span v-else class="agent-trace-toggle-placeholder" aria-hidden="true" />
                  <span class="agent-trace-label">{{ trace.label }}</span>
                  <span class="agent-trace-copy">
                    <small v-if="trace.detail">{{ trace.detail }}</small>
                    <template v-if="isAgentTraceExpanded(message, trace, traceIndex)">
                      <small v-if="trace.arguments">参数：{{ trace.arguments }}</small>
                      <small v-if="trace.resultSummary && trace.resultSummary !== trace.detail">结果：{{ trace.resultSummary }}</small>
                      <div v-if="traceSources(message, trace).length" class="agent-trace-sources">
                        <span>本次引用：</span>
                        <button v-for="source in traceSources(message, trace)" :key="sourceKey(source)" type="button" @click="openSource(source)">
                          {{ source.originalFilename || "未命名文档" }} · 第 {{ source.position + 1 }} 段
                        </button>
                      </div>
                    </template>
                  </span>
                </div>
              </div>
              <div v-if="message.role === 'assistant' && message.researchEvents?.length" class="research-trace">
                <div class="research-trace-head"><span>研究轨迹</span><small>{{ message.researchEvents.length }} EVENTS</small></div>
                <div v-for="trace in message.researchEvents" :key="`${trace.type}-${trace.round}-${trace.label}-${trace.detail}`" class="research-trace-row">
                  <span class="research-trace-dot" />
                  <span class="research-trace-label">{{ trace.label }}</span>
                  <small v-if="trace.detail">{{ trace.detail }}</small>
                </div>
              </div>
              <div class="message-bubble" :class="{ 'message-bubble--error': message.status === 'error' }">
                <span v-if="message.role === 'assistant' && !message.content && message.status === 'streaming'" class="typing"><i /><i /><i /></span>
                <span v-else-if="message.role === 'user' || message.status === 'streaming' || message.status === 'error'">{{ message.content }}</span>
                <div v-else class="markdown-content" v-html="renderCompletedAnswer(message)" />
              </div>
              <div v-if="message.role === 'assistant' && message.queryRewrite" class="query-rewrite-status">
                <span class="query-rewrite-status-dot" />
                <span v-if="message.queryRewrite.fallback">多查询改写不可用，已自动使用原问题检索</span>
                <span v-else-if="message.queryRewrite.applied">已使用 {{ message.queryRewrite.variant_count }} 个改写查询扩大召回</span>
                <span v-else>已启用多查询改写，本次使用原问题检索</span>
              </div>
              <div v-if="message.role === 'assistant' && message.retrieval" class="retrieval-inspector">
                <div class="retrieval-stats" aria-label="检索统计">
                  <span>向量 {{ message.retrieval.vector_candidates }}</span>
                  <span>关键词 {{ message.retrieval.keyword_candidates }}→{{ message.retrieval.keyword_after_threshold }}</span>
                  <span>去重 {{ message.retrieval.deduplicated_candidates }}</span>
                  <span v-if="message.retrieval.rerank_before">重排 {{ message.retrieval.rerank_before }}→{{ message.retrieval.rerank_after }}</span>
                  <span>最终 {{ message.retrieval.final_filtered || message.retrieval.final_results }}</span>
                  <span v-if="message.retrieval.rerank_fallback">Rerank 已降级</span>
                  <button class="retrieval-details-toggle" type="button" @click="toggleRetrievalDetails(index)">{{ isRetrievalDetailsOpen(index) ? "收起详情" : "检索详情" }}</button>
                </div>
                <div v-if="isRetrievalDetailsOpen(index)" class="retrieval-details">
                  <div class="retrieval-detail-grid">
                    <div><span>向量召回</span><strong>{{ message.retrieval.vector_candidates }}</strong></div>
                    <div><span>关键词召回</span><strong>{{ message.retrieval.keyword_candidates }}</strong></div>
                    <div><span>关键词过阈值</span><strong>{{ message.retrieval.keyword_after_threshold }}</strong></div>
                    <div><span>关键词被过滤</span><strong>{{ message.retrieval.keyword_rejected }}</strong></div>
                    <div><span>去重后候选</span><strong>{{ message.retrieval.deduplicated_candidates }}</strong></div>
                    <div><span>Rerank 前</span><strong>{{ message.retrieval.rerank_before || "未执行" }}</strong></div>
                    <div><span>Rerank 后</span><strong>{{ message.retrieval.rerank_after || "未执行" }}</strong></div>
                    <div><span>距离过滤后</span><strong>{{ message.retrieval.final_filtered }}</strong></div>
                  </div>
                  <p v-if="message.retrieval.rerank_fallback">Rerank 服务本轮失败，系统保留了混合召回结果继续回答。</p>
                  <p v-else-if="!message.retrieval.rerank_before">本轮未配置 Rerank，直接使用混合召回结果。</p>
                  <p v-else>最终回答只使用通过相似度距离阈值的片段。</p>
                </div>
              </div>
              <div v-if="message.role === 'assistant' && message.content && message.status !== 'streaming'" class="message-actions">
                <button type="button" @click="copyAnswer(message, index)">{{ copiedMessageIndex === index ? "已复制回答" : "复制回答" }}</button>
                <button v-if="message.retryable && message.mode === 'agent'" type="button" @click="retryAnswer(message, index)">重新生成</button>
              </div>
              <div v-if="shouldShowFollowUps(message, index)" class="follow-up-suggestions" aria-label="继续追问">
                <span>继续追问</span>
                <button v-for="prompt in followUpPrompts(message)" :key="prompt" type="button" @click="submitSuggestedQuestion(prompt)">{{ prompt }}</button>
                <button v-if="!message.generatedFollowUps?.length" class="follow-up-generate" type="button" :disabled="message.followUpLoading" @click="generateFollowUpSuggestions(message, index)">{{ message.followUpLoading ? "生成中…" : "生成更具体的追问" }}</button>
              </div>
              <div v-if="message.role === 'assistant' && message.sources?.length" class="sources">
                <div class="sources-heading"><span class="sources-label">引用 {{ message.sources.length }}</span><span>点击查看原文</span></div>
                <div class="source-list">
                  <button v-for="(source, sourceIndex) in message.sources" :key="`${source.documentId}-${source.position}`" class="source-card" type="button" @click="openSource(source)">
                    <span class="source-card-index">{{ String(sourceIndex + 1).padStart(2, "0") }}</span>
                    <span class="source-card-body"><strong>{{ source.originalFilename || "未命名文档" }} <em :class="`source-match source-match--${source.matchType || 'unknown'}`">{{ matchTypeLabel(source.matchType) }}</em></strong><small>第 {{ source.position + 1 }} 段 · {{ sourceScoreLabel(source) }}</small><span>{{ sourcePreview(source.content) }}</span></span>
                    <span class="source-card-arrow">↗</span>
                  </button>
                </div>
              </div>
            </article>
          </div>
          <form class="composer" @submit.prevent="askQuestion">
            <textarea v-model="question" rows="2" :disabled="!selectedKnowledgeBase || streaming" placeholder="问问这套资料…" @keydown.enter.exact.prevent="askQuestion" />
            <div class="composer-footer">
              <span>Enter 发送 · Shift + Enter 换行</span>
              <button v-if="canStopGeneration" class="stop-button" type="button" aria-label="停止生成" @click="stopGeneration">停止生成</button>
              <button class="send-button" type="submit" :disabled="!question.trim() || streaming || !selectedKnowledgeBase" aria-label="发送问题">↗</button>
            </div>
          </form>
        </div>
      </section>

      <section v-else class="empty-workspace">
        <div class="empty-workspace-mark">文</div><p class="eyebrow">FIRST STEP</p><h1>先建立一间资料室。</h1><p>知识库是文档、处理任务和后续问答的边界。创建后，就可以把第一份资料放进来。</p>
      </section>
    </main>

    <div v-if="providerSettingsOpen" class="settings-backdrop" @click.self="closeProviderSettings">
      <section class="settings-panel" role="dialog" aria-modal="true" aria-labelledby="provider-settings-title">
        <header class="settings-header">
          <div>
            <p class="eyebrow">MODEL CONTROL ROOM</p>
            <h2 id="provider-settings-title">模型服务设置</h2>
          </div>
          <button class="settings-close" type="button" aria-label="关闭模型服务设置" @click="closeProviderSettings">×</button>
        </header>
        <div v-if="providerLoading" class="settings-loading">正在读取当前配置…</div>
        <form v-else class="settings-form" @submit.prevent="saveProvider">
          <p class="settings-intro">告诉文库该去哪里寻找聊天和嵌入能力。密钥始终留在后端环境变量里。</p>
          <label>服务名称<input v-model="providerForm.name" type="text" placeholder="例如：OpenAI" maxlength="120" required /></label>
          <label>Base URL<input v-model="providerForm.baseUrl" type="url" placeholder="https://api.openai.com/v1" required /></label>
          <div class="settings-two-column">
            <label>聊天模型<input v-model="providerForm.chatModel" type="text" placeholder="例如：gpt-4o-mini" required /></label>
            <label>嵌入模型<input v-model="providerForm.embeddingModel" type="text" placeholder="例如：text-embedding-3-small" required /></label>
          </div>
          <div class="settings-two-column">
            <label>Rerank Base URL（可选）<input v-model="providerForm.rerankBaseUrl" type="url" placeholder="例如：https://…/compatible-api/v1" /></label>
            <label>Rerank 模型（可选）<input v-model="providerForm.rerankModel" type="text" placeholder="例如：qwen3-rerank" /></label>
          </div>
          <div class="secret-note"><span class="secret-note-mark">⌘</span><div><strong>API Key 不在这里输入</strong><p>后端只读取 `.env` 中的 <code>{{ providerForm.apiKeyEnvVar }}</code>，页面不会显示或保存密钥。</p></div></div>
          <p v-if="providerMessage" class="provider-message" :class="`provider-message--${providerMessageKind}`" role="status">{{ providerMessage }}</p>
          <div class="settings-actions"><button class="settings-secondary" type="button" :disabled="providerTesting || providerSaving" @click="testProviderConnection">{{ providerTesting ? "测试中…" : "测试连接" }}</button><button class="settings-primary" type="submit" :disabled="providerSaving || providerTesting">{{ providerSaving ? "保存中…" : "保存配置" }}</button></div>
        </form>
      </section>
    </div>

    <div v-if="selectedDocument" class="source-backdrop" @click.self="closeDocumentPreview">
      <aside class="source-panel document-preview-panel" role="dialog" aria-modal="true" aria-labelledby="document-preview-title">
        <header class="source-panel-header">
          <div><p class="eyebrow">DOCUMENT {{ String(selectedDocument.id).padStart(2, "0") }}</p><h2 id="document-preview-title">正文预览</h2></div>
          <button class="settings-close" type="button" aria-label="关闭文档预览" @click="closeDocumentPreview">×</button>
        </header>
        <div class="source-panel-meta">
          <strong>{{ selectedDocument.originalFilename }}</strong>
          <span>{{ formatBytes(selectedDocument.sizeBytes) }} · 只显示已处理的正文片段</span>
        </div>
        <div class="document-preview-content">
          <p v-if="documentPreviewLoading">正在读取文档正文…</p>
          <template v-else-if="documentPreview">
            <article v-for="chunk in documentPreview.chunks" :key="chunk.position" class="document-preview-chunk">
              <span>第 {{ chunk.position + 1 }} 段</span>
              <p>{{ chunk.content }}</p>
            </article>
            <div v-if="documentPreview.truncated" class="document-preview-actions">
              <p class="document-preview-note">当前已显示到第 {{ documentPreview.nextPosition }} 段。</p>
              <button class="source-copy-button" type="button" :disabled="documentPreviewLoading || streaming" @click="loadMoreDocumentPreview">{{ documentPreviewLoading ? "读取中…" : "继续读取" }}</button>
            </div>
          </template>
        </div>
        <p class="source-panel-note">正文来自当前知识库已处理的 chunk，读取范围受权限和字节上限约束。</p>
      </aside>
    </div>

    <div v-if="selectedSource" class="source-backdrop" @click.self="closeSource">
      <aside class="source-panel" role="dialog" aria-modal="true" aria-labelledby="source-panel-title">
        <header class="source-panel-header">
          <div><p class="eyebrow">SOURCE {{ String(selectedSource.position + 1).padStart(2, "0") }}</p><h2 id="source-panel-title">原文片段</h2></div>
          <button class="settings-close" type="button" aria-label="关闭引用详情" @click="closeSource">×</button>
        </header>
        <div class="source-panel-meta">
          <strong>{{ selectedSource.originalFilename || "未命名文档" }}</strong>
          <span>第 {{ selectedSource.position + 1 }} 段 · {{ matchTypeLabel(selectedSource.matchType) }} · {{ sourceScoreLabel(selectedSource) }}</span>
        </div>
        <div class="source-panel-content"><p v-if="sourceLoading">正在读取引用原文…</p><template v-else><p>{{ sourceDisplayContent(selectedSource) }}</p><small v-if="selectedSource.contentTruncated">历史记录只保存了引用片段预览，原文已被截断。</small></template></div>
        <div class="source-panel-actions"><button class="source-copy-button" type="button" @click="copySource(selectedSource)">{{ copiedSourceKey === sourceKey(selectedSource) ? "已复制原文" : "复制原文" }}</button></div>
        <p class="source-panel-note">这段内容来自知识库检索结果，仅作为回答依据展示，不会被当作操作指令执行。</p>
      </aside>
    </div>
  </div>
</template>
