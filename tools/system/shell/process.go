package shell

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/support/mathutil"
	"github.com/invokr-agent/invokr/support/strutil"
	"github.com/invokr-agent/invokr/tools/core"

	"github.com/google/uuid"
)

const (
	processToolName      = "process"
	processRetention     = 24 * time.Hour
	maxRetainedProcesses = 100
)

type processArgs struct {
	Action     string `json:"action" enum:"list,poll,wait,log,kill"`
	ProcessID  string `json:"process_id,omitempty"`
	TimeoutMS  int    `json:"timeout_ms,omitempty"`
	MaxBytes   int    `json:"max_bytes,omitempty"`
	StartBytes int64  `json:"start_bytes,omitempty"`
}

type processRecord struct {
	Mode       approval.Mode `json:"mode,omitempty"`
	release    func()
	ID         string    `json:"id"`
	Command    string    `json:"command"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"-"`
	Done       bool      `json:"done"`
	ExitCode   int       `json:"exit_code"`
	LogPath    string    `json:"log_path"`
	SessionID  string    `json:"-"`
	cmd        *exec.Cmd
	cancel     context.CancelFunc
	done       chan struct{}
}

// processResult carries snapshots of the records themselves: processRecord's
// json tags are exactly the model-facing view (the bookkeeping fields are
// tagged "-"), and a snapshot is taken under the store lock.
type processResult struct {
	Processes []processRecord `json:"processes,omitempty"`
	Process   *processRecord  `json:"process,omitempty"`
	Log       string          `json:"log,omitempty"`
}

type processStore struct {
	mu      sync.Mutex
	records map[string]*processRecord
	now     func() time.Time
}

var processes = &processStore{records: map[string]*processRecord{}, now: func() time.Time { return time.Now().UTC() }}

func RegisterProcessTool(registrar core.NativeRegistrar, dispatcher events.Dispatcher) {
	core.Register(registrar, core.Tool[processArgs]{
		Spec: core.ToolSpec{
			Name:        processToolName,
			Description: "Inspect or control background shell processes started with shell(background=true).",
			Example:     `{"name":"process","arguments":{"action":"poll","process_id":"..."}}`,
			Parameters:  core.SchemaFor[processArgs](),
			Metadata:    core.NativeMetadata("system", []string{string(shellTierReadonly), string(shellTierWrite)}, constants.ApprovalPolicyAsk, core.ToolCapabilities{ReadsWorkspace: true, WritesWorkspace: true, ExecutesCode: true}),
		},
		Prepare: prepareProcessArgs,
		Approve: func(ctx context.Context, args processArgs) core.ApprovalDecision {
			return core.Automatic(approval.Request{Tool: processToolName, Subject: args.Action + " " + args.ProcessID, ReadOnly: args.Action != "kill", Writes: args.Action == "kill", Confined: processes.ownedBy(args.ProcessID, core.SessionIDFromContext(ctx))})
		},
		Execute: func(ctx context.Context, args processArgs) (string, error) {
			return runProcessAction(ctx, dispatcher, args)
		},
	})
}

func startBackgroundShellCommand(dispatcher events.Dispatcher, ctx context.Context, plan preparedShellCommand) (string, error) {
	sessionID := core.SessionIDFromContext(ctx)
	logDir := processLogDir(sessionID)
	if err := os.MkdirAll(logDir, 0700); err != nil {
		return "", fmt.Errorf("create process log dir: %w", err)
	}
	pruneProcessLogs(logDir, time.Now().UTC())
	id := uuid.NewString()
	logPath := filepath.Join(logDir, id+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return "", fmt.Errorf("create process log: %w", err)
	}
	spec, err := processSpec(plan)
	if err != nil {
		_ = logFile.Close()
		return "", err
	}
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	cmd := exec.CommandContext(runCtx, spec.Name, spec.Args...)
	var cleanup func() error
	if spec.Build != nil {
		if cmd, cleanup, err = spec.Build(runCtx); err != nil {
			cancel()
			_ = logFile.Close()
			return "", err
		}
	}
	cmd.Dir = plan.cwd
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = spec.Env
	core.ConfigureProcessGroup(cmd)
	record := &processRecord{ID: id, Command: plan.line, StartedAt: time.Now().UTC(), LogPath: logPath, SessionID: sessionID, cmd: cmd, cancel: cancel, done: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		cancel()
		if cleanup != nil {
			_ = cleanup()
		}
		_ = logFile.Close()
		return "", fmt.Errorf("start background command: %w", err)
	}
	record.Mode, record.release = approval.ModeFromContext(ctx), approval.Retain(ctx)
	processes.add(record)
	go waitBackgroundProcess(dispatcher, runCtx, record, logFile, plan, cleanup)
	payload, err := core.MarshalResult("background", BackgroundResult{ProcessID: id, LogPath: logPath})
	if err != nil {
		return "", err
	}
	dispatchShellAudit(dispatcher, ctx, plan, "started", nil)
	return payload, nil
}

func waitBackgroundProcess(dispatcher events.Dispatcher, ctx context.Context, record *processRecord, logFile *os.File, plan preparedShellCommand, cleanup func() error) {
	defer record.release()
	err := record.cmd.Wait()
	_ = logFile.Close()
	if cleanup != nil {
		_ = cleanup()
	}
	exitCode := 0
	if err != nil {
		exitCode = -1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	processes.finish(record.ID, exitCode)
	dispatchShellAudit(dispatcher, ctx, plan, core.Outcome(err), err)
}

func prepareProcessArgs(_ context.Context, args processArgs) (processArgs, error) {
	args.Action = strings.TrimSpace(args.Action)
	switch args.Action {
	case "list":
		return args, nil
	case "poll", "wait", "log", "kill":
		if strings.TrimSpace(args.ProcessID) == "" {
			return args, fmt.Errorf("process_id is required for %s", args.Action)
		}
		return args, nil
	default:
		return args, fmt.Errorf("unsupported process action %q", args.Action)
	}
}

func runProcessAction(ctx context.Context, dispatcher events.Dispatcher, args processArgs) (string, error) {
	sessionID := core.SessionIDFromContext(ctx)
	if args.Action != "list" && !processes.ownedBy(args.ProcessID, sessionID) {
		return "", fmt.Errorf("process not found in this session")
	}
	switch args.Action {
	case "list":
		return encodeProcessResult(processResult{Processes: processes.list(sessionID)})
	case "poll":
		return encodeProcessResult(processResult{Process: processes.view(args.ProcessID)})
	case "wait":
		return waitProcess(ctx, args)
	case "log":
		return readProcessLog(args)
	case "kill":
		processes.kill(args.ProcessID)
		dispatchProcessAudit(dispatcher, ctx, args.ProcessID, "killed", nil)
		return encodeProcessResult(processResult{Process: processes.view(args.ProcessID)})
	default:
		return "", fmt.Errorf("unsupported process action %q", args.Action)
	}
}

// processWaitTimeout clamps the model-supplied wait timeout so a single
// `wait` call cannot block the turn indefinitely.
func processWaitTimeout(timeoutMS int) time.Duration {
	return time.Duration(mathutil.ClampInt(timeoutMS, int(time.Second/time.Millisecond), constants.ShellMaxTimeoutMS)) * time.Millisecond
}

func waitProcess(ctx context.Context, args processArgs) (string, error) {
	timeout := processWaitTimeout(args.TimeoutMS)
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	record := processes.get(args.ProcessID)
	if record == nil {
		return encodeProcessResult(processResult{})
	}
	if processes.isDone(args.ProcessID) {
		return encodeProcessResult(processResult{Process: processes.view(args.ProcessID)})
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-deadline.C:
	case <-record.done:
	}
	return encodeProcessResult(processResult{Process: processes.view(args.ProcessID)})
}

func readProcessLog(args processArgs) (string, error) {
	view := processes.view(args.ProcessID)
	if view == nil {
		return "", fmt.Errorf("unknown process_id %q", args.ProcessID)
	}
	f, err := os.Open(view.LogPath)
	if err != nil {
		return "", fmt.Errorf("read process log: %w", err)
	}
	defer func() { _ = f.Close() }()
	start := max(args.StartBytes, 0)
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", fmt.Errorf("seek process log: %w", err)
	}
	maxBytes := mathutil.ClampInt(args.MaxBytes, constants.ShellDefaultMaxOutputBytes, constants.ShellMaxOutputBytes)
	data, err := io.ReadAll(io.LimitReader(f, int64(maxBytes)))
	if err != nil {
		return "", fmt.Errorf("read process log: %w", err)
	}
	return encodeProcessResult(processResult{Process: view, Log: string(data)})
}

func encodeProcessResult(result processResult) (string, error) {
	return core.MarshalResult("process", result)
}

func processLogDir(sessionID string) string {
	if strings.TrimSpace(sessionID) == "" {
		sessionID = "unscoped"
	} else {
		sessionID = strutil.HashHex(sessionID)[:12]
	}
	return filepath.Join(config.InvokrHomeDir(), "process", sessionID)
}

func pruneProcessLogs(dir string, now time.Time) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type retainedLog struct {
		path    string
		modTime time.Time
	}
	logs := make([]retainedLog, 0, len(entries))
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() || filepath.Ext(entry.Name()) != ".log" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if now.Sub(info.ModTime()) > processRetention {
			_ = os.Remove(path)
			continue
		}
		logs = append(logs, retainedLog{path: path, modTime: info.ModTime()})
	}
	slices.SortFunc(logs, func(a, b retainedLog) int { return b.modTime.Compare(a.modTime) })
	for _, log := range logs[min(len(logs), maxRetainedProcesses-1):] {
		_ = os.Remove(log.path)
	}
}

// swept takes the store lock, retires the records the retention policy drops,
// and returns the unlock to defer: every reader below sees a pruned store.
func (s *processStore) swept() func() {
	s.mu.Lock()
	s.pruneLocked()
	return s.mu.Unlock
}

func (s *processStore) add(record *processRecord) {
	defer s.swept()()
	s.records[record.ID] = record
}

func (s *processStore) finish(id string, exitCode int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if record := s.records[id]; record != nil {
		wasDone := record.Done
		record.Done = true
		record.ExitCode = exitCode
		record.FinishedAt = s.currentTime()
		if !wasDone {
			close(record.done)
		}
	}
	s.pruneLocked()
}

func (s *processStore) get(id string) *processRecord {
	defer s.swept()()
	return s.records[id]
}

func (s *processStore) isDone(id string) bool {
	defer s.swept()()
	record := s.records[id]
	return record == nil || record.Done
}

func (s *processStore) list(sessionID string) []processRecord {
	defer s.swept()()
	out := make([]processRecord, 0, len(s.records))
	for _, record := range s.records {
		if record.SessionID != sessionID {
			continue
		}
		out = append(out, *record)
	}
	return out
}

func (s *processStore) ownedBy(id, sessionID string) bool {
	defer s.swept()()
	record := s.records[id]
	return record != nil && record.SessionID == sessionID
}

func (s *processStore) view(id string) *processRecord {
	defer s.swept()()
	record := s.records[id]
	if record == nil {
		return nil
	}
	snapshot := *record
	return &snapshot
}

func (s *processStore) kill(id string) {
	s.mu.Lock()
	record := s.records[id]
	s.mu.Unlock()
	if record != nil && record.cancel != nil {
		record.cancel()
	}
}

func (s *processStore) currentTime() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now().UTC()
}

func (s *processStore) pruneLocked() {
	now := s.currentTime()
	var finished []*processRecord
	for id, record := range s.records {
		if !record.Done {
			continue
		}
		if !record.FinishedAt.IsZero() && now.Sub(record.FinishedAt) > processRetention {
			_ = os.Remove(record.LogPath)
			delete(s.records, id)
			continue
		}
		finished = append(finished, record)
	}
	if len(finished) <= maxRetainedProcesses {
		return
	}
	slices.SortFunc(finished, func(a, b *processRecord) int { return a.FinishedAt.Compare(b.FinishedAt) })
	for _, record := range finished[:len(finished)-maxRetainedProcesses] {
		_ = os.Remove(record.LogPath)
		delete(s.records, record.ID)
	}
}

func dispatchProcessAudit(dispatcher events.Dispatcher, ctx context.Context, id string, outcome string, err error) {
	core.AuditDetails(ctx, dispatcher, processToolName, outcome, map[string]any{"process_id": id}, err)
}
