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
	"github.com/higress-group/issue-spec/internal/commentrunner/intake"
	webhook "github.com/higress-group/issue-spec/internal/commentrunner/intake/webhook"
	crjobs "github.com/higress-group/issue-spec/internal/commentrunner/jobs"
	"github.com/higress-group/issue-spec/internal/commentrunner/state"
	"github.com/higress-group/issue-spec/internal/commentrunner/writeback"
)

func TestRunnerLoggerKeepsConcurrentJobCorrelationIsolated(t *testing.T) {
	root := t.TempDir()
	logger, err := newRunnerLogger(commentrunner.Config{
		Hostname: "github.example.test", Repositories: []string{"owner/a", "owner/b"}, RunnerIdentity: "runner",
		StatePath: filepath.Join(root, "state.json"), LogDir: filepath.Join(root, "logs"),
		LogMaxSizeMB: 1, LogMaxFiles: 2, LogRetentionDays: 30, LogRawCaptureKB: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.close() })

	jobs := []state.Job{
		{ID: "job-a", Repo: "owner/a", PublicSessionID: "session-a", TriggerCommentID: 101, StatusCommentID: 201,
			Workspace: state.WorkspaceMetadata{ID: "workspace-a"}, AcpxRecordID: "record-a",
			DispatchIntent: state.DispatchIntent{TurnCorrelationToken: "turn-a"},
			Acpx:           state.AcpxMetadata{LastTurnID: "turn-a"}},
		{ID: "job-b", Repo: "owner/b", PublicSessionID: "session-b", TriggerCommentID: 102, StatusCommentID: 202,
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
	// Follow the concurrent writes with the opposite sequential order to prove
	// that one repository's scope is never retained for the next job.
	for index := len(jobs) - 1; index >= 0; index-- {
		job := jobs[index]
		if err := logger.logJobEvent(job, diagnostics.Correlation{DeliveryID: "delivery-" + job.ID},
			diagnostics.LevelInfo, "job_completed", "runner job completed", nil, "", ""); err != nil {
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
				event.Correlation.DeliveryID != "delivery-"+job.ID || event.RunnerScope.Repo != job.Repo {
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
		if count != 2 {
			t.Fatalf("job %s event count=%d want=2", job.ID, count)
		}
		sessionEvents := readDiagnosticEvents(t, filepath.Join(root, "logs", "sessions", job.PublicSessionID,
			job.DispatchIntent.TurnCorrelationToken+".ndjson"))
		if len(sessionEvents) != 2 {
			t.Fatalf("job %s session scope=%+v", job.ID, sessionEvents)
		}
		for _, event := range sessionEvents {
			if event.RunnerScope.Repo != job.Repo {
				t.Fatalf("job %s session scope=%+v", job.ID, sessionEvents)
			}
		}
	}
	runnerEvents := readDiagnosticEvents(t, filepath.Join(root, "logs", "runner.ndjson"))
	jobScopes := map[string]string{}
	for _, event := range runnerEvents {
		if event.Correlation.JobID != "" {
			jobScopes[event.Correlation.JobID] = event.RunnerScope.Repo
		}
	}
	for _, job := range jobs {
		if jobScopes[job.ID] != job.Repo {
			t.Fatalf("job %s runner scope=%q want=%q", job.ID, jobScopes[job.ID], job.Repo)
		}
	}
}

func TestRunnerLoggerWebhookReconcileUsesAuthoritativeRepositoryScope(t *testing.T) {
	root := t.TempDir()
	logger, err := newRunnerLogger(commentrunner.Config{
		Hostname: "github.example.test", Repositories: []string{"owner/a", "owner/b"}, RunnerIdentity: "runner",
		StatePath: filepath.Join(root, "state.json"), LogDir: filepath.Join(root, "logs"),
		LogMaxSizeMB: 1, LogMaxFiles: 2, LogRetentionDays: 30, LogRawCaptureKB: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.close() })
	if err := logger.logStartup("poll"); err != nil {
		t.Fatal(err)
	}
	if err := logger.ObserveWebhookReconcile(webhook.ReconcileResult{Claimed: true, DeliveryID: "delivery-b",
		EventID: "event-b", Repository: "owner/b", TriggerCommentID: 42, PublicSessionID: "session-b",
		Outcome: state.DeliveryOutcomeJob, JobID: "job-webhook-b", Completed: true}); err != nil {
		t.Fatal(err)
	}
	events := readDiagnosticEvents(t, filepath.Join(root, "logs", "runner.ndjson"))
	for _, event := range events {
		switch event.Event {
		case "startup":
			if event.RunnerScope.Repo != "" {
				t.Fatalf("process scope pinned to repository: %+v", event.RunnerScope)
			}
		case "webhook_reconciled", "job_queued":
			if event.RunnerScope.Repo != "owner/b" {
				t.Fatalf("webhook event %s scope=%+v", event.Event, event.RunnerScope)
			}
		}
	}
	jobEvents := readDiagnosticEvents(t, filepath.Join(root, "logs", "jobs", "job-webhook-b.ndjson"))
	if len(jobEvents) != 1 || jobEvents[0].RunnerScope.Repo != "owner/b" {
		t.Fatalf("webhook job scope=%+v", jobEvents)
	}
}

func TestRunnerLoggerPollAndRestartUseAuthoritativeRepositoryScope(t *testing.T) {
	root := t.TempDir()
	logger, err := newRunnerLogger(commentrunner.Config{
		Hostname: "github.example.test", Repositories: []string{"owner/a", "owner/b"}, RunnerIdentity: "runner",
		StatePath: filepath.Join(root, "state.json"), LogDir: filepath.Join(root, "logs"),
		LogMaxSizeMB: 1, LogMaxFiles: 2, LogRetentionDays: 30, LogRawCaptureKB: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = logger.close() })
	candidates := []intake.JobCandidate{
		{JobID: "job-poll-a", Repo: "owner/a", PublicSessionID: "session-a", Created: true},
		{JobID: "job-poll-b", Repo: "owner/b", PublicSessionID: "session-b", Created: true},
	}
	if err := logger.logIntake(&intake.Result{OK: true, Jobs: candidates}); err != nil {
		t.Fatal(err)
	}
	if err := logger.logReconcile(&crjobs.ReconcileResult{Jobs: []crjobs.ReconcileJob{
		{JobID: "job-poll-a", Repo: "owner/a", PublicSessionID: "session-a", Status: state.StatusQueued},
		{JobID: "job-poll-b", Repo: "owner/b", PublicSessionID: "session-b", Status: state.StatusQueued},
	}}); err != nil {
		t.Fatal(err)
	}

	wantScopes := map[string]string{"job-poll-a": "owner/a", "job-poll-b": "owner/b"}
	for jobID, wantRepo := range wantScopes {
		events := readDiagnosticEvents(t, filepath.Join(root, "logs", "jobs", jobID+".ndjson"))
		if len(events) != 1 || events[0].RunnerScope.Repo != wantRepo {
			t.Fatalf("poll job %s scope=%+v", jobID, events)
		}
	}
	for _, event := range readDiagnosticEvents(t, filepath.Join(root, "logs", "runner.ndjson")) {
		if event.Correlation.JobID == "" {
			if event.RunnerScope.Repo != "" {
				t.Fatalf("process event %s pinned to repository: %+v", event.Event, event.RunnerScope)
			}
			continue
		}
		wantRepo, ok := wantScopes[event.Correlation.JobID]
		if ok && event.RunnerScope.Repo != wantRepo {
			t.Fatalf("event %s job %s scope=%q want=%q", event.Event, event.Correlation.JobID,
				event.RunnerScope.Repo, wantRepo)
		}
	}
}

func readDiagnosticEvents(t *testing.T, path string) []diagnostics.Event {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var events []diagnostics.Event
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var event diagnostics.Event
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			t.Fatal(err)
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return events
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
