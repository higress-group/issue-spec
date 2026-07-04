# Runner Diagnostics Troubleshooting

## Overview

This skill provides guidance for troubleshooting issues with the issue-spec comment runner using persistent diagnostic logs.

## Log Locations

Runner logs are stored in the scoped runner directory:

```
~/.issue-spec/runners/<host>/<owner>/<repo>/<runner>/
├── state.json              # Runner state
└── logs/                   # Diagnostic logs
    ├── runner.ndjson       # Runner lifecycle events
    ├── errors.ndjson       # Error/warning events
    ├── index.ndjson        # Identifier-to-file mapping
    ├── jobs/               # Job-specific logs
    │   ├── <job-id>.ndjson
    │   ├── <job-id>-acpx-stdout.log
    │   └── <job-id>-acpx-stderr.log
    └── sessions/          # Session-specific logs
        └── <public-session-id>/
            └── <turn-correlation-id>.ndjson
```

## Finding Logs from GitHub Status Comments

When investigating a failed runner job from a GitHub status comment:

1. **Extract identifiers from the status comment**:
   - Public session ID
   - Job ID (if shown)
   - Trigger comment URL
   - Status comment URL

2. **Locate the runner state**:
   ```bash
   # Find the runner state directory
   cat ~/.issue-spec/runners/*/higress/issue-spec/<runner>/state.json | jq .
   ```

3. **Use the index to find log files**:
   ```bash
   # Search index by job ID
   grep '"id":"<job-id>"' logs/index.ndjson

   # Search index by public session ID
   grep '"id":"<public-session-id>"' logs/index.ndjson
   ```

4. **Read the job log**:
   ```bash
   cat logs/jobs/<job-id>.ndjson | jq .
   ```

5. **Check ACPX output**:
   ```bash
   cat logs/jobs/<job-id>-acpx-stdout.log
   cat logs/jobs/<job-id>-acpx-stderr.log
   ```

## Investigating Failures

### Step 1: Check Runner Log

Look for errors in the runner log:

```bash
# Find error events
grep '"level":"error"' logs/runner.ndjson | jq .

# Check for a specific cycle
grep '"cycle_id":"<cycle-id>"' logs/runner.ndjson | jq .
```

### Step 2: Check Error Log

The errors.ndjson file contains all warnings and errors:

```bash
cat logs/errors.ndjson | jq .
```

### Step 3: Check Job Log

Job logs contain detailed execution events:

```bash
# Find all events for a job
cat logs/jobs/<job-id>.ndjson | jq .

# Find state transitions
grep '"event":"state"' logs/jobs/<job-id>.ndjson | jq .

# Find errors
grep '"level":"error"' logs/jobs/<job-id>.ndjson | jq .
```

### Step 4: Check ACPX Output

ACPX stdout and stderr capture shows coordinator activity:

```bash
# Check for errors
cat logs/jobs/<job-id>-acpx-stderr.log

# Check last output
tail -100 logs/jobs/<job-id>-acpx-stdout.log
```

## Correlation Chain

Logs are linked through correlation IDs:

```
GitHub Status Comment
  → public_session_id
  → job_id
  → logs/jobs/<job-id>.ndjson
  → logs/sessions/<public-session-id>/<turn-correlation-id>.ndjson
  → logs/jobs/<job-id>-acpx-stdout.log
  → logs/jobs/<job-id>-acpx-stderr.log
```

All events share correlation fields:
- `cycle_id`: Links runner poll cycle events
- `job_id`: Links job-specific events
- `public_session_id`: Links session events
- `trigger_comment_id`: Links to the triggering GitHub comment
- `acpx_record_id`: Links to ACPX records

## Common Failure Patterns

### Preflight Failures

Symptom: Runner exits during preflight check.

Investigation:
```bash
# Check preflight events in runner log
grep '"component":"preflight"' logs/runner.ndjson | jq .
```

Common causes:
- GitHub authentication expired
- Repository access denied
- Sandbox prerequisites not met
- ACPX not installed

### Intake Failures

Symptom: Commands not being detected.

Investigation:
```bash
# Check intake events
grep '"component":"intake"' logs/runner.ndjson | jq .
```

Common causes:
- Notification polling issues
- GitHub API rate limiting
- Repository comment access issues

### Dispatch Failures

Symptom: Jobs not starting or failing during dispatch.

Investigation:
```bash
# Check dispatch events
grep '"component":"dispatch"' logs/runner.ndjson | jq .

# Check job state transitions
grep '"event":"state"' logs/jobs/<job-id>.ndjson | jq .
```

Common causes:
- Workspace creation failures
- Sandbox setup issues
- ACPX execution failures

### ACPX Failures

Symptom: Coordinator crashes or hangs.

Investigation:
```bash
# Check ACPX stderr
cat logs/jobs/<job-id>-acpx-stderr.log

# Look for ACPX events in job log
grep '"component":"acpx"' logs/jobs/<job-id>.ndjson | jq .
```

Common causes:
- Agent quota exceeded
- Model access issues
- Network failures
- Sandbox isolation violations

## Safety Rules

### Redacted Content

All logs are redacted for security. When working with logs:

1. **Never reconstruct redacted values**
   - Redacted values are replaced with markers like `[REDACTED:token]`
   - Original values are not recoverable from logs

2. **Don't paste sensitive diagnostics to GitHub**
   - Log files contain redacted but potentially sensitive information
   - Summarize findings without copying log content
   - Use diagnostic IDs instead of file paths

3. **Handle truncated files carefully**
   - Truncation markers indicate incomplete output
   - `[TRUNCATED: original size exceeded capture limit]`
   - Full output was not captured

## Summarizing Failures

When reporting failures:

1. Start with public identifiers:
   - Public session ID
   - Job ID
   - Status comment URL

2. Describe the failure:
   - When it occurred
   - What was being attempted
   - Error messages (without secrets)

3. Provide correlation:
   - Cycle ID from runner log
   - Relevant event timestamps
   - Component that failed

4. Include safe excerpts:
   - Error event details (without secrets)
   - State transitions
   - Truncated file indicators

5. Exclude from reports:
   - Absolute file paths
   - Redacted values (original or reconstructed)
   - Full log file contents
   - Auth material traces

## Example Troubleshooting Workflow

```bash
# 1. Find failed job from status comment
JOB_ID="<job-id>"
SESSION_ID="<public-session-id>"

# 2. Locate runner logs
cd ~/.issue-spec/runners/github.com/higress/issue-spec/<runner>/logs

# 3. Check job state
grep '"event":"state"' jobs/${JOB_ID}.ndjson | jq -r '.message'

# 4. Find errors in job log
grep '"level":"error"' jobs/${JOB_ID}.ndjson | jq .

# 5. Check ACPX output
tail -100 jobs/${JOB_ID}-acpx-stderr.log

# 6. Find related cycle in runner log
CYCLE_ID=$(grep "job_id.*${JOB_ID}" jobs/${JOB_ID}.ndjson | jq -r '.correlation.cycle_id' | head -1)
grep "cycle_id.*${CYCLE_ID}" runner.ndjson | jq .

# 7. Summarize findings
# - What failed
# - When it failed
# - Error messages
# - Related log entries
```

## Quick Reference

| Task | Command |
|------|---------|
| Find job log | `cat logs/jobs/<job-id>.ndjson \| jq .` |
| Find errors | `grep '"level":"error"' logs/errors.ndjson \| jq .` |
| Search index | `grep '<id>' logs/index.ndjson \| jq .` |
| Check ACPX | `cat logs/jobs/<job-id>-acpx-stderr.log` |
| Trace cycle | `grep '<cycle-id>' logs/runner.ndjson \| jq .` |
| Count errors | `grep '"level":"error"' logs/errors.ndjson \| wc -l` |
| Find latest job | `ls -t logs/jobs/*.ndjson \| head -1` |
| Check truncation | `grep TRUNCATED logs/jobs/*.log` |
