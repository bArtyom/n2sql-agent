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
};

const knowledgeBases = ref<KnowledgeBase[]>([]);
const selectedKnowledgeBaseId = ref<number | null>(null);
const documents = ref<DocumentItem[]>([]);
const messages = ref<ChatMessage[]>([]);
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
let documentPollTimer: number | undefined;

const selectedKnowledgeBase = computed(() =>
  knowledgeBases.value.find((item) => item.id === selectedKnowledgeBaseId.value) ?? null,
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
    throw new Error(payload?.error || `请求失败（${response.status}）`);
  }
  return payload as T;
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

function selectKnowledgeBase(id: number) {
  selectedKnowledgeBaseId.value = id;
  mobileRailOpen.value = false;
  messages.value = [];
  void refreshDocuments();
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
    documents.value = [];
    mobileRailOpen.value = false;
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
  question.value = "";
  messages.value.push({ role: "user", content: prompt });
  const answer: ChatMessage = { role: "assistant", content: "", sources: [], status: "streaming" };
  messages.value.push(answer);
  streaming.value = true;
  try {
    const response = await fetch(`/api/knowledge-bases/${selectedKnowledgeBaseId.value}/chat/stream`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Accept: "text/event-stream" },
      body: JSON.stringify({ message: prompt, topK: 5 }),
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
      blocks.forEach((block) => consumeSSEBlock(block, answer));
      if (done) break;
    }
    if (buffer.trim()) consumeSSEBlock(buffer, answer);
    if (answer.status === "streaming") answer.status = "done";
  } catch (error) {
    answer.status = "error";
    answer.content = error instanceof Error ? error.message : "问答失败，请稍后重试。";
    showError(error);
  } finally {
    streaming.value = false;
  }
}

function consumeSSEBlock(block: string, answer: ChatMessage) {
  let event = "message";
  const dataLines: string[] = [];
  for (const line of block.split(/\r?\n/)) {
    if (line.startsWith("event:")) event = line.slice(6).trim();
    if (line.startsWith("data:")) dataLines.push(line.slice(5).trimStart());
  }
  if (!dataLines.length) return;
  try {
    const payload = JSON.parse(dataLines.join("\n")) as { delta?: string; sources?: Source[]; error?: string };
    if (event === "sources") answer.sources = payload.sources ?? [];
    if (event === "delta") answer.content += payload.delta ?? "";
    if (event === "error") {
      answer.status = "error";
      answer.content = payload.error || "问答失败。";
    }
    if (event === "done") answer.status = "done";
  } catch {
    answer.status = "error";
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

onMounted(() => void loadKnowledgeBases());
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
          <div class="upload-zone" :class="{ 'upload-zone--busy': uploading }" @dragover.prevent @drop.prevent="onDrop" @click="openFilePicker">
            <input ref="fileInput" class="visually-hidden" type="file" accept=".md,.txt,.pdf" multiple @change="onFileChange" />
            <span class="upload-icon">↑</span>
            <div><strong>{{ uploading ? "正在接收资料…" : "拖入文件，或点击上传" }}</strong><span>支持 Markdown、TXT、PDF · 单文件不超过 10 MB</span></div>
            <span class="upload-shortcut">⌘ U</span>
          </div>
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
          <div class="messages" aria-live="polite">
            <div v-if="!messages.length" class="chat-empty"><span>“</span><p>问一个关于这套资料的问题，<br />让线索自己浮上来。</p></div>
            <article v-for="(message, index) in messages" :key="index" class="message" :class="`message--${message.role}`">
              <div class="message-label">{{ message.role === "user" ? "你" : "文库助手" }}</div>
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
  </div>
</template>
