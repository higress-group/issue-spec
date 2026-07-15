package commands

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/higress-group/issue-spec/internal/commentrunner"
	runnercontext "github.com/higress-group/issue-spec/internal/commentrunner/context"
	"github.com/higress-group/issue-spec/internal/commentrunner/diagnostics"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/writeback"
)

func TestRunnerLoggerKeepsConcurrentJobCorrelationIsolated(t *testing.T) {
	root := t.TempDir()
	logger, err := newRunnerLogger(commentrunner.Config{
		Hostname: "github.example.test", Repositories: []string{"owner/repo"}, RunnerIdentity: "runner",
		StatePath: filepath.Join(root, "state.json"), LogDir: filepath.Join(root, "logs"),
		LogMaxSizeMB: 1, LogMaxFiles: 2, LogRetentionDays: 30, LogRawCaptureKB: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.close() })

	jobs := []state.Job{
		{ID: "job-a", PublicSessionID: "session-a", TriggerCommentID: 101, StatusCommentID: 201,
			Workspace: state.WorkspaceMetadata{ID: "workspace-a"}, AcpxRecordID: "record-a",
			DispatchIntent: state.DispatchIntent{TurnCorrelationToken: "turn-a"},
			Acpx:           state.AcpxMetadata{LastTurnID: "turn-a"}},
		{ID: "job-b", PublicSessionID: "session-b", TriggerCommentID: 102, StatusCommentID: 202,
			Workspace: state.WorkspaceMetadata{ID: "workspace-b"}, AcpxRecordID: "record-b",
			DispatchIntent: state.DispatchIntent{TurnCorrelationToken: "turn-b"},
			Acpx:           state.AcpxMetadata{LastTurnID: "turn-b"}},
	}
	var wg sync.WaitGroup
	errs := make(chan error, len(jobs))
	for _, job := range jobs {
		job := job
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- logger.logJobEvent(job, diagnostics.Correlation{DeliveryID: "delivery-" + job.ID},
				diagnostics.LevelInfo, "job_started", "runner job started", nil, "", "")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	for _, job := range jobs {
		path := filepath.Join(root, "logs", "jobs", job.ID+".ndjson")
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		scanner := bufio.NewScanner(file)
		count := 0
		for scanner.Scan() {
			var event diagnostics.Event
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			count++
			if event.Correlation.JobID != job.ID || event.Correlation.PublicSessionID != job.PublicSessionID ||
				event.Correlation.WorkspaceID != job.Workspace.ID ||
				event.Correlation.DeliveryID != "delivery-"+job.ID {
				_ = file.Close()
				t.Fatalf("job %s received cross-job correlation: %+v", job.ID, event.Correlation)
			}
		}
		if err := scanner.Err(); err != nil {
			_ = file.Close()
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("job %s event count=%d want=1", job.ID, count)
		}
	}
}

func TestRunnerLoggerBoundsAndRedactsCoordinatorDiagnostics(t *testing.T) {
	root := t.TempDir()
	logger, err := newRunnerLogger(commentrunner.Config{
		Hostname: "github.example.test", Repositories: []string{"owner/repo"}, RunnerIdentity: "runner",
		StatePath: filepath.Join(root, "state.json"), LogDir: filepath.Join(root, "logs"),
		LogMaxSizeMB: 1, LogMaxFiles: 2, LogRetentionDays: 30, LogRawCaptureKB: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.close() })

	secret := "abcdefghijklmnopqrstuvwxyz0123456789"
	summary := &runnercontext.CoordinatorSummary{Status: "failed"}
	for index := 0; index < maxCoordinatorDiagnosticEntries+8; index++ {
		summary.Diagnostics = append(summary.Diagnostics, runnercontext.DiagnosticSummary{
			Severity: strings.Repeat("warning", 10),
			Message:  "token=" + secret + " " + strings.Repeat("diagnostic ", 200),
		})
	}
	request := writeback.Request{Job: state.Job{ID: "job-summary"}, Status: state.StatusFailed,
		CoordinatorSummary: summary}
	if err := logger.logJobLifecycle(request, writeback.Result{}, nil); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(root, "logs", "jobs", "job-summary.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || !strings.Contains(string(data), "[REDACTED:token]") {
		t.Fatalf("coordinator diagnostic redaction failed: %s", data)
	}
	var event diagnostics.Event
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatal(err)
	}
	entries, ok := event.Details["summary_diagnostic_entries"].([]interface{})
	if !ok || len(entries) == 0 || len(entries) > maxCoordinatorDiagnosticEntries {
		t.Fatalf("bounded diagnostic entries=%T %v", event.Details["summary_diagnostic_entries"], entries)
	}
	total := 0
	for _, raw := range entries {
		entry, ok := raw.(map[string]interface{})
		if !ok {
			t.Fatalf("diagnostic entry=%T", raw)
		}
		severity, _ := entry["severity"].(string)
		message, _ := entry["message"].(string)
		if len([]byte(severity)) > maxCoordinatorDiagnosticSeverityBytes ||
			len([]byte(message)) > maxCoordinatorDiagnosticMessageBytes {
			t.Fatalf("unbounded diagnostic entry=%v", entry)
		}
		total += len([]byte(severity)) + len([]byte(message))
	}
	if total > maxCoordinatorDiagnosticsTotalBytes || event.Details["summary_diagnostics_truncated"] != true {
		t.Fatalf("diagnostic total=%d truncated=%v", total, event.Details["summary_diagnostics_truncated"])
	}
}
