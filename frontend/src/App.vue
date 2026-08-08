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
};
type ChatMessage = {
  role: "user" | "assistant";
  content: string;
  sources?: Source[];
  status?: "streaming" | "done" | "error";
  activity?: string;
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
};
type ModelProvider = {
  name: string;
  baseUrl: string;
  apiKeyEnvVar: string;
  chatModel: string;
  embeddingModel: string;
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
let documentPollTimer: number | undefined;

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
  selectedKnowledgeBaseId.value = id;
  mobileRailOpen.value = false;
  messages.value = [];
  conversationId.value = null;
  void refreshDocuments();
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
    newKnowledgeBaseName.value = "";
    newKnowledgeBaseDescription.value = "";
    messages.value = [];
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
  let activeConversationID: number;
  try {
    activeConversationID = await ensureConversation(prompt);
  } catch (error) {
    showError(error);
    return;
  }
  question.value = "";
  messages.value.push({ role: "user", content: prompt });
  const answer: ChatMessage = { role: "assistant", content: "", sources: [], status: "streaming" };
  messages.value.push(answer);
  const answerIndex = messages.value.length - 1;
  streaming.value = true;
  try {
    const response = await fetch(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/agent-chat/stream`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Accept: "text/event-stream" },
      body: JSON.stringify({ message: prompt, conversation_id: activeConversationID }),
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
      blocks.forEach((block) => consumeSSEBlock(block, answerIndex));
      if (done) break;
    }
    if (buffer.trim()) consumeSSEBlock(buffer, answerIndex);
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
        answer.activity = "正在理解问题…";
        break;
      case "tool_called":
        answer.activity = dataString("tool_name") === "knowledge_search" ? "正在查找资料…" : "正在调用工具…";
        break;
      case "tool_finished":
        if (Object.prototype.hasOwnProperty.call(eventData, "sources")) {
          answer.sources = mergeSources(answer.sources ?? [], parseSources(eventData.sources));
        }
        answer.activity = "资料查找完成，正在组织答案…";
        break;
      case "message_delta":
        answer.content += dataString("content") || payload.content || "";
        answer.activity = "正在组织答案…";
        break;
      case "run_finished":
        answer.content ||= dataString("answer") || payload.answer || "";
        answer.activity = "";
        answer.status = "done";
        break;
      case "conversation_saved":
        answer.activity = "已保存到会话";
        break;
      case "conversation_save_failed":
        answer.status = "error";
        answer.activity = "";
        answer.content += answer.content ? "\n\n（回答已生成，但保存会话失败）" : "回答已生成，但保存会话失败。";
        break;
      case "run_failed":
      case "error":
        answer.status = "error";
        answer.activity = "";
        answer.content = dataString("error") || payload.error || "问答失败。";
        break;
      case "run_canceled":
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

onMounted(() => void loadKnowledgeBases().then(() => loadConversation()));
onUnmounted(() => window.clearInterval(documentPollTimer));
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
              <span class="file-icon">{{ document.contentType === "application/pdf" ? "PDF" : "TXT" }}</span>
              <div class="document-copy"><strong>{{ document.originalFilename }}</strong><span>{{ formatBytes(document.sizeBytes) }} · #{{ document.id }}</span></div>
              <span class="processing-status" :class="`processing-status--${document.processingStatus}`"><i />{{ statusLabel(document.processingStatus) }}</span>
            </li>
          </ul>
        </div>

        <div class="chat-panel panel-card">
          <div class="panel-heading">
            <div><span class="section-index">02</span><h2>问答台</h2></div>
            <span class="panel-meta">STREAMING</span>
          </div>
          <div class="chat-intro"><span class="chat-spark">✦</span><div><strong>从资料里找答案</strong><p>回答会标记它引用的原始段落。</p></div></div>
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
            <button
              v-for="item in conversations"
              :key="item.id"
              type="button"
              class="conversation-item"
              :class="{ 'conversation-item--active': item.id === conversationId }"
              :disabled="conversationsLoading || streaming"
              @click="selectConversation(item.id)"
            >
              <span>{{ item.title }}</span>
              <small>{{ new Date(item.updatedAt).toLocaleDateString("zh-CN") }}</small>
            </button>
          </div>
          <div class="messages" aria-live="polite">
            <div v-if="!messages.length" class="chat-empty"><span>“</span><p>问一个关于这套资料的问题，<br />让线索自己浮上来。</p></div>
            <article v-for="(message, index) in messages" :key="index" class="message" :class="`message--${message.role}`">
              <div class="message-label">{{ message.role === "user" ? "你" : "文库助手" }}</div>
              <div v-if="message.role === 'assistant' && message.status === 'streaming' && message.activity" class="message-activity">
                <span class="message-activity-dot" />
                <span>{{ message.activity }}</span>
              </div>
              <div class="message-bubble" :class="{ 'message-bubble--error': message.status === 'error' }">
                <span v-if="message.role === 'assistant' && !message.content && message.status === 'streaming'" class="typing"><i /><i /><i /></span>
                <span v-else>{{ message.content }}</span>
              </div>
              <div v-if="message.role === 'assistant' && message.sources?.length" class="sources">
                <span class="sources-label">引用 {{ message.sources.length }}</span>
                <details v-for="source in message.sources" :key="`${source.documentId}-${source.position}`">
                  <summary>{{ source.originalFilename || "未命名文档" }} · 第 {{ source.position + 1 }} 段</summary>
                  <p>{{ source.content }}</p>
                </details>
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
          <div class="secret-note"><span class="secret-note-mark">⌘</span><div><strong>API Key 不在这里输入</strong><p>后端只读取 `.env` 中的 <code>{{ providerForm.apiKeyEnvVar }}</code>，页面不会显示或保存密钥。</p></div></div>
          <p v-if="providerMessage" class="provider-message" :class="`provider-message--${providerMessageKind}`" role="status">{{ providerMessage }}</p>
          <div class="settings-actions"><button class="settings-secondary" type="button" :disabled="providerTesting || providerSaving" @click="testProviderConnection">{{ providerTesting ? "测试中…" : "测试连接" }}</button><button class="settings-primary" type="submit" :disabled="providerSaving || providerTesting">{{ providerSaving ? "保存中…" : "保存配置" }}</button></div>
        </form>
      </section>
    </div>
  </div>
</template>
