package modules

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// StartServer is set by the workers package at init time to avoid an import
// cycle (workers imports modules, but the CLI in modules must start the
// server). workers is the --workers value (0 = keep the saved setting);
// checkTimes are the normalized --check-time values (nil = keep the saved
// setting).
var StartServer func(webListen string, webPort int, workers int, checkTimes []string) error

// controlClient is used for every request to the local control endpoints.
var controlClient = &http.Client{Timeout: 10 * time.Second}

func controlURL(port int, path string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/control%s", port, path)
}

// controlError extracts the error message of a control response body.
func controlError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &payload) == nil && payload.Error != "" {
		return fmt.Errorf("%s", payload.Error)
	}
	return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

// StopServer sends a graceful shutdown request to a running local server.
func StopServer(port int) error {
	resp, err := controlClient.Post(controlURL(port, "/shutdown"), "application/json", nil)
	if err != nil {
		return fmt.Errorf("failed to connect to server on port %d: %w", port, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		fmt.Println("Server shutdown initiated")
		return nil
	}
	return fmt.Errorf("shutdown request failed: %w", controlError(resp))
}

// ServerStatus is the payload of GET /control/status.
type ServerStatus struct {
	Status      string `json:"status"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	WebListen   string `json:"web_listen"`
	Uptime      string `json:"uptime"`
	Users       int    `json:"users"`
	Mailboxes   int    `json:"mailboxes"`
	ActiveJobs  int    `json:"active_jobs"`
	NextCheckAt string `json:"next_check_at"`
}

// GetServerStatus queries a running local server's status endpoint.
func GetServerStatus(port int) (*ServerStatus, error) {
	resp, err := controlClient.Get(controlURL(port, "/status"))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to server on port %d: %w", port, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, controlError(resp)
	}
	var st ServerStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, fmt.Errorf("failed to parse status: %w", err)
	}
	return &st, nil
}

// ShowServerStatus prints the status of a running local server.
func ShowServerStatus(port int) error {
	st, err := GetServerStatus(port)
	if err != nil {
		return err
	}
	next := st.NextCheckAt
	if next == "" {
		next = "none"
	} else if t, err := time.Parse(time.RFC3339, next); err == nil {
		next = t.Local().Format("2006-01-02 15:04")
	}
	fmt.Printf("%-12s %v\n", "status:", st.Status)
	fmt.Printf("%-12s %v\n", "name:", st.Name)
	fmt.Printf("%-12s %v\n", "version:", st.Version)
	fmt.Printf("%-12s %v\n", "web_listen:", st.WebListen)
	fmt.Printf("%-12s %v\n", "uptime:", st.Uptime)
	fmt.Printf("%-12s %v\n", "users:", st.Users)
	fmt.Printf("%-12s %v\n", "mailboxes:", st.Mailboxes)
	fmt.Printf("%-12s %v\n", "active_jobs:", st.ActiveJobs)
	fmt.Printf("%-12s %v\n", "next_check:", next)
	return nil
}

// IsServerRunning reports whether a MailCare server answers on the local
// control port.
func IsServerRunning(port int) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(controlURL(port, "/status"))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var st ServerStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return false
	}
	return st.Name == AppName
}

// JobRequest is the payload of POST /control/jobs (and POST /api/v1/jobs).
type JobRequest struct {
	Kind      string `json:"kind"`
	MailboxID int64  `json:"mailbox_id,omitempty"`
	Target    string `json:"target,omitempty"`
}

// JobInfo mirrors the JobDTO fields the CLI needs.
type JobInfo struct {
	ID             int64  `json:"id"`
	Kind           string `json:"kind"`
	MailboxAddress string `json:"mailbox_address"`
	Target         string `json:"target"`
	Status         string `json:"status"`
	Progress       string `json:"progress"`
	Result         string `json:"result"`
	ErrorMessage   string `json:"error_message"`
}

// IsFinished reports whether the job reached a terminal status.
func (j *JobInfo) IsFinished() bool {
	switch j.Status {
	case JobStatusDone, JobStatusError, JobStatusCanceled:
		return true
	}
	return false
}

type jobEnvelope struct {
	Job     JobInfo `json:"job"`
	Created bool    `json:"created"`
}

// SubmitJobToServer queues a job on the running server. created is false
// when an identical job was already queued or running.
func SubmitJobToServer(port int, req JobRequest) (*JobInfo, bool, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, false, err
	}
	resp, err := controlClient.Post(controlURL(port, "/jobs"), "application/json", bytes.NewReader(body))
	if err != nil {
		return nil, false, fmt.Errorf("failed to connect to server on port %d: %w", port, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, false, controlError(resp)
	}
	var env jobEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, false, fmt.Errorf("failed to parse the job response: %w", err)
	}
	return &env.Job, env.Created, nil
}

// GetServerJob fetches one job from the running server.
func GetServerJob(port int, id int64) (*JobInfo, error) {
	resp, err := controlClient.Get(controlURL(port, fmt.Sprintf("/jobs/%d", id)))
	if err != nil {
		return nil, fmt.Errorf("failed to connect to server on port %d: %w", port, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, controlError(resp)
	}
	var env jobEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, fmt.Errorf("failed to parse the job response: %w", err)
	}
	return &env.Job, nil
}

// WaitForJob polls a job on the running server until it finishes, passing
// every new progress line to echo. It returns the final job.
func WaitForJob(ctx context.Context, port int, id int64, echo func(string)) (*JobInfo, error) {
	printed := ""
	for {
		job, err := GetServerJob(port, id)
		if err != nil {
			return nil, err
		}
		if echo != nil {
			for _, line := range newProgressLines(printed, job.Progress) {
				echo(line)
			}
		}
		printed = job.Progress
		if job.IsFinished() {
			return job, nil
		}
		select {
		case <-ctx.Done():
			return job, ctx.Err()
		case <-time.After(CLIJobPollInterval):
		}
	}
}

// newProgressLines returns the lines of cur that were not part of prev. The
// progress text is a bounded log, so lines may have scrolled off its top; the
// last line of prev is searched from the end of cur to find the boundary.
func newProgressLines(prev, cur string) []string {
	if cur == "" {
		return nil
	}
	curLines := strings.Split(cur, "\n")
	if prev == "" {
		return curLines
	}
	prevLines := strings.Split(prev, "\n")
	lastSeen := prevLines[len(prevLines)-1]
	for i := len(curLines) - 1; i >= 0; i-- {
		if curLines[i] == lastSeen {
			return curLines[i+1:]
		}
	}
	return curLines
}
