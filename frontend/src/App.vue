<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from "vue";

type KnowledgeBase = { id: number; name: string; description: string };
type DocumentItem = {
  id: number;
  knowledgeBaseId: number;
  originalFilename: string;
  contentType: string;
  sizeBytes: number;
  processingStatus: "pending" | "processing" | "succeeded" | "failed" | string;
};
type Source = {
  documentId: number;
  originalFilename?: string;
  position: number;
  content: string;
  distance: number;
  matchType?: "vector" | "keyword" | "hybrid" | string;
  keywordScore?: number;
  rerankScore?: number;
};
type ChatMessage = {
  role: "user" | "assistant";
  content: string;
  sources?: Source[];
  status?: "streaming" | "done" | "error";
  activity?: string;
  researchEvents?: ResearchEvent[];
  agentEvents?: AgentEvent[];
};
type ResearchEvent = { type: string; round?: number; label: string; detail?: string };
type AgentEvent = { type: string; step?: number; label: string; detail?: string; status: "running" | "done" | "error" };
type ChatMode = "agent" | "research" | "a2a";
type A2ATask = {
  id: string;
  status: "submitted" | "working" | "completed" | "failed" | string;
  created_at?: string;
  updated_at?: string;
  error?: string;
};
type Conversation = { id: number; knowledgeBaseId: number; title: string; createdAt: string; updatedAt: string };
type ConversationMessage = { id: number; conversationId: number; role: "user" | "assistant"; content: string; createdAt: string };
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
const question = ref("");
const newKnowledgeBaseName = ref("");
const newKnowledgeBaseDescription = ref("");
const loading = ref(true);
const loadingDocuments = ref(false);
const creatingKnowledgeBase = ref(false);
const uploading = ref(false);
const streaming = ref(false);
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
const selectedDocumentIDs = ref<number[]>([]);
const selectedSource = ref<Source | null>(null);
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
  return value.filter((item): item is Source => {
    if (!item || typeof item !== "object") return false;
    const source = item as Record<string, unknown>;
    return typeof source.documentId === "number"
      && typeof source.position === "number"
      && typeof source.content === "string"
      && typeof source.distance === "number";
  });
}

function mergeSources(existing: Source[], incoming: Source[]): Source[] {
  const merged = new Map(existing.map((source) => [`${source.documentId}-${source.position}`, source]));
  for (const source of incoming) {
    const key = `${source.documentId}-${source.position}`;
    if (!merged.has(key)) merged.set(key, source);
  }
  return [...merged.values()];
}

function sourcePreview(content: string): string {
  const normalized = content.replace(/\s+/g, " ").trim();
  return normalized.length > 118 ? `${normalized.slice(0, 118)}…` : normalized;
}

function matchTypeLabel(matchType?: string): string {
  return ({ vector: "语义命中", keyword: "关键词命中", hybrid: "双路命中" }[matchType || ""] || "检索命中");
}

function sourceScoreLabel(source: Source): string {
  if (typeof source.rerankScore === "number") return `重排 ${source.rerankScore.toFixed(2)}`;
  if (typeof source.keywordScore === "number" && source.keywordScore > 0) return `关键词 ${source.keywordScore.toFixed(2)}`;
  return `距离 ${source.distance.toFixed(2)}`;
}

function openSource(source: Source) {
  selectedSource.value = source;
}

function closeSource() {
  selectedSource.value = null;
}

function closeSourceOnEscape(event: KeyboardEvent) {
  if (event.key === "Escape") closeSource();
}

function sourceKey(source: Source): string {
  return `${source.documentId}-${source.position}`;
}

function toggleDocument(documentID: number) {
  if (chatMode.value !== "agent") return;
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
  if (await copyText(source.content)) showCopyFeedback("source", sourceKey(source));
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

async function selectConversation(id: number | null) {
  conversationId.value = id;
  messages.value = [];
  if (!id || !selectedKnowledgeBaseId.value) return;
  const stored = await requestJSON<ConversationMessage[]>(
    `/api/knowledge-bases/${selectedKnowledgeBaseId.value}/conversations/${id}/messages`,
  );
  messages.value = stored.map((message) => ({ role: message.role, content: message.content, status: "done" }));
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
  messages.value.push({ role: "user", content: prompt });
  const answer: ChatMessage = { role: "assistant", content: "", sources: [], status: "streaming" };
  messages.value.push(answer);
  const answerIndex = messages.value.length - 1;
  streaming.value = true;
  try {
    if (useA2AMode) {
      await askA2ATask(prompt, answerIndex);
      return;
    }
    const headers: Record<string, string> = {
      "Content-Type": "application/json",
      Accept: "text/event-stream",
    };
    if (!useResearchMode) headers["Idempotency-Key"] = crypto.randomUUID();
    const streamPath = useResearchMode ? "multi-agent-chat/stream" : "agent-chat/stream";
    const response = await fetch(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/${streamPath}`, {
      method: "POST",
      headers,
      body: JSON.stringify(useResearchMode
        ? { message: prompt, topK: topK.value }
        : {
            message: prompt,
            top_k: topK.value,
            similarity_threshold: similarityThreshold.value,
            document_ids: selectedDocumentIDs.value,
            conversation_id: activeConversationID,
          }),
    });
    if (!response.ok || !response.body) {
      const payload = await response.json().catch(() => null);
      throw new Error(payload?.error || "问答服务暂不可用");
    }
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffer = "";
    while (true) {
      const { value, done } = await reader.read();
      buffer += decoder.decode(value ?? new Uint8Array(), { stream: !done });
      const blocks = buffer.split(/\r?\n\r?\n/);
      buffer = blocks.pop() ?? "";
      blocks.forEach((block) => consumeSSEBlock(block, answerIndex, useResearchMode));
      if (done) break;
    }
    if (buffer.trim()) consumeSSEBlock(buffer, answerIndex, useResearchMode);
    const currentAnswer = messages.value[answerIndex];
    if (currentAnswer?.status === "streaming") {
      currentAnswer.status = "error";
      currentAnswer.activity = "";
      currentAnswer.content = currentAnswer.content || "流式响应提前结束，请重试。";
    }
  } catch (error) {
    const currentAnswer = messages.value[answerIndex];
    if (currentAnswer) {
      currentAnswer.status = "error";
      currentAnswer.content = error instanceof Error ? error.message : "问答失败，请稍后重试。";
    }
    showError(error);
  } finally {
    streaming.value = false;
  }
}

async function askA2ATask(prompt: string, answerIndex: number) {
  if (!selectedKnowledgeBaseId.value) throw new Error("请先选择知识库。");
  const answer = messages.value[answerIndex];
  if (!answer) throw new Error("无法创建问答结果。");

  const task = await requestJSON<A2ATask>("/api/a2a/tasks", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ knowledge_base_id: selectedKnowledgeBaseId.value, message: prompt, top_k: topK.value }),
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
        const result = await requestJSON<{ answer?: string; sources?: Source[] }>(
          `/api/a2a/tasks/${encodeURIComponent(task.id)}/result`,
        );
        answer.content = typeof result.answer === "string" ? result.answer : "";
        answer.sources = mergeSources(answer.sources ?? [], parseSources(result.sources));
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

function recordAgentEvent(answer: ChatMessage, type: string, label: string, detail = "", step?: number, status: AgentEvent["status"] = "done") {
  answer.agentEvents ??= [];
  answer.agentEvents.push({ type, step, label, detail: detail.slice(0, 140), status });
  if (answer.agentEvents.length > 12) answer.agentEvents.shift();
}

function finishLastAgentToolEvent(answer: ChatMessage, detail: string, status: AgentEvent["status"] = "done") {
  const events = answer.agentEvents ?? [];
  const latest = [...events].reverse().find((item) => item.type === "tool_called" && item.status === "running");
  if (latest) {
    latest.label = "知识库检索完成";
    latest.detail = detail.slice(0, 140);
    latest.status = status;
  }
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
    const eventData = payload.data && typeof payload.data === "object" ? payload.data : {};
    const dataString = (key: string) => typeof eventData[key] === "string" ? eventData[key] as string : "";

    switch (event) {
      case "sources":
        answer.sources = mergeSources(answer.sources ?? [], parseSources(payload.sources));
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
          recordAgentEvent(answer, event, "调用知识库工具", toolName, payload.step_number, "running");
        }
        answer.activity = dataString("tool_name") === "knowledge_search" ? "正在查找资料…" : "正在调用工具…";
        break;
      case "tool_finished":
        if (!researchMode) {
          const sources = parseSources(eventData.sources);
          answer.sources = mergeSources(answer.sources ?? [], sources);
          finishLastAgentToolEvent(answer, eventData.no_relevant_results === true ? "没有找到足够相关资料" : `${sources.length || "已"} 条结果已返回`);
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
        answer.content ||= dataString("answer") || payload.answer || "";
        answer.activity = "";
        answer.status = "done";
        break;
      case "conversation_saved":
        answer.activity = "已保存到会话";
        break;
      case "conversation_replayed": {
        const replayed = eventData.response;
        if (replayed && typeof replayed === "object") {
          const replayedAnswer = replayed as Record<string, unknown>;
          answer.content = typeof replayedAnswer.answer === "string" ? replayedAnswer.answer : answer.content;
        }
        answer.activity = "已恢复之前的回答";
        answer.status = "done";
        break;
      }
      case "conversation_save_failed":
        answer.status = "error";
        answer.activity = "";
        answer.content += answer.content ? "\n\n（回答已生成，但保存会话失败）" : "回答已生成，但保存会话失败。";
        break;
      case "run_failed":
      case "error":
        if (!researchMode) {
          for (const agentEvent of answer.agentEvents ?? []) {
            if (agentEvent.status === "running") agentEvent.status = "error";
          }
          recordAgentEvent(answer, event, "Agent 运行失败", dataString("error") || payload.error || "执行失败", payload.step_number, "error");
        }
        answer.status = "error";
        answer.activity = "";
        answer.content = dataString("error") || payload.error || "问答失败。";
        break;
      case "run_canceled":
        if (!researchMode) recordAgentEvent(answer, event, "Agent 已取消", "请求被取消", payload.step_number, "error");
        answer.status = "error";
        answer.activity = "";
        answer.content = "请求已取消。";
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
          <button class="quiet-button" :disabled="loadingDocuments" @click="refreshDocuments">↻ 刷新</button>
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
                <input type="checkbox" :checked="selectedDocumentIDs.includes(document.id)" :disabled="streaming || chatMode !== 'agent'" @change="toggleDocument(document.id)" />
                <span class="visually-hidden">选择 {{ document.originalFilename }}</span>
              </label>
              <span class="file-icon">{{ document.contentType === "application/pdf" ? "PDF" : "TXT" }}</span>
              <div class="document-copy"><strong>{{ document.originalFilename }}</strong><span>{{ formatBytes(document.sizeBytes) }} · #{{ document.id }}</span></div>
              <span class="processing-status" :class="`processing-status--${document.processingStatus}`"><i />{{ statusLabel(document.processingStatus) }}</span>
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
            <div><strong>检索范围</strong><span>{{ chatMode === 'agent' && selectedDocumentIDs.length ? `标准 Agent 仅检索 ${selectedDocumentIDs.length} 份已选文档；` : "当前模式检索整个知识库；" }}控制召回数量和证据相关度</span></div>
            <label>召回片段数<select v-model.number="topK" :disabled="streaming"><option v-for="value in [3, 5, 8, 12, 20]" :key="value" :value="value">{{ value }} 条</option></select></label>
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
                <div
                  v-for="item in conversations"
                  :key="item.id"
                  class="conversation-item"
                  :class="{ 'conversation-item--active': item.id === conversationId }"
                  @click="selectConversation(item.id)"
                >
                  <span class="conversation-item-title">{{ item.title }}</span>
                  <small>{{ new Date(item.updatedAt).toLocaleDateString("zh-CN") }}</small>
                  <div class="conversation-actions">
                    <button type="button" aria-label="重命名会话" :disabled="conversationsLoading || streaming" @click.stop="renameConversation(item)">改名</button>
                    <button type="button" aria-label="删除会话" :disabled="conversationsLoading || streaming" @click.stop="deleteConversation(item)">删</button>
                  </div>
                </div>
            </div>
          </template>
          <div class="messages" aria-live="polite">
            <div v-if="!messages.length" class="chat-empty"><span>“</span><p>问一个关于这套资料的问题，<br />让线索自己浮上来。</p></div>
            <article v-for="(message, index) in messages" :key="index" class="message" :class="`message--${message.role}`">
              <div class="message-label">{{ message.role === "user" ? "你" : "文库助手" }}</div>
              <div v-if="message.role === 'assistant' && message.status === 'streaming' && message.activity" class="message-activity">
                <span class="message-activity-dot" />
                <span>{{ message.activity }}</span>
              </div>
              <div v-if="message.role === 'assistant' && message.agentEvents?.length" class="agent-trace">
                <div class="agent-trace-head"><span>Agent 运行轨迹</span><small>{{ message.agentEvents.length }} EVENTS</small></div>
                <div v-for="trace in message.agentEvents" :key="`${trace.type}-${trace.step}-${trace.label}-${trace.detail}`" class="agent-trace-row" :class="`agent-trace-row--${trace.status}`">
                  <span class="agent-trace-marker">{{ trace.status === 'done' ? '✓' : trace.status === 'error' ? '!' : '·' }}</span>
                  <span class="agent-trace-label">{{ trace.label }}</span>
                  <small v-if="trace.detail">{{ trace.detail }}</small>
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
                <span v-else>{{ message.content }}</span>
              </div>
              <div v-if="message.role === 'assistant' && message.content && message.status !== 'streaming'" class="message-actions">
                <button type="button" @click="copyAnswer(message, index)">{{ copiedMessageIndex === index ? "已复制回答" : "复制回答" }}</button>
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
            <div class="composer-footer"><span>Enter 发送 · Shift + Enter 换行</span><button class="send-button" type="submit" :disabled="!question.trim() || streaming || !selectedKnowledgeBase" aria-label="发送问题">↗</button></div>
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
        <div class="source-panel-content"><p>{{ selectedSource.content }}</p></div>
        <div class="source-panel-actions"><button class="source-copy-button" type="button" @click="copySource(selectedSource)">{{ copiedSourceKey === sourceKey(selectedSource) ? "已复制原文" : "复制原文" }}</button></div>
        <p class="source-panel-note">这段内容来自知识库检索结果，仅作为回答依据展示，不会被当作操作指令执行。</p>
      </aside>
    </div>
  </div>
</template>
