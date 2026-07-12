package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/higress-group/issue-spec/internal/auth"
	changegraph "github.com/higress-group/issue-spec/internal/change"
	"github.com/higress-group/issue-spec/internal/reconcile"
)

func (a *app) runWorkflowReconcile(ctx context.Context, args []string) int {
	fs := newFlagSet("workflow reconcile", a.err)
	planPath := fs.String("plan", "", "versioned reconcile plan JSON file, or - for stdin")
	checkpoint := fs.String("checkpoint", "", "durable checkpoint JSON path")
	host := fs.String("hostname", "", "issue backend hostname override")
	jsonOut := fs.Bool("json", false, "write JSON output")
	if ok, code := a.parseFlagSet(fs, args); !ok {
		return code
	}
	if strings.TrimSpace(*planPath) == "" {
		a.errorf("--plan is required\n")
		return 2
	}
	plan, err := readReconcilePlan(*planPath, a.in)
	if err != nil {
		a.errorf("read reconcile plan: %v\n", err)
		return 2
	}
	if _, ok := a.validateRepo(plan.Repo); !ok {
		return 2
	}
	if *host == "" {
		*host = plan.Hostname
	}
	if *host == "" {
		*host = "github.com"
	}
	client, _, err := a.clientFor(ctx, *host)
	if err != nil {
		a.errorf("auth required for workflow reconcile on %s: %v\n", auth.NormalizeHost(*host), err)
		return 1
	}
	var located changegraph.Located
	if plan.Proposal > 0 {
		located, err = changegraph.Locate(ctx, client, plan.Repo, plan.Proposal)
		if err != nil {
			a.errorf("locate change: %v\n", err)
			return 1
		}
		if err := resolvePlanRoles(&plan, located); err != nil {
			a.errorf("resolve plan target: %v\n", err)
			return 2
		}
	} else if hasPlanRoles(plan) {
		a.errorf("plan targets use roles but proposal is absent\n")
		return 2
	}
	result, err := (reconcile.Engine{Backend: client}).Run(ctx, plan, *checkpoint)
	if located.Change != "" {
		result.Change = located
	}
	if err != nil {
		a.errorf("workflow reconcile: %v\n", err)
		return 1
	}
	if *jsonOut {
		if code := a.outputJSON(result); code != 0 {
			return code
		}
	} else {
		fmt.Fprintf(a.out, "reconcile %s: created=%d updated=%d unchanged=%d conflicted=%d pending=%d atomic=%v\n", result.PlanDigest, result.Created, result.Updated, result.Unchanged, result.Conflicted, result.Pending, result.Atomic)
	}
	if !result.OK {
		return 1
	}
	return 0
}

func readReconcilePlan(path string, stdin io.Reader) (reconcile.Plan, error) {
	var reader io.Reader
	base := "."
	if path == "-" {
		reader = stdin
	} else {
		file, err := os.Open(path)
		if err != nil {
			return reconcile.Plan{}, err
		}
		defer file.Close()
		reader, base = file, filepath.Dir(path)
	}
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	var plan reconcile.Plan
	if err := decoder.Decode(&plan); err != nil {
		return plan, err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return plan, fmt.Errorf("plan contains trailing JSON")
	}
	for i := range plan.Operations {
		desired := &plan.Operations[i].Desired
		if desired.BodyFile == "" {
			continue
		}
		if desired.Body != "" {
			return plan, fmt.Errorf("operation %s declares both body and body_file", plan.Operations[i].ID)
		}
		bodyPath := desired.BodyFile
		if !filepath.IsAbs(bodyPath) {
			bodyPath = filepath.Join(base, bodyPath)
		}
		data, err := os.ReadFile(bodyPath)
		if err != nil {
			return plan, fmt.Errorf("operation %s body_file: %w", plan.Operations[i].ID, err)
		}
		desired.Body, desired.BodyFile = string(data), ""
	}
	return plan, nil
}

func hasPlanRoles(plan reconcile.Plan) bool {
	for _, op := range plan.Operations {
		if op.Target.Role != "" || (op.Desired.Peer != nil && op.Desired.Peer.Role != "") {
			return true
		}
	}
	return false
}
func resolvePlanRoles(plan *reconcile.Plan, located changegraph.Located) error {
	resolve := func(target *reconcile.Target) error {
		if target.Role == "" {
			return nil
		}
		switch strings.ToLower(target.Role) {
		case "proposal":
			target.Issue = located.Proposal.Number
		case "design":
			target.Issue = located.Design.Number
		case "implement":
			target.Issue = located.Implement.Number
		default:
			return fmt.Errorf("unknown role %q", target.Role)
		}
		target.Role = ""
		return nil
	}
	for i := range plan.Operations {
		if err := resolve(&plan.Operations[i].Target); err != nil {
			return err
		}
		if plan.Operations[i].Desired.Peer != nil {
			if err := resolve(plan.Operations[i].Desired.Peer); err != nil {
				return err
			}
		}
	}
	return nil
}
