<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";
import { plainAnswerText, renderAnswerMarkdown } from "./utils/markdown";

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
  chunks: { position: number; content: string; headingPath?: string }[];
  nextPosition: number;
  truncated: boolean;
  diagnostics?: {
    strategy: string;
    chunkCount: number;
    headingCount: number;
    protectedBlockCount: number;
    totalRunes: number;
    minChunkRunes: number;
    maxChunkRunes: number;
    shortChunkCount: number;
    oversizeChunkCount: number;
  };
};
type QuestionSuggestion = { id: string; text: string; category?: string };
type Source = {
  documentId: number;
  originalFilename?: string;
  position: number;
  content: string;
  headingPath?: string;
  contentTruncated?: boolean;
  parentContent?: string;
  parentPosition?: number;
  contextBefore?: { position: number; content: string }[];
  contextAfter?: { position: number; content: string }[];
  distance: number;
  matchType?: "vector" | "keyword" | "hybrid" | string;
  keywordScore?: number;
  headingScore?: number;
  fusionScore?: number;
  rerankScore?: number;
};
type ChatMessage = {
  id?: number;
  role: "user" | "assistant";
  content: string;
  sources?: Source[];
  status?: "streaming" | "done" | "error" | "stopped";
  activity?: string;
  reasoningContent?: string;
  reasoningExpanded?: boolean;
  agentEvents?: AgentEvent[];
  queryRewrite?: QueryRewriteStatus;
  retrieval?: RetrievalStats;
  agentStats?: AgentRunStats;
  requestMessage?: string;
  conversationId?: number | null;
  mode?: "agent";
  retryable?: boolean;
  runID?: string;
  childRunTree?: AgentRunTree;
  lastEventID?: string;
  streamGap?: boolean;
  seenEventIDs?: Set<string>;
  // 只用于当前页面的展开状态，不会提交到后端。
  expandedAgentEvents?: Set<string>;
  generatedFollowUps?: QuestionSuggestion[];
  followUpLoading?: boolean;
  // 只用于当前页面：已向后端请求停止本次生成。
  stopRequested?: boolean;
  feedback?: -1 | 1 | null;
  feedbackSubmitting?: boolean;
  attachments?: ChatAttachmentPreview[];
  summaryTask?: { knowledgeBaseId: number; documentId: number; taskId?: string; status: string };
};
type ThinkingMode = "fast" | "standard" | "deep";
type ChatAttachmentPreview = {
  filename: string;
  contentType: string;
  dataBase64?: string;
  dataURL?: string;
  sizeBytes: number;
};
type ChatAttachmentDraft = ChatAttachmentPreview & { dataBase64: string };
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
type AgentDocument = { id: number; original_filename?: string; content_type?: string; size_bytes?: number; processing_status?: string };
type AgentEvent = {
  type: string;
  step?: number;
  label: string;
  detail?: string;
  toolCallID?: string;
  arguments?: string;
  resultSummary?: string;
  sourceKeys?: string[];
  documents?: AgentDocument[];
  documentInfo?: AgentDocument | null;
  pending?: boolean;
  taskID?: string;
  childRunID?: string;
  childEventType?: string;
  status: "running" | "done" | "error";
};
type AgentRunTree = {
  run_id: string;
  parent_run_id?: string;
  run_kind: string;
  status: string;
  attempt_count?: number;
  error?: string;
  response?: { answer?: string };
  children?: AgentRunTree[];
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
type Conversation = { id: number; knowledgeBaseId: number; title: string; isPinned: boolean; chatModel?: string; createdAt: string; updatedAt: string };
type ConversationPage = { items: Conversation[]; has_more: boolean; offset: number; limit: number };
type FeedbackStats = { total: number; positive: number; negative: number; positiveRate: number };
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
  chatModels?: string[];
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

// 未完成的 Agent 运行记录：刷新页面后据此尝试从 Hub 重放剩余事件。
// 只在标准 Agent 模式使用；run 结束或恢复失败后清除。
type PendingAgentRun = { knowledgeBaseId: number; runID: string; conversationId?: number | null };
type PendingApproval = { knowledgeBaseId: number; runID: string; toolName: string; arguments: string };
const PENDING_RUN_KEY = "n2sql-pending-agent-run";

function savePendingRun(run: PendingAgentRun) {
  try {
    localStorage.setItem(PENDING_RUN_KEY, JSON.stringify(run));
  } catch {
    // 隐私模式等场景忽略持久化失败，不影响问答。
  }
}

function readPendingRun(): PendingAgentRun | null {
  try {
    const raw = localStorage.getItem(PENDING_RUN_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as PendingAgentRun;
    if (typeof parsed?.runID !== "string" || typeof parsed.knowledgeBaseId !== "number") return null;
    return parsed;
  } catch {
    return null;
  }
}

function clearPendingRun() {
  try {
    localStorage.removeItem(PENDING_RUN_KEY);
  } catch {
    // 忽略清除失败。
  }
}

const knowledgeBases = ref<KnowledgeBase[]>([]);
const selectedKnowledgeBaseId = ref<number | null>(null);
const documents = ref<DocumentItem[]>([]);
const messages = ref<ChatMessage[]>([]);
const conversations = ref<Conversation[]>([]);
const feedbackStats = ref<FeedbackStats>({ total: 0, positive: 0, negative: 0, positiveRate: 0 });
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
const reprocessingDocumentID = ref<number | null>(null);
const streaming = ref(false);
const stopping = ref(false);
const errorMessage = ref("");
const mobileRailOpen = ref(false);
const fileInput = ref<HTMLInputElement | null>(null);
const chatAttachmentInput = ref<HTMLInputElement | null>(null);
const providerSettingsOpen = ref(false);
const providerLoading = ref(false);
const providerSaving = ref(false);
const providerTesting = ref(false);
const providerMessage = ref("");
const providerMessageKind = ref<"idle" | "success" | "error">("idle");
const providerForm = ref<ModelProvider>(emptyModelProvider());
const providerChatModelsText = ref("");
const chatModelOptions = ref<string[]>([]);
const defaultChatModel = ref("");
const chatModelOptionsLoading = ref(false);
const chatModelOptionsError = ref("");
const selectedChatModel = ref("");
const thinkingMode = ref<ThinkingMode>("standard");
const chatAttachments = ref<ChatAttachmentDraft[]>([]);
const conversationsLoading = ref(false);
const conversationSearch = ref("");
const conversationHasMore = ref(false);
const conversationOffset = ref(0);
const conversationPageSize = 30;
const selectedConversationIDs = ref<number[]>([]);
const conversationBatchBusy = ref(false);
let conversationSearchTimer: number | undefined;
const conversationCreating = ref(false);
const openConversationMenuId = ref<number | null>(null);
const copiedConversationID = ref<number | null>(null);
// 历史消息分页游标：记录当前会话、已加载的最早消息 id 和是否还有更早的。
const messageCursor = ref<{ conversationId: number | null; beforeId: number | null; hasMore: boolean }>({ conversationId: null, beforeId: null, hasMore: false });
const loadingOlderMessages = ref(false);
const pendingApproval = ref<PendingApproval | null>(null);
const approvalBusy = ref(false);
const topK = ref(5);
const similarityThreshold = ref(0.65);
const queryRewrite = ref(false);
const keywordThreshold = ref(0.10);
const selectedDocumentIDs = ref<number[]>([]);
const documentMentionOpen = ref(false);
const documentMentionQuery = ref("");
const selectedSource = ref<Source | null>(null);
const sourceLoading = ref(false);
const selectedDocument = ref<DocumentItem | null>(null);
const documentPreview = ref<DocumentPreview | null>(null);
const documentPreviewLoading = ref(false);
const documentPreviewPageSize = 8;
const starterQuestions = computed(() => {
  const readyDocuments = documents.value.filter((document) => document.processingStatus === "succeeded");
  const selectedDocuments = readyDocuments.filter((document) => selectedDocumentIDs.value.includes(document.id));
  const scope = selectedDocuments.length === 1 ? `《${selectedDocuments[0].originalFilename}》` : "这些资料";
  const questions = selectedDocuments.length
    ? [
        `请总结${scope}的主要内容。`,
        `请指出${scope}中最值得关注的关键结论。`,
        `请根据${scope}给出一个具体应用示例。`,
      ]
    : [
        "知识库里有哪些文档？",
        readyDocuments.length ? `请总结${readyDocuments.slice(0, 2).map((document) => `《${document.originalFilename}》`).join("、")}的主要内容。` : "请总结这套资料的主要内容。",
        "这些资料之间有哪些共同主题或关联？",
      ];
  return questions;
});
const followUpQuestions = [
  "请用一个具体例子说明刚才的结论。",
  "请指出刚才回答依据的关键原文。",
  "这个结论有哪些限制或例外？",
];
const copiedMessageIndex = ref<number | null>(null);
const copiedSourceKey = ref<string | null>(null);
let copyFeedbackTimer: number | undefined;
let documentPollTimer: number | undefined;
const summaryPollingTimers = new Map<number, number>();

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
    chatModels: [],
    // Kept only for compatibility with the existing backend/database field.
    // Embedding is configured through LOCAL_EMBEDDING_* in .env.
    embeddingModel: "local",
    rerankBaseUrl: "",
    rerankModel: "",
    enabled: true,
  };
}

function normalizedChatModelOptions(provider: ModelProvider): string[] {
  const candidates = [provider.chatModel, ...(provider.chatModels ?? [])];
  const seen = new Set<string>();
  return candidates
    .filter((candidate): candidate is string => typeof candidate === "string")
    .map((candidate) => candidate.trim())
    .filter((candidate) => candidate.length > 0 && candidate.length <= 200)
    .filter((candidate) => {
      if (seen.has(candidate)) return false;
      seen.add(candidate);
      return true;
    })
    .slice(0, 16);
}

function applyModelProvider(provider: ModelProvider) {
  const options = normalizedChatModelOptions(provider);
  defaultChatModel.value = typeof provider.chatModel === "string" ? provider.chatModel.trim() : "";
  chatModelOptions.value = options;
  const conversationModel = selectedConversation.value?.chatModel?.trim();
  if (conversationModel && options.includes(conversationModel)) {
    selectedChatModel.value = conversationModel;
  } else if (!selectedChatModel.value || !options.includes(selectedChatModel.value)) {
    selectedChatModel.value = defaultChatModel.value && options.includes(defaultChatModel.value) ? defaultChatModel.value : (options[0] ?? "");
  }
}

function setProviderForm(provider: ModelProvider) {
  providerForm.value = provider;
  providerChatModelsText.value = normalizedChatModelOptions(provider).join(", ");
  applyModelProvider(provider);
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

// 正文内引用标记的事件委托：<kb/> 渲染成 .kb-ref 后，点击由 messages
// 容器统一捕获，按 data 属性找到对应来源并打开原文抽屉。
function onMessagesClick(event: MouseEvent) {
  const target = event.target as HTMLElement;
  const ref = target.closest<HTMLElement>(".kb-ref");
  if (!ref) return;
  const docId = ref.getAttribute("data-doc-id");
  const pos = ref.getAttribute("data-pos");
  if (!docId || !pos) return;
  const source = findSourceByRef(docId, pos);
  if (source) {
    void openSource(source);
  } else {
    errorMessage.value = "找不到这条引用对应的资料。";
  }
}

function findSourceByRef(docId: string, pos: string): Source | undefined {
  for (const message of messages.value) {
    for (const source of message.sources ?? []) {
      if (String(source.documentId) === docId && String(source.position) === pos) return source;
    }
  }
  return undefined;
}

async function openSource(source: Source) {
  selectedSource.value = source;
  sourceLoading.value = false;
  if (!source.contentTruncated || !selectedKnowledgeBaseId.value) return;

  const requestedKnowledgeBaseID = selectedKnowledgeBaseId.value;
  const requestedSourceKey = sourceKey(source);
  sourceLoading.value = true;
  try {
      const detail = await requestJSON<Pick<Source, "documentId" | "originalFilename" | "position" | "content" | "headingPath" | "parentContent" | "parentPosition">>(
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
    && message.mode === "agent";
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
    closeProviderSettings();
  }
}

function sourceKey(source: Source): string {
  return `${source.documentId}:${source.position}`;
}

function toggleDocument(documentID: number) {
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
    const provider = await requestJSON<ModelProvider>("/api/model-provider");
    setProviderForm(provider);
  } catch (error) {
    if (error instanceof APIError && error.status === 404) {
      const payload = error.payload;
      const apiKeyEnvVar = payload && typeof payload === "object" && "apiKeyEnvVar" in payload && typeof payload.apiKeyEnvVar === "string"
        ? payload.apiKeyEnvVar
        : emptyModelProvider().apiKeyEnvVar;
      setProviderForm({ ...emptyModelProvider(), apiKeyEnvVar });
    } else {
      providerMessageKind.value = "error";
      providerMessage.value = error instanceof Error ? error.message : "无法读取模型配置。";
    }
  } finally {
    providerLoading.value = false;
  }
}

async function loadChatModelOptions() {
  chatModelOptionsLoading.value = true;
  chatModelOptionsError.value = "";
  try {
    const provider = await requestJSON<ModelProvider>("/api/model-provider");
    applyModelProvider(provider);
  } catch (error) {
    if (error instanceof APIError && error.status === 404) {
      chatModelOptions.value = [];
      defaultChatModel.value = "";
      selectedChatModel.value = "";
    } else {
      chatModelOptionsError.value = error instanceof Error ? error.message : "无法读取聊天模型列表。";
    }
  } finally {
    chatModelOptionsLoading.value = false;
  }
}

function closeProviderSettings() {
  if (providerSaving.value || providerTesting.value) return;
  providerSettingsOpen.value = false;
}

async function saveProvider() {
  const form = providerForm.value;
  if (!form.name.trim() || !form.baseUrl.trim() || !form.chatModel.trim()) {
    providerMessageKind.value = "error";
    providerMessage.value = "名称、Base URL 和聊天模型都需要填写。";
    return;
  }
  providerSaving.value = true;
  providerMessage.value = "";
  try {
    providerForm.value = await requestJSON<ModelProvider>("/api/model-provider", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        ...form,
        // The API still accepts the legacy field, but users no longer need to
        // configure it because local embedding is selected by the backend.
        embeddingModel: form.embeddingModel.trim() || "local",
        apiKeyEnvVar: form.apiKeyEnvVar,
        chatModels: providerChatModelsText.value.split(",").map((item) => item.trim()).filter(Boolean),
      }),
    });
    setProviderForm(providerForm.value);
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
    await refreshFeedbackStats();
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
    conversationOffset.value = 0;
    const page = await fetchConversationList(0);
    conversations.value = page.items;
    conversationHasMore.value = page.has_more;
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
    conversationOffset.value = 0;
    const page = await fetchConversationList(0);
    conversations.value = page.items;
    conversationHasMore.value = page.has_more;
  } catch {
    // 保持现有列表，问答结果不受影响。
  }
}

async function selectConversation(id: number | null) {
  conversationId.value = id;
  const conversationModel = selectedConversation.value?.chatModel?.trim();
  selectedChatModel.value = conversationModel && chatModelOptions.value.includes(conversationModel)
    ? conversationModel
    : defaultChatModel.value;
  closeConversationMenu();
  retrievalDetailsOpen.value = new Set();
  messages.value = [];
  messageCursor.value = { conversationId: id, beforeId: null, hasMore: false };
  if (!id || !selectedKnowledgeBaseId.value) return;
  await loadMessagePage(id, null);
}

async function fetchConversationList(offset = conversationOffset.value): Promise<ConversationPage> {
  if (!selectedKnowledgeBaseId.value) return { items: [], has_more: false, offset, limit: conversationPageSize };
  const query = conversationSearch.value.trim();
  const params = new URLSearchParams({ limit: String(conversationPageSize), offset: String(offset) });
  if (query) params.set("q", query);
  return requestJSON<ConversationPage>(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/conversations?${params.toString()}`);
}

async function loadMoreConversations() {
  if (!conversationHasMore.value || conversationsLoading.value) return;
  conversationsLoading.value = true;
  try {
    const nextOffset = conversationOffset.value + conversationPageSize;
    const page = await fetchConversationList(nextOffset);
    conversations.value = [...conversations.value, ...page.items];
    conversationOffset.value = nextOffset;
    conversationHasMore.value = page.has_more;
  } finally {
    conversationsLoading.value = false;
  }
}

function toggleConversationSelection(id: number) {
  selectedConversationIDs.value = selectedConversationIDs.value.includes(id)
    ? selectedConversationIDs.value.filter((item) => item !== id)
    : [...selectedConversationIDs.value, id];
}

function clearConversationSelection() { selectedConversationIDs.value = []; }

async function deleteSelectedConversations() {
  if (!selectedKnowledgeBaseId.value || !selectedConversationIDs.value.length || conversationBatchBusy.value) return;
  if (!window.confirm(`删除选中的 ${selectedConversationIDs.value.length} 个会话？`)) return;
  conversationBatchBusy.value = true;
  try {
    await requestJSON<void>(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/conversations/batch-delete`, {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ids: selectedConversationIDs.value }),
    });
    const deletingCurrent = conversationId.value !== null && selectedConversationIDs.value.includes(conversationId.value);
    clearConversationSelection();
    await refreshConversationList();
    if (deletingCurrent) await selectConversation(conversations.value[0]?.id ?? null);
  } catch (error) { showError(error); } finally { conversationBatchBusy.value = false; }
}

async function pinSelectedConversations(pinned: boolean) {
  if (!selectedKnowledgeBaseId.value || !selectedConversationIDs.value.length || conversationBatchBusy.value) return;
  conversationBatchBusy.value = true;
  try {
    await requestJSON<void>(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/conversations/batch-pin`, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ ids: selectedConversationIDs.value, pinned }) });
    clearConversationSelection();
    await refreshConversationList();
  } catch (error) { showError(error); } finally { conversationBatchBusy.value = false; }
}

function exportSelectedConversations() {
  const selected = conversations.value.filter((item) => selectedConversationIDs.value.includes(item.id));
  if (!selected.length) return;
  const markdown = selected.map((item) => `# ${item.title}\n\n- 会话 ID：${item.id}\n- 更新时间：${item.updatedAt}`).join("\n\n");
  const blob = new Blob([markdown], { type: "text/markdown;charset=utf-8" });
  const link = document.createElement("a"); link.href = URL.createObjectURL(blob); link.download = "conversations.md"; link.click(); URL.revokeObjectURL(link.href);
}

function scheduleConversationSearch() {
  if (conversationSearchTimer !== undefined) window.clearTimeout(conversationSearchTimer);
  conversationSearchTimer = window.setTimeout(() => {
    conversationSearchTimer = undefined;
    void refreshConversationList();
  }, 250);
}


async function persistConversationChatModel(id: number, model: string): Promise<Conversation> {
  if (!selectedKnowledgeBaseId.value) throw new Error("请先选择知识库。");
  return requestJSON<Conversation>(
    `/api/knowledge-bases/${selectedKnowledgeBaseId.value}/conversations/${id}`,
    {
      method: "PATCH",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ chat_model: model }),
    },
  );
}

async function updateConversationChatModel() {
  if (!selectedKnowledgeBaseId.value || !conversationId.value || streaming.value) return;
  const model = selectedChatModel.value;
  if (!chatModelOptions.value.includes(model)) {
    selectedChatModel.value = selectedConversation.value?.chatModel?.trim() || defaultChatModel.value;
    errorMessage.value = "请选择服务端已配置的聊天模型。";
    return;
  }
  const previous = selectedConversation.value?.chatModel?.trim() || defaultChatModel.value;
  try {
    const updated = await persistConversationChatModel(conversationId.value, model);
    conversations.value = conversations.value.map((item) => item.id === updated.id ? updated : item);
    selectedChatModel.value = updated.chatModel?.trim() || defaultChatModel.value;
  } catch (error) {
    selectedChatModel.value = previous;
    showError(error);
  }
}

// 把后端消息记录转换为页面上的聊天消息。
function toChatMessage(message: ConversationMessage): ChatMessage {
  return {
    id: message.id,
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
  };
}

const documentMentionCandidates = computed(() => {
  const query = documentMentionQuery.value.trim().toLowerCase();
  return documents.value
    .filter((document) => document.processingStatus === "succeeded")
    .filter((document) => !query || document.originalFilename.toLowerCase().includes(query))
    .slice(0, 8);
});

function updateDocumentMentionState() {
  const match = question.value.match(/(?:^|\s)@([^\s@]*)$/);
  documentMentionOpen.value = match !== null;
  documentMentionQuery.value = match?.[1] ?? "";
}

function selectDocumentMention(document: DocumentItem) {
  if (!selectedDocumentIDs.value.includes(document.id)) {
    selectedDocumentIDs.value = [...selectedDocumentIDs.value, document.id];
  }
  question.value = question.value.replace(/(?:^|\s)@[^\s@]*$/, " ").trimStart();
  documentMentionOpen.value = false;
  documentMentionQuery.value = "";
}

function removeDocumentMention(documentID: number) {
  selectedDocumentIDs.value = selectedDocumentIDs.value.filter((id) => id !== documentID);
}

async function refreshFeedbackStats() {
  if (!selectedKnowledgeBaseId.value) {
    feedbackStats.value = { total: 0, positive: 0, negative: 0, positiveRate: 0 };
    return;
  }
  try {
    feedbackStats.value = await requestJSON<FeedbackStats>(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/feedback/stats`);
  } catch {
    feedbackStats.value = { total: 0, positive: 0, negative: 0, positiveRate: 0 };
  }
}

async function submitAnswerFeedback(message: ChatMessage, rating: -1 | 1) {
  if (!selectedKnowledgeBaseId.value || !conversationId.value || !message.id || message.feedbackSubmitting) return;
  message.feedbackSubmitting = true;
  try {
    await requestJSON(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/conversations/${conversationId.value}/messages/${message.id}/feedback`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ rating }),
    });
    message.feedback = rating;
    void refreshFeedbackStats();
  } catch (error) {
    showError(error);
  } finally {
    message.feedbackSubmitting = false;
  }
}

// 加载一页历史消息并更新游标。beforeId 为空加载最新一页。
async function loadMessagePage(id: number, beforeId: number | null): Promise<boolean> {
  if (!selectedKnowledgeBaseId.value) return false;
  const query = beforeId ? `?limit=50&before_id=${beforeId}` : "?limit=50";
  const stored = await requestJSON<{ messages: ConversationMessage[]; has_more: boolean }>(
    `/api/knowledge-bases/${selectedKnowledgeBaseId.value}/conversations/${id}/messages${query}`,
  );
  const parsed = stored.messages.map(toChatMessage);
  if (beforeId) {
    messages.value = [...parsed, ...messages.value];
  } else {
    messages.value = parsed;
  }
  messageCursor.value = {
    conversationId: id,
    beforeId: stored.messages[0]?.id ?? null,
    hasMore: stored.has_more,
  };
  return stored.has_more;
}

function onMessagesScroll(event: Event) {
  const element = event.target as HTMLElement;
  if (element.scrollTop <= 8 && messageCursor.value.hasMore && !loadingOlderMessages.value && messageCursor.value.conversationId) {
    void loadOlderMessages();
  }
}

async function loadOlderMessages() {
  const cursor = messageCursor.value;
  if (!cursor.conversationId || !cursor.hasMore || loadingOlderMessages.value) return;
  loadingOlderMessages.value = true;
  const messagesElement = document.querySelector(".messages");
  const previousScrollHeight = messagesElement?.scrollHeight ?? 0;
  try {
    await loadMessagePage(cursor.conversationId, cursor.beforeId);
    // 新内容插入顶部后把滚动位置向下推，保持用户当前看到的区域不变。
    requestAnimationFrame(() => {
      if (messagesElement) messagesElement.scrollTop = messagesElement.scrollHeight - previousScrollHeight;
    });
  } catch (error) {
    showError(error);
  } finally {
    loadingOlderMessages.value = false;
  }
}

async function createConversation() {
  if (!selectedKnowledgeBaseId.value || conversationCreating.value || streaming.value) return;
  conversationCreating.value = true;
  try {
    let created = await requestJSON<Conversation>(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/conversations`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ title: "新对话" }),
    });
    if (selectedChatModel.value) created = await persistConversationChatModel(created.id, selectedChatModel.value);
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

function toggleConversationMenu(id: number) {
  openConversationMenuId.value = openConversationMenuId.value === id ? null : id;
}

function closeConversationMenu() {
  openConversationMenuId.value = null;
}

async function clearConversationMessages(item: Conversation) {
  if (!selectedKnowledgeBaseId.value || streaming.value) return;
  if (!window.confirm(`清空“${item.title}”的所有消息？会话本身会保留。`)) return;
  try {
    await requestJSON<void>(
      `/api/knowledge-bases/${selectedKnowledgeBaseId.value}/conversations/${item.id}/messages`,
      { method: "DELETE" },
    );
    if (conversationId.value === item.id) {
      messages.value = [];
    }
  } catch (error) {
    showError(error);
  }
}

async function copyConversationMarkdown(item: Conversation) {
  if (!selectedKnowledgeBaseId.value) return;
  try {
    // 分页拉取全部消息（最多 200 条一页），保证复制的会话完整。
    const all: ConversationMessage[] = [];
    let beforeId: number | null = null;
    while (true) {
      const query: string = beforeId ? `?limit=200&before_id=${beforeId}` : "?limit=200";
      const page: { messages: ConversationMessage[]; has_more: boolean } = await requestJSON(
        `/api/knowledge-bases/${selectedKnowledgeBaseId.value}/conversations/${item.id}/messages${query}`,
      );
      all.unshift(...page.messages);
      if (!page.has_more || !page.messages.length) break;
      beforeId = page.messages[0].id;
    }
    if (!all.length) {
      errorMessage.value = "这个会话还没有消息可复制。";
      return;
    }
    const lines = all.map((message) => message.role === "user" ? `**问题**：${message.content}` : `**回答**：${message.content}`);
    await navigator.clipboard.writeText(`# ${item.title}\n\n${lines.join("\n\n")}`);
    copiedConversationID.value = item.id;
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
  let created = await requestJSON<Conversation>(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/conversations`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ title: title.slice(0, 80) }),
  });
  if (selectedChatModel.value) created = await persistConversationChatModel(created.id, selectedChatModel.value);
  conversationId.value = created.id;
  conversations.value = [created, ...conversations.value.filter((item) => item.id !== created.id)];
  return created.id;
}

function selectKnowledgeBase(id: number) {
  if (streaming.value) return;
	conversationSearch.value = "";
	conversationOffset.value = 0;
	conversationHasMore.value = false;
	if (conversationSearchTimer !== undefined) window.clearTimeout(conversationSearchTimer);
	selectedKnowledgeBaseId.value = id;
	clearConversationSelection();
	selectedDocumentIDs.value = [];
  closeSource();
  closeDocumentPreview();
  mobileRailOpen.value = false;
  messages.value = [];
  conversationId.value = null;
  void refreshDocuments();
  void refreshFeedbackStats();
  void loadConversation();
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

const maxChatAttachments = 3;
const maxChatImageBytes = 4 * 1024 * 1024;
const maxChatTextBytes = 128 * 1024;

function attachmentContentType(file: File): string {
  if (["image/png", "image/jpeg", "image/webp"].includes(file.type)) return file.type;
  const extension = file.name.toLowerCase().split(".").pop();
  if (extension === "md") return "text/markdown";
  if (extension === "txt") return "text/plain";
  return file.type;
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  const chunkSize = 0x8000;
  for (let offset = 0; offset < bytes.length; offset += chunkSize) {
    binary += String.fromCharCode(...bytes.subarray(offset, Math.min(offset + chunkSize, bytes.length)));
  }
  return btoa(binary);
}

async function prepareChatAttachment(file: File): Promise<ChatAttachmentDraft> {
  const contentType = attachmentContentType(file);
  if (!["image/png", "image/jpeg", "image/webp", "text/plain", "text/markdown"].includes(contentType)) {
    throw new Error(`不支持附件类型：${file.name}`);
  }
  const limit = contentType.startsWith("image/") ? maxChatImageBytes : maxChatTextBytes;
  if (file.size <= 0 || file.size > limit) {
    throw new Error(`附件“${file.name}”超过大小限制。`);
  }
  const dataBase64 = bytesToBase64(new Uint8Array(await file.arrayBuffer()));
  return {
    filename: file.name,
    contentType,
    dataBase64,
    dataURL: `data:${contentType};base64,${dataBase64}`,
    sizeBytes: file.size,
  };
}

async function addChatAttachments(files: File[]) {
  const remaining = maxChatAttachments - chatAttachments.value.length;
  if (remaining <= 0) {
    errorMessage.value = `一条消息最多添加 ${maxChatAttachments} 个附件。`;
    return;
  }
  try {
    const prepared = [] as ChatAttachmentDraft[];
    for (const file of files.slice(0, remaining)) prepared.push(await prepareChatAttachment(file));
    chatAttachments.value = [...chatAttachments.value, ...prepared];
    if (files.length > remaining) errorMessage.value = `一条消息最多添加 ${maxChatAttachments} 个附件。`;
  } catch (error) {
    errorMessage.value = error instanceof Error ? error.message : "无法读取附件。";
  } finally {
    if (chatAttachmentInput.value) chatAttachmentInput.value.value = "";
  }
}

function onChatAttachmentInput(event: Event) {
  const target = event.target as HTMLInputElement | null;
  void addChatAttachments(Array.from(target?.files ?? []));
}

function removeChatAttachment(index: number) {
  chatAttachments.value = chatAttachments.value.filter((_, itemIndex) => itemIndex !== index);
}

function clearChatAttachments() {
  chatAttachments.value = [];
  if (chatAttachmentInput.value) chatAttachmentInput.value.value = "";
}

async function askQuestion() {
  const prompt = question.value.trim();
  if (!prompt || !selectedKnowledgeBaseId.value || streaming.value) return;
  const outgoingAttachments = [...chatAttachments.value];
  let activeConversationID: number | null = null;
  try {
    activeConversationID = await ensureConversation(prompt);
  } catch (error) {
    showError(error);
    return;
  }
  question.value = "";
  clearChatAttachments();
  closeSource();
  closeDocumentPreview();
  messages.value.push({
    role: "user",
    content: prompt,
    attachments: outgoingAttachments.map(({ filename, contentType, dataURL, sizeBytes }) => ({ filename, contentType, dataURL, sizeBytes })),
  });
  const answer: ChatMessage = {
    role: "assistant",
    content: "",
    sources: [],
    status: "streaming",
    requestMessage: prompt,
    conversationId: activeConversationID,
    mode: "agent",
    seenEventIDs: new Set(),
  };
  messages.value.push(answer);
  const answerIndex = messages.value.length - 1;
  streaming.value = true;
  try {
    await streamAgentQuestion(prompt, answerIndex, activeConversationID, outgoingAttachments);
  } catch (error) {
    markAnswerFailure(answerIndex, error);
    showError(error);
  } finally {
    streaming.value = false;
    // 首轮问答后后端会把默认标题换成问题摘要，刷新列表让标题即时可见。
    void refreshConversationList();
  }
}

async function streamAgentQuestion(prompt: string, answerIndex: number, activeConversationID: number | null, attachments: ChatAttachmentDraft[] = []) {
  if (!selectedKnowledgeBaseId.value) throw new Error("请先选择知识库。");
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    Accept: "text/event-stream",
  };
  headers["Idempotency-Key"] = crypto.randomUUID();
  const response = await fetch(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/agent-chat/stream`, {
    method: "POST",
    headers,
    body: JSON.stringify({
      message: prompt,
      chat_model: selectedChatModel.value || undefined,
      top_k: topK.value,
      similarity_threshold: similarityThreshold.value,
      document_ids: selectedDocumentIDs.value,
      query_rewrite: queryRewrite.value,
      keyword_threshold: keywordThreshold.value,
      conversation_id: activeConversationID,
      thinking_mode: thinkingMode.value,
      attachments: attachments.map(({ filename, contentType, dataBase64 }) => ({ filename, content_type: contentType, data_base64: dataBase64 })),
    }),
  });
  if (!response.ok) {
    const payload = await response.json().catch(() => null);
    throw new Error(payload?.error || "问答服务暂不可用");
  }
  try {
    const payload = await response.json() as { run_id?: string; stream_url?: string };
    const initialRunID = payload.run_id || response.headers.get("X-Agent-Run-ID");
    if (!initialRunID) throw new Error("问答服务没有返回运行 ID。");
    const streamResponse = await fetch(payload.stream_url || `/api/knowledge-bases/${selectedKnowledgeBaseId.value}/agent-runs/${encodeURIComponent(initialRunID)}/stream`, {
      method: "GET",
      headers: { Accept: "text/event-stream" },
    });
    if (!streamResponse.ok || !streamResponse.body) {
      throw new Error("无法连接 Agent 流式通道。");
    }
    if (initialRunID) {
      messages.value[answerIndex].runID = initialRunID;
      // 记住未完成运行：刷新后可以重连 Hub 恢复，而不用重新调用模型。
      if (activeConversationID) {
        savePendingRun({ knowledgeBaseId: selectedKnowledgeBaseId.value, runID: initialRunID, conversationId: activeConversationID });
      }
    }
    await readAgentSSE(streamResponse, answerIndex);
  } catch (error) {
    const currentAnswer = messages.value[answerIndex];
    if (currentAnswer?.runID) {
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
    if (currentAnswer.runID) {
      await resumeAgentStream(currentAnswer, answerIndex);
    }
    if (currentAnswer.status === "streaming") {
      currentAnswer.status = "error";
      currentAnswer.retryable = true;
      currentAnswer.activity = "";
      currentAnswer.content = currentAnswer.content || "流式响应提前结束，请重试。";
    }
  }
  clearPendingRun();
}

async function reprocessDocument(item: DocumentItem) {
  if (!selectedKnowledgeBaseId.value || streaming.value || reprocessingDocumentID.value !== null) return;
  if (["pending", "processing"].includes(item.processingStatus)) return;
  if (!window.confirm(`重新处理“${item.originalFilename}”？系统会重新切分并生成向量。`)) return;
  reprocessingDocumentID.value = item.id;
  try {
    await requestJSON<{ documentId: number; status: string }>(
      `/api/knowledge-bases/${selectedKnowledgeBaseId.value}/documents/${item.id}/reprocess`,
      { method: "POST" },
    );
    await refreshDocuments();
  } catch (error) {
    if (error instanceof APIError && error.status === 409) {
      errorMessage.value = "文档已经在处理中。";
    } else {
      showError(error);
    }
  } finally {
    reprocessingDocumentID.value = null;
  }
}

async function readAgentSSE(response: Response, answerIndex: number) {
  if (!response.body) throw new Error("问答服务没有返回流式内容。");
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";
  while (true) {
    const { value, done } = await reader.read();
    buffer += decoder.decode(value ?? new Uint8Array(), { stream: !done });
    const blocks = buffer.split(/\r?\n\r?\n/);
    buffer = blocks.pop() ?? "";
    blocks.forEach((block) => consumeSSEBlock(block, answerIndex));
    if (done) break;
  }
  if (buffer.trim()) consumeSSEBlock(buffer, answerIndex);
}

async function resumeAgentStream(answer: ChatMessage, answerIndex: number) {
  if (!selectedKnowledgeBaseId.value || !answer.runID) return;
  answer.activity = "正在恢复 Agent 运行…";
  answer.streamGap = false;
  const initialResult = await connectAgentStream(answer, answerIndex, true);
  if (initialResult === "completed") return;
  if (initialResult === "gap") {
    const statusPayload = await fetchAgentRunStatus(answer);
    if (statusPayload?.status === "running") {
      answer.streamGap = false;
      answer.activity = "中间事件已过期，正在订阅最新进度…";
      const tailResult = await connectAgentStream(answer, answerIndex, false);
      if (tailResult === "completed") return;
    } else if (statusPayload?.status === "waiting_children") {
      const count = statusPayload.children?.length ?? 0;
      answer.activity = count > 0 ? `正在等待 ${count} 个子 Agent 完成…` : "正在等待子 Agent 完成…";
      await loadAgentRunTree(answer);
    }
  }
  if (await recoverPersistedAgentResponse(answer)) return;
  throw new Error("实时事件已过期，最终答案尚未持久化。");
}

type AgentStreamConnectResult = "completed" | "gap" | "unavailable";

async function connectAgentStream(answer: ChatMessage, answerIndex: number, withCursor: boolean): Promise<AgentStreamConnectResult> {
  if (!selectedKnowledgeBaseId.value || !answer.runID) return "unavailable";
  const headers: Record<string, string> = { Accept: "text/event-stream" };
  if (withCursor && answer.lastEventID) headers["Last-Event-ID"] = answer.lastEventID;
  const response = await fetch(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/agent-runs/${encodeURIComponent(answer.runID)}/stream`, {
    method: "GET",
    headers,
  });
  if (!response.ok || !response.body) return "unavailable";
  await readAgentSSE(response, answerIndex);
  return answer.streamGap ? "gap" : "completed";
}

type AgentRunStatusPayload = {
  run_id?: string;
  status?: string;
  error?: string;
  response?: Record<string, unknown>;
  children?: { run_id?: string; status?: string; attempt_count?: number; updated_at?: string }[];
};

async function fetchAgentRunStatus(answer: ChatMessage): Promise<AgentRunStatusPayload | null> {
  if (!selectedKnowledgeBaseId.value || !answer.runID) return null;
  const statusResponse = await fetch(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/agent-runs/${encodeURIComponent(answer.runID)}`, {
    method: "GET",
    headers: { Accept: "application/json" },
  });
  if (!statusResponse.ok) return null;
  return await statusResponse.json() as AgentRunStatusPayload;
}

async function loadAgentRunTree(answer: ChatMessage) {
  if (!selectedKnowledgeBaseId.value || !answer.runID) return;
  try {
    const response = await fetch(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/agent-runs/${encodeURIComponent(answer.runID)}/children`, {
      method: "GET",
      headers: { Accept: "application/json" },
    });
    if (!response.ok) return;
    answer.childRunTree = await response.json() as AgentRunTree;
  } catch {
    // 执行树只是诊断信息，加载失败不能覆盖已经生成的答案。
  }
}

function childRunTreeRows(message: ChatMessage) {
  const rows: { node: AgentRunTree; depth: number }[] = [];
  const visit = (nodes: AgentRunTree[] | undefined, depth: number) => {
    for (const node of nodes ?? []) {
      rows.push({ node, depth });
      visit(node.children, depth + 1);
    }
  };
  visit(message.childRunTree?.children, 0);
  return rows;
}

function childRunStatusLabel(status: string) {
  switch (status) {
    case "succeeded": return "已完成";
    case "failed": return "失败";
    case "canceled": return "已取消";
    case "running": return "运行中";
    default: return "等待中";
  }
}

async function recoverPersistedAgentResponse(answer: ChatMessage): Promise<boolean> {
  if (!selectedKnowledgeBaseId.value || !answer.runID) return false;
  for (let attempt = 0; attempt < 60; attempt += 1) {
    const statusResponse = await fetch(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/agent-runs/${encodeURIComponent(answer.runID)}`, {
      method: "GET",
      headers: { Accept: "application/json" },
    });
    if (!statusResponse.ok) return false;
    const statusPayload = await statusResponse.json() as AgentRunStatusPayload;
    if (statusPayload.status === "waiting_children") {
      const count = statusPayload.children?.length ?? 0;
      answer.activity = count > 0 ? `正在等待 ${count} 个子 Agent 完成…` : "正在等待子 Agent 完成…";
      if (attempt === 0 || attempt % 4 === 0) void loadAgentRunTree(answer);
    }
    if (statusPayload.response && typeof statusPayload.response === "object") {
      applyPersistedAgentResponse(answer, statusPayload);
      return true;
    }
    if (statusPayload.status === "failed" || statusPayload.status === "canceled") return false;
    await new Promise((resolve) => window.setTimeout(resolve, 500));
  }
  return false;
}

function applyPersistedAgentResponse(answer: ChatMessage, payload: AgentRunStatusPayload) {
  const response = payload.response;
  if (!response) return;
  answer.runID = typeof payload.run_id === "string" ? payload.run_id : answer.runID;
  answer.content = typeof response.answer === "string" ? response.answer : answer.content;
  answer.sources = mergeSources(answer.sources ?? [], parseSources(response.sources));
  const stats = response.stats;
  if (stats && typeof stats === "object") {
    answer.agentStats = parseAgentRunStats(stats);
    const statsRecord = stats as Record<string, unknown>;
    if (statsRecord.query_rewrite && typeof statsRecord.query_rewrite === "object") answer.queryRewrite = statsRecord.query_rewrite as QueryRewriteStatus;
    if (statsRecord.retrieval && typeof statsRecord.retrieval === "object") answer.retrieval = statsRecord.retrieval as RetrievalStats;
  }
  answer.agentEvents = restoreAgentEvents({
    run_id: answer.runID,
    status: typeof response.status === "string" ? response.status : "succeeded",
    stats: parseAgentRunStats(stats),
    steps: Array.isArray(response.steps) ? response.steps as StoredAgentTrace["steps"] : [],
    events: Array.isArray(response.trace) ? response.trace as StoredAgentTrace["events"] : [],
  });
  answer.activity = "已从持久化结果恢复";
  answer.status = payload.status === "canceled" ? "stopped" : payload.status === "failed" ? "error" : "done";
  answer.retryable = answer.status === "error";
  void loadAgentRunTree(answer);
}

// 页面刷新后尝试恢复上次未完成的 Agent 运行（仅标准 Agent 模式）。
// Hub 在进程内保留事件快照（约 10 分钟），恢复失败时降级为普通错误消息。
async function restorePendingAgentRun() {
  const pending = readPendingRun();
  if (!pending || pending.knowledgeBaseId !== selectedKnowledgeBaseId.value) return;
  clearPendingRun();
  const answer: ChatMessage = {
    role: "assistant",
    content: "",
    sources: [],
    status: "streaming",
    runID: pending.runID,
    conversationId: pending.conversationId ?? null,
    mode: "agent",
    seenEventIDs: new Set(),
  };
  messages.value.push(answer);
  const answerIndex = messages.value.length - 1;
  streaming.value = true;
  try {
    await resumeAgentStream(answer, answerIndex);
  } catch (error) {
    markAnswerFailure(answerIndex, error);
  } finally {
    streaming.value = false;
  }
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
  answer.reasoningContent = "";
  answer.reasoningExpanded = false;
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
    await streamAgentQuestion(answer.requestMessage, answerIndex, answer.conversationId ?? conversationId.value);
  } catch (error) {
    markAnswerFailure(answerIndex, error);
    showError(error);
  } finally {
    streaming.value = false;
  }
}

// 标准 Agent 流式回答通过持久化 run_id 支持停止和断线恢复。
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

const agentToolLabels: Record<string, string> = {
  knowledge_search: "检索知识库",
  delegate_research: "委派只读研究",
  document_list: "查看文档列表",
  document_info: "查看文档状态",
  document_read: "读取文档正文",
  document_summary: "生成文档摘要",
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
    case "document_summary":
      return "正在生成文档摘要…";
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

// 流式运行中的实时流水线状态：检索工具进行中/完成时显示进度徽标，
// 回答结束（run_finished）后由 agentTraceSummary 接管展示运行摘要。
function agentTraceStatus(message: ChatMessage) {
  const events = message.agentEvents ?? [];
  const last = events[events.length - 1];
  if (!last || message.status !== "streaming") return "";
  if (last.status === "running" && last.type === "tool_called") {
    return `正在${last.label}…`;
  }
  if (last.status === "done" && last.label.includes("检索完成")) {
    return `搜索完成 · ${(message.sources ?? []).length} 条引用`;
  }
  return "";
}

function recordAgentEvent(
  answer: ChatMessage,
  type: string,
  label: string,
  detail = "",
  step?: number,
  status: AgentEvent["status"] = "done",
  extra: Pick<AgentEvent, "toolCallID" | "arguments" | "resultSummary" | "pending" | "taskID" | "childRunID" | "childEventType"> = {},
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

function finishLastAgentToolEvent(
  answer: ChatMessage,
  options: {
    label: string;
    detail: string;
    status?: AgentEvent["status"];
    toolCallID?: string;
    sourceKeys?: string[];
    documents?: AgentDocument[];
    documentInfo?: AgentDocument | null;
    pending?: boolean;
    taskID?: string;
  },
) {
  const events = answer.agentEvents ?? [];
  const latest = [...events].reverse().find((item) => item.type === "tool_called"
    && item.status === "running"
    && (!options.toolCallID || item.toolCallID === options.toolCallID));
  if (latest) {
    latest.label = options.label;
    latest.detail = options.detail.slice(0, 140);
    latest.resultSummary = options.detail.slice(0, 140);
    latest.sourceKeys = (options.sourceKeys ?? []).slice(0, 20);
    latest.status = options.status ?? "done";
    latest.pending = options.pending;
    latest.taskID = options.taskID;
    if (options.documents?.length) latest.documents = options.documents;
    if (options.documentInfo) latest.documentInfo = options.documentInfo;
  }
}

function childEventState(eventType: string): AgentEvent["status"] {
  if (eventType === "run_failed" || eventType === "run_canceled") return "error";
  if (eventType === "run_started" || eventType === "step_started" || eventType === "tool_called" || eventType === "reasoning_delta" || eventType === "message_delta") return "running";
  return "done";
}

function childEventLabel(eventType: string, toolName: string) {
  if (toolName) return `子 Agent：${displayAgentToolName(toolName)}`;
  switch (eventType) {
    case "run_started": return "子 Agent 开始运行";
    case "run_finished": return "子 Agent 完成";
    case "run_failed": return "子 Agent 运行失败";
    case "run_canceled": return "子 Agent 已取消";
    case "parent_resumed": return "父 Agent 即将恢复";
    case "step_started": return "子 Agent 分析问题";
    default: return "子 Agent 执行中";
  }
}

function stopSummaryPolling(answerIndex: number) {
  const timer = summaryPollingTimers.get(answerIndex);
  if (timer !== undefined) window.clearTimeout(timer);
  summaryPollingTimers.delete(answerIndex);
}

function startDocumentSummaryPolling(answer: ChatMessage, answerIndex: number) {
  if (!answer.summaryTask || summaryPollingTimers.has(answerIndex)) return;
  const task = answer.summaryTask;
  const startedAt = Date.now();
  const poll = async () => {
    summaryPollingTimers.delete(answerIndex);
    if (!selectedKnowledgeBaseId.value || selectedKnowledgeBaseId.value !== task.knowledgeBaseId) return;
    try {
      const status = await requestJSON<{ status: string; error?: string }>(
        `/api/knowledge-bases/${task.knowledgeBaseId}/documents/${task.documentId}/summary`,
      );
      task.status = status.status;
      if (status.status === "succeeded") {
        answer.activity = "文档摘要已生成，请再次提问以使用缓存摘要。";
        const event = [...(answer.agentEvents ?? [])].reverse().find((item) => item.taskID === task.taskId);
        if (event) event.detail = "后台摘要已生成";
        return;
      }
      if (status.status === "failed") {
        answer.activity = status.error ? `文档摘要生成失败：${status.error}` : "文档摘要生成失败，请稍后重试。";
        return;
      }
    } catch {
      // 短暂网络错误不终止任务跟踪，下一轮继续查询。
    }
    if (Date.now() - startedAt < 20 * 60 * 1000) {
      summaryPollingTimers.set(answerIndex, window.setTimeout(() => void poll(), 2500));
    } else {
      answer.activity = "文档摘要仍在后台生成，可稍后再次提问查看结果。";
    }
  };
  void poll();
}

function documentIDFromToolArguments(answer: ChatMessage): number | null {
  const event = [...(answer.agentEvents ?? [])].reverse().find((item) => item.type === "tool_called" && item.arguments);
  if (!event?.arguments) return null;
  try {
    const parsed = JSON.parse(event.arguments) as { document_id?: unknown };
    return typeof parsed.document_id === "number" && parsed.document_id > 0 ? parsed.document_id : null;
  } catch {
    return null;
  }
}

function toolFinishedLabel(toolName: string): string {
  switch (toolName) {
    case "knowledge_search": return "知识库检索完成";
    case "document_list": return "查看文档列表完成";
    case "document_info": return "查看文档状态完成";
    case "document_read": return "读取文档正文完成";
    case "document_summary": return "文档摘要已提交";
    default: return "工具调用完成";
  }
}

function parseAgentDocuments(value: unknown): AgentDocument[] {
  if (!Array.isArray(value)) return [];
  return value
    .filter((item): item is AgentDocument => {
      if (!item || typeof item !== "object") return false;
      return typeof (item as Record<string, unknown>).id === "number";
    })
    .slice(0, 20);
}

function parseAgentDocumentInfo(value: unknown): AgentDocument | null {
  if (!value || typeof value !== "object") return null;
  const doc = value as Record<string, unknown>;
  return typeof doc.id === "number" ? doc as AgentDocument : null;
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

async function resolveApproval(approved: boolean) {
  const pending = pendingApproval.value;
  if (!pending || approvalBusy.value) return;
  approvalBusy.value = true;
  try {
    const response = await fetch(`/api/knowledge-bases/${pending.knowledgeBaseId}/agent-runs/${encodeURIComponent(pending.runID)}/approval`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ approved }),
    });
    if (!response.ok) {
      const payload = await response.json().catch(() => null);
      throw new Error(payload?.error || "审批请求失败");
    }
    pendingApproval.value = null;
  } catch (error) {
    showError(error);
  } finally {
    approvalBusy.value = false;
  }
}

function reasoningStatus(message: ChatMessage) {
  if (message.status === "streaming") return "思考中";
  if (message.status === "stopped") return "已停止";
  if (message.status === "error") return "已结束";
  return "已完成";
}

function toggleReasoning(message: ChatMessage) {
  message.reasoningExpanded = !message.reasoningExpanded;
}

function consumeSSEBlock(block: string, answerIndex: number) {
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
    if (payload.id) answer.lastEventID = payload.id;
    if (payload.id && answer.seenEventIDs) {
      if (answer.seenEventIDs.has(payload.id)) return;
      answer.seenEventIDs.add(payload.id);
    }
    if (payload.run_id) answer.runID = payload.run_id;
    const eventData = payload.data && typeof payload.data === "object" ? payload.data : {};
    const dataString = (key: string) => typeof eventData[key] === "string" ? eventData[key] as string : "";

    switch (event) {
      case "gap":
        answer.streamGap = true;
        answer.activity = "中间事件已过期，正在恢复最终答案…";
        break;
      case "waiting_children":
        answer.activity = "正在等待子 Agent 完成…";
        void fetchAgentRunStatus(answer).then((statusPayload) => {
          if (statusPayload?.status !== "waiting_children") return;
          const count = statusPayload.children?.length ?? 0;
          answer.activity = count > 0 ? `正在等待 ${count} 个子 Agent 完成…` : "正在等待子 Agent 完成…";
          void loadAgentRunTree(answer);
        });
        break;
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
        recordAgentEvent(answer, event, "Agent 开始运行", "正在分析问题", payload.step_number, "running");
        answer.activity = "正在理解问题…";
        break;
      case "tool_called":
        {
          const toolName = dataString("tool_name") || "knowledge_search";
          recordAgentEvent(answer, event, displayAgentToolName(toolName), "", payload.step_number, "running", {
            toolCallID: dataString("tool_call_id"),
            arguments: dataString("arguments"),
          });
        }
        answer.activity = agentToolActivity(dataString("tool_name"));
        break;
      case "child_event":
        {
          const childEventType = dataString("child_event_type");
          const toolName = dataString("tool_name");
          const childRunID = dataString("child_run_id");
          const status = childEventState(childEventType);
          const resultSummary = dataString("result_summary");
          const detail = childEventType === "parent_resumed"
            ? "所有子 Agent 已结束，父 Agent 即将继续"
            : resultSummary || (childRunID ? `运行 ${childRunID}` : "异步子任务");
          recordAgentEvent(answer, event, childEventLabel(childEventType, toolName), detail, payload.step_number, status, {
            childRunID: childRunID || undefined,
            childEventType: childEventType || undefined,
            resultSummary: resultSummary || undefined,
          });
        }
        break;
      case "tool_finished":
        {
          const sources = parseSources(eventData.sources);
          answer.sources = mergeSources(answer.sources ?? [], sources);
          const toolName = dataString("tool_name") || "knowledge_search";
          finishLastAgentToolEvent(answer, {
            label: toolFinishedLabel(toolName),
            detail: dataString("child_run_id")
              ? `已委派子 Agent 完成研究（${dataString("child_status") || "已完成"}）`
              : dataString("result_summary") || (eventData.no_relevant_results === true ? "没有找到足够相关资料" : `${sources.length || "已"} 条结果已返回`),
            status: "done",
            toolCallID: dataString("tool_call_id"),
            sourceKeys: sources.map(sourceKey),
            documents: parseAgentDocuments(eventData.documents),
            documentInfo: parseAgentDocumentInfo(eventData.document_info),
            pending: eventData.pending === true,
            taskID: dataString("task_id") || undefined,
          });
          if (toolName === "document_summary" && eventData.pending === true && selectedKnowledgeBaseId.value) {
            const documentID = documentIDFromToolArguments(answer);
            const taskID = dataString("task_id") || undefined;
            if (documentID) {
              answer.summaryTask = { knowledgeBaseId: selectedKnowledgeBaseId.value, documentId: documentID, taskId: taskID, status: "processing" };
              startDocumentSummaryPolling(answer, answerIndex);
            }
          }
        }
        if (Object.prototype.hasOwnProperty.call(eventData, "sources")) {
          answer.sources = mergeSources(answer.sources ?? [], parseSources(eventData.sources));
        }
        answer.activity = eventData.pending === true
          ? "文档摘要已提交后台生成，请稍后再次询问…"
          : "资料查找完成，正在组织答案…";
        break;
      case "approval_required":
        if (answer.runID && selectedKnowledgeBaseId.value) {
          pendingApproval.value = {
            knowledgeBaseId: selectedKnowledgeBaseId.value,
            runID: answer.runID,
            toolName: dataString("tool_name") || "未知工具",
            arguments: dataString("arguments"),
          };
          answer.activity = "等待你的确认后继续执行…";
        }
        break;
      case "approval_resolved":
        if (pendingApproval.value?.runID === answer.runID) pendingApproval.value = null;
        break;
      case "approval_expired":
        if (pendingApproval.value?.runID === answer.runID) pendingApproval.value = null;
        answer.activity = "审批已超时，本轮运行已结束。";
        break;
      case "reasoning_delta":
        {
          const reasoning = dataString("content");
          if (reasoning) {
            answer.reasoningContent = `${answer.reasoningContent ?? ""}${reasoning}`.slice(0, 12 * 1024);
            answer.activity = "正在思考…";
          }
        }
        break;
      case "message_delta":
        if (!answer.agentEvents?.some((item) => item.type === "answer_started")) {
          recordAgentEvent(answer, "answer_started", "开始生成答案", "模型正在根据检索结果组织回答", payload.step_number, "running");
        }
        answer.content += dataString("content") || payload.content || "";
        answer.activity = "正在组织答案…";
        break;
      case "run_finished":
        if (pendingApproval.value?.runID === answer.runID) pendingApproval.value = null;
        clearPendingRun();
        for (const agentEvent of answer.agentEvents ?? []) {
          if (agentEvent.status === "running") agentEvent.status = "done";
        }
        const answerEvent = answer.agentEvents?.find((item) => item.type === "answer_started");
        if (answerEvent) {
          answerEvent.detail = "最终回答已生成";
          answerEvent.status = "done";
        }
        recordAgentEvent(answer, event, "Agent 完成", "本轮运行成功", payload.step_number, "done");
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
        void loadAgentRunTree(answer);
        break;
      case "conversation_saved":
        if (typeof eventData.assistant_message_id === "number") answer.id = eventData.assistant_message_id;
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
        if (pendingApproval.value?.runID === answer.runID) pendingApproval.value = null;
        // 用户主动停止后引擎已发 run_canceled；后续通用 error 事件不应覆盖已停止状态。
        if (answer.status === "stopped") break;
        clearPendingRun();
        for (const agentEvent of answer.agentEvents ?? []) {
          if (agentEvent.status === "running") agentEvent.status = "error";
        }
        recordAgentEvent(answer, event, "Agent 运行失败", dataString("error") || payload.error || "执行失败", payload.step_number, "error");
        answer.status = "error";
        answer.retryable = true;
        answer.activity = "";
        answer.content = dataString("error") || payload.error || "问答失败。";
        break;
      case "run_canceled":
        if (pendingApproval.value?.runID === answer.runID) pendingApproval.value = null;
        clearPendingRun();
        for (const agentEvent of answer.agentEvents ?? []) {
          if (agentEvent.status === "running") agentEvent.status = "error";
        }
        recordAgentEvent(answer, event, "Agent 已停止", "用户停止生成", payload.step_number, "error");
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
  void loadChatModelOptions();
  void loadKnowledgeBases().then(() => loadConversation()).then(() => restorePendingAgentRun());
});
onUnmounted(() => {
  window.clearInterval(documentPollTimer);
  for (const answerIndex of summaryPollingTimers.keys()) stopSummaryPolling(answerIndex);
  window.clearTimeout(copyFeedbackTimer);
  if (conversationSearchTimer !== undefined) window.clearTimeout(conversationSearchTimer);
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
                v-if="['succeeded', 'failed'].includes(document.processingStatus)"
                class="document-preview-button"
                type="button"
                :disabled="streaming || reprocessingDocumentID !== null"
                title="重新切分并生成向量"
                @click="reprocessDocument(document)"
              >{{ reprocessingDocumentID === document.id ? "排队中…" : "重处理" }}</button>
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
            <span class="panel-meta">STREAMING</span>
          </div>
          <div class="chat-intro">
            <span class="chat-spark">✦</span>
            <div class="chat-intro-copy"><strong>从资料里找答案</strong><p>回答会标记它引用的原始段落。</p></div>
            <div v-if="feedbackStats.total" class="feedback-summary" title="当前知识库的回答反馈统计">
              <strong>{{ Math.round(feedbackStats.positiveRate * 100) }}%</strong>
              <span>满意率 · {{ feedbackStats.total }} 条反馈</span>
            </div>
          </div>
          <div class="retrieval-controls">
            <div><strong>检索范围</strong><span>{{ selectedDocumentIDs.length ? `标准 Agent 仅检索 ${selectedDocumentIDs.length} 份已选文档；` : "当前模式检索整个知识库；" }}控制召回数量和证据相关度</span></div>
            <label>召回片段数<select v-model.number="topK" :disabled="streaming"><option v-for="value in [3, 5, 8, 12, 20]" :key="value" :value="value">{{ value }} 条</option></select></label>
            <label class="rewrite-control"><input v-model="queryRewrite" type="checkbox" :disabled="streaming" /> 多查询改写</label>
            <label class="threshold-control">关键词下限
              <input v-model.number="keywordThreshold" type="range" min="0" max="0.80" step="0.05" :disabled="streaming">
              <output>{{ keywordThreshold.toFixed(2) }}</output>
            </label>
            <label class="threshold-control">距离上限
              <input v-model.number="similarityThreshold" type="range" min="0.30" max="0.90" step="0.05" :disabled="streaming">
              <output>{{ similarityThreshold.toFixed(2) }}</output>
            </label>
          </div>
          <template>
            <div class="conversation-bar">
              <div class="conversation-current">
                <span class="conversation-caption">当前会话</span>
                <strong>{{ selectedConversation?.title || "还没有会话" }}</strong>
              </div>
              <label v-if="chatModelOptions.length" class="conversation-model">
                <span>回答模型</span>
                <select v-model="selectedChatModel" :disabled="streaming || chatModelOptionsLoading" aria-label="选择回答模型" @change="updateConversationChatModel">
                  <option v-for="model in chatModelOptions" :key="model" :value="model">{{ model }}</option>
                </select>
              </label>
              <span v-else-if="chatModelOptionsError" class="conversation-model-error">模型列表不可用</span>
              <button class="conversation-new" type="button" :disabled="conversationCreating || streaming || !selectedKnowledgeBase" @click="createConversation">
                {{ conversationCreating ? "创建中…" : "+ 新对话" }}
              </button>
            </div>
            <div class="conversation-search">
              <input v-model="conversationSearch" type="search" placeholder="搜索会话标题或消息" aria-label="搜索会话标题或消息" @input="scheduleConversationSearch" />
              <button v-if="conversationSearch" type="button" aria-label="清除会话搜索" @click="conversationSearch = ''; scheduleConversationSearch()">清除</button>
            </div>
            <div v-if="selectedConversationIDs.length" class="conversation-batch-toolbar">
              <span>已选 {{ selectedConversationIDs.length }} 个</span>
              <button type="button" :disabled="conversationBatchBusy || streaming" @click="deleteSelectedConversations">{{ conversationBatchBusy ? "删除中…" : "批量删除" }}</button>
              <button type="button" :disabled="conversationBatchBusy" @click="pinSelectedConversations(true)">批量置顶</button>
              <button type="button" :disabled="conversationBatchBusy" @click="pinSelectedConversations(false)">取消置顶</button>
              <button type="button" :disabled="conversationBatchBusy" @click="exportSelectedConversations">导出 Markdown</button>
              <button type="button" :disabled="conversationBatchBusy" @click="clearConversationSelection">取消选择</button>
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
                    <input class="conversation-select" type="checkbox" :checked="selectedConversationIDs.includes(item.id)" aria-label="选择会话" @click.stop="toggleConversationSelection(item.id)" />
                    <span class="conversation-item-title">{{ item.isPinned ? "📌 " : "" }}{{ item.title }}</span>
                    <small>{{ new Date(item.updatedAt).toLocaleDateString("zh-CN") }}</small>
                    <div class="conversation-actions">
                      <button type="button" class="conversation-more" aria-label="会话更多操作" :disabled="conversationsLoading || streaming" @click.stop="toggleConversationMenu(item.id)">⋯</button>
                      <div v-if="openConversationMenuId === item.id" class="conversation-menu" role="menu" @click.stop>
                        <button type="button" role="menuitem" @click="togglePinConversation(item); closeConversationMenu()">{{ item.isPinned ? "取消置顶" : "置顶" }}</button>
                        <button type="button" role="menuitem" @click="renameConversation(item); closeConversationMenu()">改名</button>
                        <button type="button" role="menuitem" @click="clearConversationMessages(item); closeConversationMenu()">清空消息</button>
                        <button type="button" role="menuitem" @click="copyConversationMarkdown(item); closeConversationMenu()">{{ copiedConversationID === item.id ? "已复制 Markdown" : "复制 Markdown" }}</button>
                        <button type="button" role="menuitem" class="conversation-menu--danger" @click="deleteConversation(item); closeConversationMenu()">删除</button>
                      </div>
                    </div>
                  </div>
                </template>
            </div>
            <p v-else-if="conversationSearch.trim()" class="conversation-search-empty">没有找到匹配的会话。</p>
            <button v-if="conversationHasMore" type="button" class="conversation-load-more" :disabled="conversationsLoading" @click="loadMoreConversations">{{ conversationsLoading ? "加载中…" : "加载更多会话" }}</button>
          </template>
          <div class="messages" aria-live="polite" @click="onMessagesClick" @scroll.passive="onMessagesScroll">
            <div v-if="loadingOlderMessages" class="messages-loading-older">正在加载更早的消息…</div>
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
              <div v-if="message.role === 'assistant' && message.reasoningContent" class="thinking-card">
                <button
                  type="button"
                  class="thinking-toggle"
                  :aria-expanded="message.reasoningExpanded === true"
                  :aria-controls="`thinking-${index}`"
                  @click="toggleReasoning(message)"
                >
                  <span class="thinking-toggle-arrow" :class="{ 'thinking-toggle-arrow--expanded': message.reasoningExpanded }" aria-hidden="true">›</span>
                  <span class="thinking-toggle-title">深度思考</span>
                  <small>{{ reasoningStatus(message) }}</small>
                </button>
                <div v-if="message.reasoningExpanded" :id="`thinking-${index}`" class="thinking-content" role="region" aria-label="模型深度思考内容">
                  {{ message.reasoningContent }}
                </div>
              </div>
              <div v-if="message.role === 'assistant' && message.agentEvents?.length" class="agent-trace">
                <div class="agent-trace-head"><span>Agent 运行轨迹</span><small>{{ agentTraceStatus(message) || agentTraceSummary(message) }}</small></div>
                <details v-if="childRunTreeRows(message).length" class="agent-child-tree">
                  <summary>子 Agent 执行树 <small>{{ childRunTreeRows(message).length }} 个任务</small></summary>
                  <div
                    v-for="row in childRunTreeRows(message)"
                    :key="row.node.run_id"
                    class="agent-child-tree-row"
                    :style="{ paddingLeft: `${row.depth * 16 + 6}px` }"
                  >
                    <span class="agent-child-tree-branch" aria-hidden="true">↳</span>
                    <strong>{{ row.node.run_kind === "child" ? "子 Agent" : "Agent" }}</strong>
                    <code>{{ row.node.run_id }}</code>
                    <span class="agent-child-tree-status">{{ childRunStatusLabel(row.node.status) }}</span>
                    <small v-if="row.node.error">{{ row.node.error }}</small>
                  </div>
                </details>
                <div v-for="(trace, traceIndex) in message.agentEvents" :key="agentTraceKey(trace, traceIndex)" class="agent-trace-row" :class="[`agent-trace-row--${trace.status}`, { 'agent-trace-row--child': trace.type === 'child_event' }]">
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
                    <small v-if="trace.childRunID" class="agent-trace-child-id">{{ trace.childRunID }}</small>
                    <template v-if="isAgentTraceExpanded(message, trace, traceIndex)">
                      <small v-if="trace.arguments">参数：{{ trace.arguments }}</small>
                      <small v-if="trace.resultSummary && trace.resultSummary !== trace.detail">结果：{{ trace.resultSummary }}</small>
                      <div v-if="trace.documents?.length" class="agent-trace-table">
                        <table>
                          <thead><tr><th>文档</th><th>类型</th><th>大小</th><th>状态</th></tr></thead>
                          <tbody>
                            <tr v-for="doc in trace.documents" :key="doc.id">
                              <td>{{ doc.original_filename || "未命名文档" }}</td>
                              <td>{{ doc.content_type || "—" }}</td>
                              <td>{{ formatBytes(doc.size_bytes ?? 0) }}</td>
                              <td>{{ statusLabel(doc.processing_status ?? "") }}</td>
                            </tr>
                          </tbody>
                        </table>
                      </div>
                      <div v-if="trace.documentInfo" class="agent-trace-info">
                        <span>文档 #{{ trace.documentInfo.id }}：{{ trace.documentInfo.original_filename || "未命名文档" }}</span>
                        <span>{{ trace.documentInfo.content_type || "未知类型" }} · {{ formatBytes(trace.documentInfo.size_bytes ?? 0) }} · {{ statusLabel(trace.documentInfo.processing_status ?? "") }}</span>
                      </div>
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
              <div v-if="message.role === 'user' && message.attachments?.length" class="message-attachments" aria-label="本轮附件">
                <div v-for="attachment in message.attachments" :key="`${attachment.filename}-${attachment.sizeBytes}`" class="message-attachment">
                  <img v-if="attachment.dataURL?.startsWith('data:image/')" :src="attachment.dataURL" :alt="attachment.filename" />
                  <span v-else class="message-attachment-icon">TXT</span>
                  <span>{{ attachment.filename }}</span>
                </div>
              </div>
              <div class="message-bubble" :class="{ 'message-bubble--error': message.status === 'error' }">
                <span v-if="message.role === 'assistant' && !message.content && message.status === 'streaming'" class="typing"><i /><i /><i /></span>
                <span v-else-if="message.role === 'user' || message.status === 'streaming' || message.status === 'error'">{{ message.role === "assistant" && message.status === "streaming" ? plainAnswerText(message.content) : message.content }}</span>
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
                <span v-if="message.id" class="feedback-actions" aria-label="回答反馈">
                  <button type="button" :class="{ 'feedback-active': message.feedback === 1 }" :disabled="message.feedbackSubmitting" @click="submitAnswerFeedback(message, 1)">有帮助</button>
                  <button type="button" :class="{ 'feedback-active': message.feedback === -1 }" :disabled="message.feedbackSubmitting" @click="submitAnswerFeedback(message, -1)">需改进</button>
                </span>
              </div>
              <div v-if="shouldShowFollowUps(message, index)" class="follow-up-suggestions" aria-label="继续追问">
                <span>继续追问</span>
                <button v-for="prompt in followUpPrompts(message)" :key="prompt" type="button" @click="submitSuggestedQuestion(prompt)">{{ prompt }}</button>
                <button v-if="!message.generatedFollowUps?.length" class="follow-up-generate" type="button" :disabled="message.followUpLoading" @click="generateFollowUpSuggestions(message, index)">{{ message.followUpLoading ? "生成中…" : "生成更具体的追问" }}</button>
                <button v-else class="follow-up-generate" type="button" :disabled="message.followUpLoading" @click="generateFollowUpSuggestions(message, index)">{{ message.followUpLoading ? "换一批中…" : "换一批" }}</button>
              </div>
              <div v-if="message.role === 'assistant' && message.sources?.length" class="sources">
                <div class="sources-heading"><span class="sources-label">引用 {{ message.sources.length }}</span><span>点击查看原文</span></div>
                <div class="source-list">
                  <button v-for="(source, sourceIndex) in message.sources" :key="`${source.documentId}-${source.position}`" class="source-card" type="button" @click="openSource(source)">
                    <span class="source-card-index">{{ String(sourceIndex + 1).padStart(2, "0") }}</span>
                    <span class="source-card-body"><strong>{{ source.originalFilename || "未命名文档" }} <em :class="`source-match source-match--${source.matchType || 'unknown'}`">{{ matchTypeLabel(source.matchType) }}</em></strong><small v-if="source.headingPath" class="source-heading-path">{{ source.headingPath }}</small><small>第 {{ source.position + 1 }} 段 · {{ sourceScoreLabel(source) }}</small><span>{{ sourcePreview(source.content) }}</span></span>
                    <span class="source-card-arrow">↗</span>
                  </button>
                </div>
              </div>
            </article>
          </div>
          <section v-if="pendingApproval" class="approval-card" aria-live="polite">
            <div class="approval-card-copy">
              <strong>Agent 请求执行工具</strong>
              <span>{{ pendingApproval.toolName }} · 请确认后继续</span>
              <code v-if="pendingApproval.arguments">{{ pendingApproval.arguments }}</code>
            </div>
            <div class="approval-card-actions">
              <button type="button" :disabled="approvalBusy" @click="resolveApproval(false)">拒绝</button>
              <button type="button" class="approval-approve" :disabled="approvalBusy" @click="resolveApproval(true)">{{ approvalBusy ? "处理中…" : "允许执行" }}</button>
            </div>
          </section>
          <form class="composer" @submit.prevent="askQuestion">
            <div v-if="selectedDocumentIDs.length" class="mention-chip-list" aria-label="已选择的检索文档">
              <span v-for="documentID in selectedDocumentIDs" :key="documentID" class="mention-chip">
                @{{ documents.find((document) => document.id === documentID)?.originalFilename || `文档 #${documentID}` }}
                <button type="button" aria-label="移除文档范围" @click="removeDocumentMention(documentID)">×</button>
              </span>
            </div>
            <div class="question-input-wrap">
              <textarea v-model="question" rows="2" :disabled="!selectedKnowledgeBase || streaming" placeholder="问问这套资料… 输入 @ 选择文档" @input="updateDocumentMentionState" @keydown.enter.exact.prevent="askQuestion" />
              <div v-if="documentMentionOpen && documentMentionCandidates.length" class="document-mention-menu" role="listbox" aria-label="选择检索文档">
                <button v-for="document in documentMentionCandidates" :key="document.id" type="button" role="option" @mousedown.prevent="selectDocumentMention(document)">
                  <strong>@{{ document.originalFilename }}</strong><small>仅检索此文档</small>
                </button>
              </div>
            </div>
            <div v-if="chatAttachments.length" class="attachment-draft-list" aria-label="待发送附件">
              <div v-for="(attachment, attachmentIndex) in chatAttachments" :key="`${attachment.filename}-${attachmentIndex}`" class="attachment-draft">
                <img v-if="attachment.dataURL?.startsWith('data:image/')" :src="attachment.dataURL" :alt="attachment.filename" />
                <span v-else class="attachment-draft-icon">TXT</span>
                <span class="attachment-draft-name">{{ attachment.filename }}</span>
                <button type="button" aria-label="移除附件" @click="removeChatAttachment(attachmentIndex)">×</button>
              </div>
            </div>
            <div class="composer-footer">
              <div class="composer-tools">
                <label class="attachment-trigger" title="添加图片或文本附件">
                  <span aria-hidden="true">＋</span>附件
                  <input ref="chatAttachmentInput" type="file" accept="image/png,image/jpeg,image/webp,text/plain,text/markdown,.txt,.md" multiple :disabled="streaming" @change="onChatAttachmentInput" />
                </label>
                <label class="thinking-mode-control">
                  <span>思考</span>
                  <select v-model="thinkingMode" :disabled="streaming" aria-label="选择思考模式">
                    <option value="fast">快速</option>
                    <option value="standard">标准</option>
                    <option value="deep">深度</option>
                  </select>
                </label>
                <span>Enter 发送 · Shift + Enter 换行</span>
              </div>
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

    <div v-if="providerSettingsOpen" class="settings-backdrop">
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
          <p class="settings-intro">告诉文库该去哪里寻找聊天能力。Embedding 使用后端配置的本地模型，密钥始终留在后端环境变量里。</p>
          <label>服务名称<input v-model="providerForm.name" type="text" placeholder="例如：OpenAI" maxlength="120" required /></label>
          <label>Base URL<input v-model="providerForm.baseUrl" type="url" placeholder="https://api.openai.com/v1" required /></label>
          <label>聊天模型<input v-model="providerForm.chatModel" type="text" placeholder="例如：gpt-4o-mini" required /></label>
          <p class="settings-help settings-embedding-note">Embedding 已切换为后端本地模型，不需要在这里配置。</p>
          <label>可选聊天模型<input v-model="providerChatModelsText" type="text" placeholder="用逗号分隔，例如：gpt-4o-mini,qwen-plus" maxlength="3200" /><small class="settings-help">会话选择器只显示这里配置的模型；默认聊天模型会自动保留。</small></label>
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
            <div v-if="documentPreview.diagnostics" class="document-preview-diagnostics">
              <strong>切分诊断</strong>
              <span>策略：{{ documentPreview.diagnostics.strategy }}</span>
              <span>{{ documentPreview.diagnostics.chunkCount }} 个 Chunk</span>
              <span>结构 {{ documentPreview.diagnostics.headingCount }} · 保护块 {{ documentPreview.diagnostics.protectedBlockCount }}</span>
              <span>短块 {{ documentPreview.diagnostics.shortChunkCount }} · 超长块 {{ documentPreview.diagnostics.oversizeChunkCount }}</span>
            </div>
            <article v-for="chunk in documentPreview.chunks" :key="chunk.position" class="document-preview-chunk">
              <span>第 {{ chunk.position + 1 }} 段 <em v-if="chunk.headingPath">· {{ chunk.headingPath }}</em></span>
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
          <span v-if="selectedSource.headingPath" class="source-heading-path">{{ selectedSource.headingPath }}</span><span>第 {{ selectedSource.position + 1 }} 段 · {{ matchTypeLabel(selectedSource.matchType) }} · {{ sourceScoreLabel(selectedSource) }}</span>
        </div>
        <div class="source-panel-content"><p v-if="sourceLoading">正在读取引用原文…</p><template v-else><p>{{ sourceDisplayContent(selectedSource) }}</p><small v-if="selectedSource.contentTruncated">历史记录只保存了引用片段预览，原文已被截断。</small></template></div>
        <div class="source-panel-actions"><button class="source-copy-button" type="button" @click="copySource(selectedSource)">{{ copiedSourceKey === sourceKey(selectedSource) ? "已复制原文" : "复制原文" }}</button></div>
        <p class="source-panel-note">这段内容来自知识库检索结果，仅作为回答依据展示，不会被当作操作指令执行。</p>
      </aside>
    </div>
  </div>
</template>
