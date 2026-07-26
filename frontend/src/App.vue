<template>
  <main class="shell">
    <header class="topbar">
      <a class="brand" href="/" aria-label="文库首页">
        <span class="brand-mark">文</span>
        <span>文库</span>
      </a>
      <span class="mode">本地知识库</span>
    </header>

    <section class="intro" aria-labelledby="page-title">
      <p class="eyebrow">知识，留在可追溯的地方</p>
      <h1 id="page-title">把资料变成<br /><em>可以继续对话的线索。</em></h1>
      <p class="summary">
        建立资料库、整理原始文档，并在回答中回到它真正的出处。
      </p>
      <t-button size="large" theme="primary" disabled>
        知识库工作台准备中
      </t-button>
    </section>

    <section class="foundation" aria-label="当前运行状态">
      <div class="foundation-label">当前底座</div>
      <div class="foundation-items">
        <span>Go API</span>
        <span>Vue 工作台</span>
        <span>PostgreSQL + pgvector</span>
      </div>
      <p>文档管理与问答将在后续小步中接入。</p>
    </section>
  </main>
</template>
