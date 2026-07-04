package commands

import (
	"flag"
	"os"
	"strings"
)

const (
	codexThreadIDEnv        = "CODEX_THREAD_ID"
	agentSessionParamSource = "agent-session-parameter"
)

type writerSession struct {
	ID     string
	Source string
}

func addAgentSessionFlag(fs *flag.FlagSet) *string {
	return fs.String("agent-session", "", "artifact writer session id; CODEX_THREAD_ID takes precedence when present, otherwise this explicit value is recorded with source agent-session-parameter")
}

func resolveWriterSession(explicit string) writerSession {
	if value := strings.TrimSpace(os.Getenv(codexThreadIDEnv)); value != "" {
		return writerSession{ID: value, Source: codexThreadIDEnv}
	}
	if value := strings.TrimSpace(explicit); value != "" {
		return writerSession{ID: value, Source: agentSessionParamSource}
	}
	return writerSession{}
}
