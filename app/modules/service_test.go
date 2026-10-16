package modules

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/alecthomas/kong"

	"mailcare/app/models"
)

// fakeControlServer answers the control endpoints on a loopback port the way
// a MailCare server serving dataDir does, counting the job submissions and
// shutdown requests it receives. It stands in for a server started for
// another data directory (or, once dataDir is changed, for the data directory of the test).
type fakeControlServer struct {
	port int
	mu   sync.Mutex

	dataDir   string
	jobs      int
	shutdowns int
}

func newFakeControlServer(t *testing.T, dataDir string) *fakeControlServer {
	t.Helper()
	f := &fakeControlServer{dataDir: dataDir}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /control/status", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		st := ServerStatus{Status: "running", Name: AppName, Version: "test", WebListen: "127.0.0.1:0", DataDir: f.dataDir}
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(st)
	})
	mux.HandleFunc("POST /control/jobs", func(w http.ResponseWriter, r *http.Request) {
		var req JobRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.mu.Lock()
		f.jobs++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(jobEnvelope{Job: JobInfo{ID: 1, Kind: req.Kind, Status: JobStatusQueued}, Created: true})
	})
	mux.HandleFunc("POST /control/shutdown", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.shutdowns++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "shutting down"})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	f.port, err = strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fakeControlServer) setDataDir(dir string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dataDir = dir
}

func (f *fakeControlServer) counts() (jobs, shutdowns int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.jobs, f.shutdowns
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	func() {
		defer func() {
			os.Stdout = orig
			w.Close()
		}()
		fn()
	}()
	return <-done
}

func TestSameDataDir(t *testing.T) {
	dir := t.TempDir()
	if !SameDataDir(dir, dir) {
		t.Errorf("a directory does not match itself")
	}
	// Cleaning and relative segments do not matter.
	if !SameDataDir(dir, filepath.Join(dir, "sub", "..")+string(os.PathSeparator)) {
		t.Errorf("uncleaned form of the same directory not matched")
	}
	// A symbolic link to the directory names the same directory.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(dir, link); err == nil && !SameDataDir(dir, link) {
		t.Errorf("symlink %s to %s not matched", link, dir)
	}
	if SameDataDir(dir, filepath.Join(dir, "other")) {
		t.Errorf("different directories matched")
	}
	// A server that does not report its directory never matches.
	if SameDataDir("", dir) || SameDataDir(dir, "") || SameDataDir("", "") {
		t.Errorf("empty path matched")
	}
	// DataDir itself is absolute.
	prev := dataDirOverride
	SetDataDir("relative-data")
	t.Cleanup(func() { SetDataDir(prev) })
	if got, err := DataDir(); err != nil || !filepath.IsAbs(got) || filepath.Base(got) != "relative-data" {
		t.Errorf("DataDir() = %q, %v; want an absolute path ending in relative-data", got, err)
	}
}

// TestRunJobsSkipsServerOfAnotherDataDir: a MailCare server answering on the
// configured port but started for another data directory must not receive
// the CLI's jobs (their mailbox ids belong to the database of the CLI); the
// jobs run in this process as when no server runs. Once the server reports
// the data directory of the CLI the jobs are handed to it.
func TestRunJobsSkipsServerOfAnotherDataDir(t *testing.T) {
	db := newTestDB(t)
	key, err := LoadSecretKey(db)
	if err != nil {
		t.Fatal(err)
	}
	mb, _ := seedMailbox(t, db, key, "ops@example.test", testSamples)
	if err := SetAgentEnabled(db, false); err != nil {
		t.Fatal(err)
	}
	fake := newFakeControlServer(t, filepath.Join(t.TempDir(), "other-data"))
	if err := models.SetSetting(db, SettingWebPort, strconv.Itoa(fake.port)); err != nil {
		t.Fatal(err)
	}
	specs := []jobSpec{{kind: JobKindReindex, mailboxID: mb.ID, address: mb.Address}}

	var runErr error
	out := captureStdout(t, func() { runErr = runJobs(db, specs, false) })
	if runErr != nil {
		t.Fatalf("runJobs with a foreign server: %v\n%s", runErr, out)
	}
	if !strings.Contains(out, "A server is running on port "+strconv.Itoa(fake.port)+" for another data directory; running in this process.") {
		t.Errorf("foreign server not announced:\n%s", out)
	}
	if jobs, _ := fake.counts(); jobs != 0 {
		t.Errorf("%d job(s) were submitted to the server of another data directory", jobs)
	}
	local, err := models.ListJobs(db, 10)
	if err != nil || len(local) != 1 || local[0].Kind != JobKindReindex || local[0].Status != JobStatusDone {
		t.Fatalf("local jobs after the inline run: %+v, %v", local, err)
	}
	if !strings.Contains(out, "Job #"+strconv.Itoa(int(local[0].ID))+" done: ") {
		t.Errorf("inline outcome not printed:\n%s", out)
	}

	// The same server reporting the data directory of the CLI receives the job.
	dataDir, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	fake.setDataDir(dataDir)
	out = captureStdout(t, func() { runErr = runJobs(db, specs, false) })
	if runErr != nil {
		t.Fatalf("runJobs with the matching server: %v\n%s", runErr, out)
	}
	if !strings.Contains(out, "Server is running on port "+strconv.Itoa(fake.port)+"; submitting to it.") || !strings.Contains(out, "Queued job #1") {
		t.Errorf("submission not announced:\n%s", out)
	}
	if jobs, _ := fake.counts(); jobs != 1 {
		t.Errorf("jobs submitted to the matching server = %d, want 1", jobs)
	}
	if local, _ := models.ListJobs(db, 10); len(local) != 1 {
		t.Errorf("a local job was created although the server took the job: %+v", local)
	}

	// Without any server the jobs run in this process, silently about servers.
	if err := models.SetSetting(db, SettingWebPort, strconv.Itoa(closedPort(t))); err != nil {
		t.Fatal(err)
	}
	out = captureStdout(t, func() { runErr = runJobs(db, specs, false) })
	if runErr != nil || strings.Contains(out, "server is running") || strings.Contains(out, "Server is running") {
		t.Errorf("run without a server: %v\n%s", runErr, out)
	}
	if local, _ := models.ListJobs(db, 10); len(local) != 2 {
		t.Errorf("local jobs after the second inline run: %d, want 2", len(local))
	}
}

// TestServiceStopAndStatusCheckDataDir: "service stop" refuses to stop a
// server started for another data directory, and "service status" shows the
// directory a server serves.
func TestServiceStopAndStatusCheckDataDir(t *testing.T) {
	newTestDB(t)
	dataDir, err := DataDir()
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other-data")
	fake := newFakeControlServer(t, other)

	err = (&ServiceStopCmd{WebPort: &fake.port}).Run()
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != ExitExec ||
		!strings.Contains(exitErr.Message, "the server on port "+strconv.Itoa(fake.port)+" serves another data directory") ||
		!strings.Contains(exitErr.Message, other) {
		t.Errorf("stop of a foreign server: %v", err)
	}
	if _, shutdowns := fake.counts(); shutdowns != 0 {
		t.Errorf("the server of another data directory was asked to shut down")
	}
	out := captureStdout(t, func() { err = (&ServiceStatusCmd{WebPort: &fake.port}).Run() })
	if err != nil || !strings.Contains(out, "data_dir:    "+other) {
		t.Errorf("status of a foreign server: %v\n%s", err, out)
	}

	fake.setDataDir(dataDir)
	out = captureStdout(t, func() { err = (&ServiceStopCmd{WebPort: &fake.port}).Run() })
	if err != nil || !strings.Contains(out, "Server shutdown initiated") {
		t.Errorf("stop of the matching server: %v\n%s", err, out)
	}
	if _, shutdowns := fake.counts(); shutdowns != 1 {
		t.Errorf("shutdown requests = %d, want 1", shutdowns)
	}

	// No server at all is still reported as a connection failure.
	closed := closedPort(t)
	err = (&ServiceStopCmd{WebPort: &closed}).Run()
	if !errors.As(err, &exitErr) || exitErr.Code != ExitExec || !strings.Contains(exitErr.Message, "failed to connect to server") {
		t.Errorf("stop without a server: %v", err)
	}
}

// serviceTestVars are the help-text variables the service flags refer to
// (main.go defines them for the real command tree).
var serviceTestVars = kong.Vars{
	"default_web_listen": DefaultWebListenAddr, "default_web_port": strconv.Itoa(DefaultWebPort),
	"default_workers": strconv.Itoa(DefaultWorkers), "max_workers": strconv.Itoa(MaxWorkers),
	"default_mail_keep_days": strconv.Itoa(DefaultMailKeepDays), "min_mail_keep_days": strconv.Itoa(MinMailKeepDays),
	"max_mail_keep_days": strconv.Itoa(MaxMailKeepDays),
}

// parseServiceArgs parses "service ..." arguments the way main.go does and
// returns the command tree.
func parseServiceArgs(t *testing.T, args ...string) *ServiceCmd {
	t.Helper()
	var cli struct {
		Service ServiceCmd `cmd:""`
	}
	parser, err := kong.New(&cli, serviceTestVars)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.Parse(append([]string{"service"}, args...)); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return &cli.Service
}

// TestServiceStartFlagsDistinguishZeroFromOmitted: an omitted numeric flag
// means "use the saved setting" (0 is handed to the server), while a given
// value is validated as it is, so --web-port 0, --workers 0 and
// --mail-keep-days 0 are argument errors instead of being ignored.
func TestServiceStartFlagsDistinguishZeroFromOmitted(t *testing.T) {
	type startCall struct {
		webListen              string
		webPort, workers, keep int
		times                  []string
	}
	var calls []startCall
	prev := StartServer
	StartServer = func(webListen string, webPort int, workers int, checkTimes []string, mailKeepDays int) error {
		calls = append(calls, startCall{webListen, webPort, workers, mailKeepDays, checkTimes})
		return nil
	}
	t.Cleanup(func() { StartServer = prev })

	// Parsing: omitted flags stay nil, given ones (0 included) are set.
	svc := parseServiceArgs(t, "start")
	if svc.Start.WebPort != nil || svc.Start.Workers != nil || svc.Start.MailKeepDays != nil {
		t.Errorf("omitted flags parsed as given: %+v", svc.Start)
	}
	svc = parseServiceArgs(t, "start", "--web-port", "0", "--workers", "0", "--mail-keep-days", "0")
	if svc.Start.WebPort == nil || *svc.Start.WebPort != 0 || svc.Start.Workers == nil || *svc.Start.Workers != 0 ||
		svc.Start.MailKeepDays == nil || *svc.Start.MailKeepDays != 0 {
		t.Errorf("explicit zeros not parsed as given: %+v", svc.Start)
	}

	// Explicit out-of-range values (0 included) are argument errors and the
	// server is not started.
	var exitErr *ExitError
	for _, tc := range []struct{ args, message string }{
		{"--web-port 0", "web-port must be between 1 and 65535"},
		{"--web-port 65536", "web-port must be between 1 and 65535"},
		{"--workers 0", "workers must be between 1 and " + strconv.Itoa(MaxWorkers)},
		{"--workers " + strconv.Itoa(MaxWorkers+1), "workers must be between 1 and " + strconv.Itoa(MaxWorkers)},
		{"--mail-keep-days 0", "mail_keep_days must be an integer between " + strconv.Itoa(MinMailKeepDays) + " and " + strconv.Itoa(MaxMailKeepDays)},
		{"--mail-keep-days " + strconv.Itoa(MaxMailKeepDays+1), "mail_keep_days must be an integer between " + strconv.Itoa(MinMailKeepDays) + " and " + strconv.Itoa(MaxMailKeepDays)},
	} {
		svc := parseServiceArgs(t, append([]string{"start"}, strings.Fields(tc.args)...)...)
		err := svc.Start.Run()
		if !errors.As(err, &exitErr) || exitErr.Code != ExitArgument || exitErr.Message != tc.message {
			t.Errorf("service start %s: %v, want argument error %q", tc.args, err, tc.message)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("the server was started with invalid flags: %+v", calls)
	}

	// Omitted flags reach the server as 0 (fall back to the saved settings);
	// given values reach it unchanged.
	if err := parseServiceArgs(t, "start").Start.Run(); err != nil {
		t.Fatalf("service start: %v", err)
	}
	if err := parseServiceArgs(t, "start", "--web-listen", "127.0.0.1", "--web-port", "8080", "--workers", "3",
		"--mail-keep-days", "10", "--check-time", "06:00").Start.Run(); err != nil {
		t.Fatalf("service start with flags: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("server starts = %d, want 2", len(calls))
	}
	if c := calls[0]; c.webListen != "" || c.webPort != 0 || c.workers != 0 || c.keep != 0 || c.times != nil {
		t.Errorf("omitted flags handed to the server as %+v", c)
	}
	if c := calls[1]; c.webListen != "127.0.0.1" || c.webPort != 8080 || c.workers != 3 || c.keep != 10 ||
		len(c.times) != 1 || c.times[0] != "06:00" {
		t.Errorf("given flags handed to the server as %+v", c)
	}

	// stop / status apply the same rule to --web-port.
	svc = parseServiceArgs(t, "stop", "--web-port", "0")
	if err := svc.Stop.Run(); !errors.As(err, &exitErr) || exitErr.Code != ExitArgument || exitErr.Message != "web-port must be between 1 and 65535" {
		t.Errorf("service stop --web-port 0: %v, want an argument error", err)
	}
	svc = parseServiceArgs(t, "status", "--web-port", "0")
	if err := svc.Status.Run(); !errors.As(err, &exitErr) || exitErr.Code != ExitArgument || exitErr.Message != "web-port must be between 1 and 65535" {
		t.Errorf("service status --web-port 0: %v, want an argument error", err)
	}
}

// The control token is written with its port and mode 0600, replaced in one
// step, and removed only by the server that wrote it.
func TestControlTokenFile(t *testing.T) {
	dir := t.TempDir()
	if err := WriteControlToken(context.Background(), dir, 9790, "first"); err != nil {
		t.Fatal(err)
	}
	if err := WriteControlToken(context.Background(), dir, 9791, "second"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, ControlTokenFile))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("mode %v", info.Mode().Perm())
	}
	if port, got := ReadControlToken(dir); port != 9791 || got != "second" {
		t.Errorf("token %d %q", port, got)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("left over files: %v", entries)
	}
	RemoveControlToken(dir, "first") // not the current token: kept
	if _, got := ReadControlToken(dir); got != "second" {
		t.Error("removed another server's token")
	}
	RemoveControlToken(dir, "second")
	if _, got := ReadControlToken(dir); got != "" {
		t.Error("token file kept")
	}
}

// refusingServer answers every request with the refusal of a server of the
// data directory dir and records the token it received.
func refusingServer(t *testing.T, dir string, got *string) int {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*got = r.Header.Get(ControlTokenHeader)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"` + ControlTokenRefused + `","data_dir_id":"` + DataDirID(dir) + `"}`))
	}))
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().(*net.TCPAddr).Port
}

// The CLI sends the token of its data directory only to the port stored
// with it. A server that refuses the token while it serves the data
// directory of the CLI (another OS user, a stale token) is reported as such,
// so the CLI never works on the data in the CLI process; a server of another
// data directory leaves the CLI free to do so.
func TestControlTokenSentAndRefused(t *testing.T) {
	dir := t.TempDir()
	SetDataDir(dir)
	t.Cleanup(func() { SetDataDir("") })
	var got string
	port := refusingServer(t, dir, &got)
	if err := WriteControlToken(context.Background(), dir, port, "cli-token"); err != nil {
		t.Fatal(err)
	}
	st := LocalServer(port)
	if got != "cli-token" || st == nil || !st.RefusedOwnDir || st.ServesDataDir(dir) {
		t.Errorf("token sent %q, status %+v", got, st)
	}
	if _, err := GetServerStatus(port); err == nil || !strings.Contains(err.Error(), "serves this data directory but refused") {
		t.Errorf("status error: %v", err)
	}
	// Another port gets no token.
	otherPort := refusingServer(t, dir, &got)
	if LocalServer(otherPort); got != "" {
		t.Errorf("token sent to another port: %q", got)
	}
	// A server of another data directory.
	other := refusingServer(t, filepath.Join(t.TempDir(), "other"), &got)
	if st := LocalServer(other); st == nil || st.RefusedOwnDir {
		t.Errorf("other data directory: %+v", st)
	}
	if _, err := GetServerStatus(other); err == nil || !strings.Contains(err.Error(), "serves another data directory") {
		t.Errorf("other status error: %v", err)
	}
	// A refusal that is not about the token keeps its message.
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"error":"forwarded requests are refused"}`))
	}))
	defer plain.Close()
	if _, err := GetServerStatus(plain.Listener.Addr().(*net.TCPAddr).Port); err == nil || err.Error() != "forwarded requests are refused" {
		t.Errorf("plain 403: %v", err)
	}
}

// The server lock admits one holder per data directory; the release (a
// clean shutdown) removes the file and frees the lock; a file left behind
// (a crash) does not block the next start.
func TestServerLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ServerLockFile)
	release, err := AcquireServerLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("lock file while held: %v", err)
	}
	if _, err := AcquireServerLock(dir); !errors.Is(err, ErrServerRunning) {
		t.Errorf("second lock: %v", err)
	}
	release()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("lock file after the release: %v", err)
	}
	if err := os.WriteFile(path, []byte("12345\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	release2, err := AcquireServerLock(dir)
	if err != nil {
		t.Fatalf("over a left file: %v", err)
	}
	release2()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("lock file after the second release: %v", err)
	}
}
