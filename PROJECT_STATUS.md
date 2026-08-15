# n2sql-agent 项目交接状态

> 这是新 Agent 开始工作时的第一份状态摘要。详细设计和学习笔记仍在 `docs/`；本文件只保留当前边界、已完成能力、明确后置项和下一步工作方式。

## 先读什么

1. `AGENTS.md`：必须遵守的开发规范、学习式提问、验证和 Git 约定。
2. `PROJECT_STATUS.md`：当前交接状态和范围边界。
3. `docs/plans/总体计划.md`：长期目标和阶段关系。
4. `docs/plans/第二阶段-Agent平台与后端工程化.md`：Agent 主线的实施顺序。
5. `docs/conversations/` 中与当前切片相关的最新记录：实现原因、用户确认和未决问题。
6. `README.md`：启动方式和可运行接口。

## 项目边界

- `n2sql-agent` 是唯一的独立实现目录；`WeKnora` 只作为架构和产品参考，不直接修改或复制其源码。
- 当前目标是通用文档知识库问答，不引入 SQL/schema/N2SQL 问答主题。
- 后端使用 Go，前端使用 Vue 3 + TypeScript + Vite，数据使用 PostgreSQL + pgvector，PostgreSQL 由 Docker Compose 管理。
- 模型 API Key 只放后端 `.env`，不进入浏览器、数据库、响应或日志。
- 开发采用“学习优先、功能切片优先”：先做一个用户可见的最小闭环，再做必要的构建、静态检查和冒烟验证；用户已明确暂缓大规模评测和测试套件。

## 当前代码状态

- 最新提交：`1cb41c2 feat: pin conversations and group list by date`。
- 当前工作区在该提交后保持干净。
- 第一阶段知识库问答底座已完成：知识库、文档上传和 Worker、Markdown/TXT/PDF 提取、OCR 最小骨架、父子 chunk、embedding、混合检索、Rerank、引用和普通 RAG/SSE。
- Agent Runtime 最小闭环已完成：Tool Registry、Function Calling、受限 ReAct、最大步数/超时/取消、工具失败安全降级、SSE 事件、上下文摘要、会话历史、幂等和运行摘要。
- Agent 只读工具已覆盖 `knowledge_search`、`document_list`、`document_info`、`document_read`；文档正文读取受知识库、文档、chunk 数量和字节数限制。
- 检索已具备向量 + PostgreSQL 关键词、RRF 融合、关键词阈值、可选 Rerank、Query Rewrite、缓存、父块上下文去重、HNSW 和检索统计。
- Multi-Agent、只读 MCP 和 PostgreSQL 持久化 A2A 已有最小可运行适配；它们不是完整官方协议或生产级多租户方案。
- 前端已支持会话、引用卡片与懒加载原文、检索统计、Agent 工具轨迹折叠、断线恢复、A2A/协作研究模式、正文分页预览、固定起步问题和按需生成追问建议。
- 最新停止生成切片：标准 Agent 流式回答期间输入栏显示“停止生成”按钮，调用 `POST /api/knowledge-bases/{id}/agent-runs/{runID}/stop` 取消执行上下文；引擎发 `run_canceled` 事件，前端标记独立 stopped 终态（保留部分内容、不显示重新生成、不写会话历史）；断线恢复与用户停止语义分离；停止按知识库 ID 隔离。
- 最新会话置顶切片：`conversations.is_pinned` 列 + 排序索引；列表按置顶优先、组内按更新时间排序；`PATCH /conversations/{id}` 支持 `{"is_pinned": true/false}`（与重命名共用端点，跨库 404）；前端会话列表按「置顶 + 今天/昨天/N 天前」分组，置顶项 📌 标记与置顶/取消置顶按钮。

## 明确后置或不做

- 暂不把 Redis、跨实例事件持久化、完整 MCP/A2A 规范、复杂沙箱、生产级认证和多租户引入当前主线。
- 暂不实现成功回答后的编辑问题/覆盖式重新生成；这是已确认的范围决定，不能自行恢复。
- 暂不把大规模评测、真实压测和完整测试套件作为 Agent/RAG 功能推进的阻塞项；只保留与改动匹配的必要验证。
- OCR 只保留最小接入和一次验证，不扩展成完整文档解析平台。

## 下一步工作方式

默认继续沿着 WeKnora 对照后的 Agent/RAG 用户可见能力推进：先阅读相关现有代码和 `docs/` 记录，选择一个小而完整的功能切片，再实现后端链路、前端反馈和必要文档。不要因为看到后置项就直接开始 Redis、完整协议或大规模评测。

下一轮候选（按对照盘点顺序）：会话自动标题（#3）、追问建议“换一批”（#4）、会话更多菜单（#7，清空/复制 Markdown）、正文内引用标记（#5，中规模）。

## 交付要求

- 修改前先检查 `AGENTS.md`、相关计划、当前实现和参考功能。
- 技术问题先改写成更清晰的问题再回答；开发相关问答和关键决策追加到 `docs/conversations/`。
- 代码保持职责清晰、错误显式、输入和权限有边界；前端改动做真实浏览器冒烟。
- 完成一个可验证切片后运行匹配的验证，检查 `git diff`，创建独立的可读提交。
