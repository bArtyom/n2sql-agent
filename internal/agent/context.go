package agent

const (
	// DefaultMaxHistoryMessages limits how many client-provided messages enter one Agent run.
	DefaultMaxHistoryMessages = 10
	// DefaultMaxHistoryBytes limits the UTF-8 content bytes retained from conversation history.
	DefaultMaxHistoryBytes = 16 * 1024
)
