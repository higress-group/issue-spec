package commands

import (
	"context"
	"fmt"

	"github.com/higress-group/issue-spec/internal/feedback"
)

func (a *app) runFeedback(ctx context.Context, args []string) int {
	if len(args) == 0 || isHelpArg(args[0]) {
		fmt.Fprintln(a.out, "Usage: issue-spec feedback configure|record [options]")
		return 0
	}
	action := args[0]
	if action != "configure" && action != "record" {
		a.errorf("unknown feedback command %q\n", action)
		return 2
	}

	fs := newFlagSet("feedback "+action, a.err)
	repoFlag := fs.String("repo", "", "target Issue repository owner/name")
	host := fs.String("hostname", "github.com", "issue backend hostname")
	issue := fs.Int("issue", 0, "positive Issue number in the selected repository")
	dryRun := fs.Bool("dry-run", false, "preview using read-only backend operations")
	jsonOut := fs.Bool("json", false, "write JSON output")
	var eventFile *string
	if action == "record" {
		eventFile = fs.String("event-file", "", "JSON occurrence file, or - for stdin")
	}
	if ok, code := a.parseFlagSet(fs, args[1:]); !ok {
		return code
	}
	repo, ok := a.validateRepo(*repoFlag)
	if !ok {
		return 2
	}
	if *issue <= 0 {
		a.errorf("--issue must be a positive Issue number in the selected repository\n")
		return 2
	}
	var event feedback.Event
	if eventFile != nil {
		input, ok := a.readFlagFile(*eventFile, "event-file")
		if !ok {
			return 2
		}
		parsed, err := feedback.ParseEvent(input)
		if err != nil {
			a.errorf("%v\n", err)
			return 2
		}
		event = parsed
	}

	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("select feedback Issue backend: %v\n", err)
		return 1
	}
	backend, ok := client.(feedback.Backend)
	if !ok {
		a.errorf("the selected Issue backend does not support feedback label operations\n")
		return 1
	}
	if action == "configure" {
		result, err := feedback.Configure(ctx, backend, repo, *issue, *dryRun)
		if err != nil {
			a.errorf("%v\n", err)
			return 1
		}
		if *jsonOut {
			return a.outputJSON(result)
		}
		status := "published"
		if *dryRun {
			status = "preview"
		}
		fmt.Fprintf(a.out, "%s %s feedback policy from Issue #%d (%s, threshold %d)\n",
			status, repo, *issue, result.Policy.Mode, result.Policy.Threshold)
		return 0
	}

	result, err := feedback.Record(ctx, backend, repo, *issue, event, *dryRun)
	if err != nil {
		a.errorf("%v\n", err)
		return 1
	}
	if *jsonOut {
		return a.outputJSON(result)
	}
	fmt.Fprintf(a.out, "%s %s#%d %s\n%d confirmed occurrences, promotion %s (%s)\n",
		result.EventStatus, repo, *issue, result.EventKey, result.ConfirmedCount, result.Promotion.Status, result.Promotion.Label)
	if result.Promotion.Reason != "" {
		fmt.Fprintln(a.out, result.Promotion.Reason)
	}
	return 0
}
