# ADR-006（已被 ADR-007 取代）

本文件保留为历史决策记录。原先将线程上下文、运行上下文、模型决策和工具结果拆成多类 checkpoint 的方案已经删除，不再作为当前实现依据。

当前统一采用 [ADR-007](ADR-007-DeerFlow风格统一Checkpoint.md) 定义的单一 `agent_checkpoints` 状态快照。
