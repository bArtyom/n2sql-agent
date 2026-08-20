package agent

// The built-in knowledge tools are read-only and each call is scoped by its
// arguments, so multiple calls from one model response are independent.
func (*KnowledgeSearchTool) ParallelSafe() bool { return true }
func (*DocumentReadTool) ParallelSafe() bool    { return true }
func (*DocumentListTool) ParallelSafe() bool    { return true }
func (*DocumentInfoTool) ParallelSafe() bool    { return true }
func (*DocumentSummaryTool) ParallelSafe() bool { return true }
