# ADR-005：采用 DeerFlow 风格的 Skill 渐进式披露

## 状态

Accepted

## 背景

Agent 的能力说明如果全部放进首轮 system prompt，会增加每次模型调用的上下文体积，也会把不相关的操作说明暴露给模型。DeerFlow 将 Skill 组织为目录包：`SKILL.md` 保存说明和元数据，脚本、参考资料和资源可以放在同一目录中，并通过渐进式披露按需加载。

## 决策

本项目采用文件目录作为第一版 Skill Catalog：

- 每个 Skill 位于一个目录中，必须包含带 YAML frontmatter 的 `SKILL.md`。
- 服务启动时只解析 `name`、`description`、`license` 和 `allowed-tools`，生成稳定排序的 `<skill_index>`。
- Agent 需要专门能力时先调用 `skill_describe`，再调用 `skill_read` 按名称加载正文；工具不接受任意文件路径。
- `skill_read` 成功后，运行状态只保存 Skill 名称并发布 `skill_loaded` 事件；正文不复制到 Agent Run 持久化状态。
- Skill 目录缺失时按空 Catalog 启动；目录存在但定义非法时启动失败，避免静默加载错误权限。
- 当前 `AGENT_SKILLS_DIR` 默认指向 `./skills`，也可以通过环境变量替换。后续再增加用户级目录、脚本执行和更细的工具权限策略。

## 备选方案

### 把全部 Skill 正文固定拼入 system prompt

实现简单，但每次请求都会携带大量无关说明，并且不利于 prompt cache，因此不采用。

### 让模型直接传文件路径读取

接口看似灵活，但会引入路径穿越和读取宿主机任意文件的风险，因此不采用。

### 首版直接接入数据库 Skill 表

数据库适合后续做用户级、自定义 Skill 管理，但当前本地文件目录更接近 DeerFlow 的包结构，也便于版本控制和复现，因此暂不引入数据库迁移。

## 影响

- 首轮 prompt 只增加稳定的 Skill 名称和短描述。
- Skill 正文和未来的 `scripts/`、`references/`、`assets/` 可以按名称继续扩展。
- 工具权限字段已进入契约和模型可见元数据；实际 Skill 脚本工具的授权过滤留到后续接入脚本执行时实现。
- `skill_loaded` 事件可以进入现有 Hub/Redis/PostgreSQL 事件链路，前端可显示“已加载 Skill”。
