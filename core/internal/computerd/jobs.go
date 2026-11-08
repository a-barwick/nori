package computerd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	jobLimit        = 60 * time.Minute
	jobsPerTask     = 4
	maxJobLog       = 16 << 20
	jobTailBytes    = 4 << 10
	jobsDirectory   = ".nori-jobs"
	jobStopDeadline = 10 * time.Second
)

// A job belongs to its task, so a correction or question leaves it running. Only
// job_stop, the task's terminal cleanup, its deadline or the computer's own stop
// end it. A job the guest can no longer account for is lost, never finished.
type jobRecord struct {
	ID         string     `json:"id"`
	TaskID     string     `json:"task_id"`
	State      string     `json:"state"`
	ExitCode   *int       `json:"exit_code,omitempty"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
	Group      string     `json:"group,omitempty"`
}

type activeJob struct {
	mu        sync.Mutex
	record    jobRecord
	stopping  bool
	cancel    context.CancelFunc
	done      chan struct{}
	logFile   *os.File
	written   int64
	truncated bool
}

// Write keeps draining past the cap so a chatty command can't block on its pipe.
func (j *activeJob) Write(p []byte) (int, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	data := p
	if remaining := maxJobLog - j.written; int64(len(data)) > remaining {
		data = data[:max(remaining, 0)]
		j.truncated = true
	}
	if len(data) > 0 {
		n, err := j.logFile.Write(data)
		j.written += int64(n)
		if err != nil {
			j.truncated = true
		}
	}
	return len(p), nil
}

func (s *server) jobRoot() (*os.Root, error) {
	root, err := os.OpenRoot(s.tasks)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err = root.Mkdir(jobsDirectory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	return root.OpenRoot(jobsDirectory)
}

func (s *server) writeJob(record jobRecord) error {
	root, err := s.jobRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := root.OpenFile(record.ID+".json.next", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	return root.Rename(record.ID+".json.next", record.ID+".json")
}

func (s *server) readJob(id string) (jobRecord, error) {
	s.jobsMu.Lock()
	record, unsaved := s.unsavedJobs[id]
	if unsaved {
		// A failed final write must never turn a completed job back into running.
		// A later read also gets its final state onto disk once storage recovers.
		// Serialize retries: writeJob uses one temporary file per job.
		if err := s.writeJob(record); err == nil {
			delete(s.unsavedJobs, id)
		}
		s.jobsMu.Unlock()
		return record, nil
	}
	s.jobsMu.Unlock()
	root, err := s.jobRoot()
	if err != nil {
		return jobRecord{}, err
	}
	defer root.Close()
	data, err := root.ReadFile(id + ".json")
	if err != nil {
		return jobRecord{}, err
	}
	return record, json.Unmarshal(data, &record)
}

func (s *server) finishJob(record jobRecord) error {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	err := s.writeJob(record)
	if err != nil {
		if s.unsavedJobs == nil {
			s.unsavedJobs = make(map[string]jobRecord)
		}
		s.unsavedJobs[record.ID] = record
	} else {
		delete(s.unsavedJobs, record.ID)
	}
	return err
}

// Return readable records alongside scan failures so known jobs can still drain.
// Callers must retain the error when acknowledging complete task cleanup.
func (s *server) jobRecords() ([]jobRecord, error) {
	root, err := s.jobRoot()
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil, err
	}
	var records []jobRecord
	var failures []error
	for _, name := range names {
		if filepath.Ext(name) != ".json" {
			continue
		}
		record, err := s.readJob(strings.TrimSuffix(name, ".json"))
		if err != nil {
			failures = append(failures, fmt.Errorf("job record %s: %w", name, err))
			continue
		}
		records = append(records, record)
	}
	return records, errors.Join(failures...)
}

// recoverJobs drains abandoned process trees before serving requests. Failed cleanup remains recorded.
func (s *server) recoverJobs() {
	records, err := s.jobRecords()
	if err != nil {
		log.Printf("job recovery scan incomplete: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), jobStopDeadline)
	defer cancel()
	for _, record := range records {
		if err := s.settleJob(ctx, record); err != nil {
			log.Printf("job %s cleanup remains unconfirmed: %v", record.ID, err)
		}
	}
}

// A failed stop retains its process-group identity. Neither a second stop nor a guest restart may forget it.
func (s *server) settleJob(ctx context.Context, record jobRecord) error {
	if record.State != "running" && record.State != "stop_unconfirmed" {
		return nil
	}
	if record.Group == "" {
		return errors.New("job process group is unknown")
	}
	if _, err := os.Stat(record.Group); !errors.Is(err, os.ErrNotExist) {
		if err != nil {
			return err
		}
		group := &operationGroup{path: record.Group}
		if err := group.stop(ctx); err != nil {
			return err
		}
		_ = os.Remove(record.Group)
	}
	now := time.Now().UTC()
	record.State, record.FinishedAt, record.Group = "lost", &now, ""
	return s.finishJob(record)
}

// The fence is saved before draining jobs and shares the reservation lock with startJob.
// It survives a guest restart and rejects late starts from every revision of this task.
func (s *server) taskJobsClosed(id string, close bool) (bool, error) {
	root, err := s.jobRoot()
	if err != nil {
		return false, err
	}
	defer root.Close()
	name := "task-" + id + ".stopped"
	if !close {
		_, err := root.Stat(name)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return err == nil, err
	}
	file, err := root.OpenFile(name, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return false, err
	}
	if err := errors.Join(file.Sync(), file.Close()); err != nil {
		return false, err
	}
	dir, err := root.Open(".")
	if err != nil {
		return false, err
	}
	defer dir.Close()
	return true, dir.Sync()
}

func (s *server) runningJobs() int {
	s.jobsMu.Lock()
	defer s.jobsMu.Unlock()
	return len(s.jobs)
}

func (s *server) startJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RunID   string `json:"run_id"`
		TaskID  string `json:"task_id"`
		JobID   string `json:"job_id"`
		Command string `json:"command"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !runPattern.MatchString(req.RunID) || !runPattern.MatchString(req.TaskID) || !runPattern.MatchString(req.JobID) || req.Command == "" {
		http.Error(w, "run_id, task_id, job_id and command required", 400)
		return
	}
	if s.wasCancelled(req.RunID) {
		http.Error(w, "run canceled", 409)
		return
	}
	if _, err := s.readJob(req.JobID); err == nil {
		http.Error(w, "job already exists", 409)
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		http.Error(w, "job state unavailable", 503)
		return
	}
	s.jobsMu.Lock()
	closed, err := s.taskJobsClosed(req.TaskID, false)
	if err != nil || closed || s.jobs[req.JobID] != nil {
		s.jobsMu.Unlock()
		if err != nil {
			http.Error(w, "job ownership unavailable", 503)
		} else {
			http.Error(w, "task closed or job already exists", 409)
		}
		return
	}
	running := 0
	for _, job := range s.jobs {
		if job.snapshot().TaskID == req.TaskID {
			running++
		}
	}
	if running >= jobsPerTask {
		s.jobsMu.Unlock()
		http.Error(w, "too many running jobs for this task", 429)
		return
	}
	// Reserve the slot so the idle stop counts this job from before it starts.
	job := &activeJob{record: jobRecord{ID: req.JobID, TaskID: req.TaskID, State: "running", StartedAt: time.Now().UTC()}, done: make(chan struct{})}
	s.jobs[req.JobID] = job
	s.jobsMu.Unlock()
	if err := s.launchJob(job, req.Command); err != nil {
		s.jobsMu.Lock()
		delete(s.jobs, req.JobID)
		s.jobsMu.Unlock()
		close(job.done)
		log.Printf("job %s did not start: %v", req.JobID, err)
		http.Error(w, "job could not be started", 503)
		return
	}
	respond(w, s.jobStatusOf(job.snapshot(), job))
}

func (s *server) launchJob(job *activeJob, command string) error {
	dir, closeDir, err := s.openTaskDir(job.record.TaskID)
	if err != nil {
		return err
	}
	defer closeDir()
	root, err := s.jobRoot()
	if err != nil {
		return err
	}
	defer root.Close()
	job.logFile, err = root.OpenFile(job.record.ID+".log", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	group, err := newOperationGroup()
	if err != nil {
		_ = job.logFile.Close()
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), jobLimit)
	cmd := exec.CommandContext(ctx, "/bin/bash", "-lc", command)
	cmd.Dir = "/proc/self/fd/" + strconv.Itoa(int(dir.Fd()))
	cmd.SysProcAttr = group.attributes()
	cmd.Cancel = group.kill
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdout = job
	cmd.Stderr = job
	job.mu.Lock()
	job.cancel = cancel
	job.record.Group = group.path
	record := job.record
	if job.stopping {
		cancel()
	}
	job.mu.Unlock()
	// Record before starting: a guest that dies here leaves a lost job, not a
	// process nobody knows about.
	if err = s.writeJob(record); err != nil {
		cancel()
		group.close()
		_ = job.logFile.Close()
		return err
	}
	if err = cmd.Start(); err != nil {
		cancel()
		group.close()
		_ = job.logFile.Close()
		now := time.Now().UTC()
		job.mu.Lock()
		job.record.State, job.record.FinishedAt, job.record.Group = "failed_to_start", &now, ""
		record = job.record
		job.mu.Unlock()
		return errors.Join(err, s.finishJob(record))
	}
	go s.superviseJob(job, cmd, group, ctx)
	return nil
}

func (s *server) superviseJob(job *activeJob, cmd *exec.Cmd, group *operationGroup, ctx context.Context) {
	waitErr := cmd.Wait()
	cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
	stopErr := group.stop(cleanup)
	stop()
	group.close()
	job.cancel()
	_ = job.logFile.Close()
	code := 0
	if waitErr != nil {
		code = 1
		var exit *exec.ExitError
		if errors.As(waitErr, &exit) {
			code = exit.ExitCode()
		}
	}
	job.mu.Lock()
	now := time.Now().UTC()
	job.record.FinishedAt = &now
	switch {
	case stopErr != nil:
		job.record.State = "stop_unconfirmed"
	case job.stopping:
		job.record.State = "stopped"
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		job.record.State = "timed_out"
	default:
		job.record.State = "finished"
		job.record.ExitCode = &code
	}
	if stopErr == nil {
		job.record.Group = ""
	}
	record := job.record
	job.mu.Unlock()
	if err := s.finishJob(record); err != nil {
		log.Printf("job %s final state could not be saved: %v", record.ID, err)
	}
	s.jobsMu.Lock()
	delete(s.jobs, record.ID)
	s.jobsMu.Unlock()
	close(job.done)
}

func (j *activeJob) snapshot() jobRecord {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.record
}

func (s *server) jobStatusOf(record jobRecord, live *activeJob) map[string]any {
	end := time.Now().UTC()
	if record.FinishedAt != nil {
		end = *record.FinishedAt
	}
	result := map[string]any{"job_id": record.ID, "task_id": record.TaskID, "lifetime": "task", "state": record.State, "started_at": record.StartedAt, "elapsed_seconds": int(end.Sub(record.StartedAt).Seconds())}
	s.jobsMu.Lock()
	_, unsaved := s.unsavedJobs[record.ID]
	s.jobsMu.Unlock()
	if unsaved {
		result["state_persisted"] = false
	}
	if record.ExitCode != nil {
		result["exit_code"] = *record.ExitCode
	}
	if record.FinishedAt != nil {
		result["finished_at"] = *record.FinishedAt
	}
	root, err := s.jobRoot()
	if err != nil {
		return result
	}
	defer root.Close()
	file, err := root.Open(record.ID + ".log")
	if err != nil {
		return result
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return result
	}
	result["output_bytes"] = info.Size()
	if live != nil {
		live.mu.Lock()
		if live.truncated {
			result["output_truncated"] = true
		}
		live.mu.Unlock()
	} else if info.Size() >= maxJobLog {
		result["output_truncated"] = true
	}
	start := max(info.Size()-jobTailBytes, 0)
	if _, err = file.Seek(start, io.SeekStart); err == nil {
		if tail, e := io.ReadAll(io.LimitReader(file, jobTailBytes)); e == nil {
			result["output_tail"] = strings.ToValidUTF8(string(tail), "")
		}
	}
	return result
}

func (s *server) findJob(w http.ResponseWriter, jobID, taskID string) (jobRecord, *activeJob, bool) {
	if !runPattern.MatchString(jobID) || !runPattern.MatchString(taskID) {
		http.Error(w, "invalid job_id or task_id", 400)
		return jobRecord{}, nil, false
	}
	s.jobsMu.Lock()
	live := s.jobs[jobID]
	s.jobsMu.Unlock()
	if live != nil {
		record := live.snapshot()
		if record.TaskID != taskID {
			http.Error(w, "unknown job", 404)
			return jobRecord{}, nil, false
		}
		return record, live, true
	}
	record, err := s.readJob(jobID)
	if errors.Is(err, os.ErrNotExist) || (err == nil && record.TaskID != taskID) {
		http.Error(w, "unknown job", 404)
		return jobRecord{}, nil, false
	}
	if err != nil {
		http.Error(w, "job state unavailable", 503)
		return jobRecord{}, nil, false
	}
	return record, nil, true
}

func (s *server) jobStatus(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"task_id"`
		JobID  string `json:"job_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	if record, live, ok := s.findJob(w, req.JobID, req.TaskID); ok {
		respond(w, s.jobStatusOf(record, live))
	}
}

func (j *activeJob) stop(ctx context.Context) error {
	j.mu.Lock()
	j.stopping = true
	cancel := j.cancel
	j.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	select {
	case <-j.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *server) jobStop(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"task_id"`
		JobID  string `json:"job_id"`
	}
	if !decode(w, r, &req) {
		return
	}
	record, live, ok := s.findJob(w, req.JobID, req.TaskID)
	if !ok {
		return
	}
	if live != nil {
		ctx, cancel := context.WithTimeout(r.Context(), jobStopDeadline)
		defer cancel()
		if err := live.stop(ctx); err != nil {
			http.Error(w, "job stop could not be confirmed", 502)
			return
		}
		record = live.snapshot()
	}
	if record.State == "stop_unconfirmed" {
		ctx, cancel := context.WithTimeout(r.Context(), jobStopDeadline)
		defer cancel()
		if err := s.settleJob(ctx, record); err != nil {
			http.Error(w, "job stop could not be confirmed", 502)
			return
		}
		var err error
		record, err = s.readJob(req.JobID)
		if err != nil {
			http.Error(w, "job state unavailable", 503)
			return
		}
	}
	respond(w, s.jobStatusOf(record, nil))
}

// stopTaskJobs is the task's terminal cleanup. It repeats safely and acknowledges
// only after every owned process tree has exited.
func (s *server) stopTaskJobs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		TaskID string `json:"task_id"`
		Close  bool   `json:"close"`
	}
	if !decode(w, r, &req) {
		return
	}
	if !runPattern.MatchString(req.TaskID) {
		http.Error(w, "invalid task_id", 400)
		return
	}
	s.jobsMu.Lock()
	if req.Close {
		if _, err := s.taskJobsClosed(req.TaskID, true); err != nil {
			s.jobsMu.Unlock()
			http.Error(w, "job ownership could not be closed", 503)
			return
		}
	}
	owned := []*activeJob{}
	for _, job := range s.jobs {
		if job.record.TaskID == req.TaskID {
			owned = append(owned, job)
		}
	}
	s.jobsMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), jobStopDeadline)
	defer cancel()
	var failures []error
	for _, job := range owned {
		if err := job.stop(ctx); err != nil {
			failures = append(failures, err)
		}
	}
	if len(failures) == 0 {
		records, err := s.jobRecords()
		failures = append(failures, err)
		for _, record := range records {
			if record.TaskID == req.TaskID {
				failures = append(failures, s.settleJob(ctx, record))
			}
		}
	}
	if errors.Join(failures...) != nil {
		http.Error(w, "job stop could not be confirmed", 502)
		return
	}
	respond(w, map[string]bool{"stopped": true, "closed": req.Close})
}
