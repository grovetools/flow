package orchestration

// AgentJobTemplate was previously used for text/template-based agent job serialization.
// It has been removed in favor of yaml.Marshal on the Job struct, which uses struct tags
// as the single source of truth for serialization. See generateJobContent() in directory.go.
