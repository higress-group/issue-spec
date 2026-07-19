package commands

import (
	"flag"
)

type writerSession struct {
	ID     string
	Source string
}

func addAgentSessionFlag(fs *flag.FlagSet) *string {
	return fs.String("agent-session", "", "deprecated compatibility flag; ignored")
}

func resolveWriterSession(_ string) writerSession {
	return writerSession{}
}
