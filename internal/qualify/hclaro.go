package qualify

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// sessionURL is the line after "Open this private session URL:" (internal/cli/cli.go).
var sessionURL = regexp.MustCompile(`^http://(127\.0\.0\.1:\d+)/(projects)?#session=([a-f0-9]{64})$`)

// Hclaro is one running hclaro process.
type Hclaro struct {
	Host, Token, Origin string
	Stdout, Stderr      string
	Dirs                ProcessDirs
	Env                 []string
	cmd                 *exec.Cmd
	done                chan struct{}
	waitErr             error
	client              *Client
	scenario            *Scenario
	// shutdown is set when the scenario stopped Hclaro itself (A7, A7b); its exit code is
	// recorded by the scenario, not judged by the cleanup.
	shutdown bool
	// view is set on a Hub workspace view (In); nil for the process itself.
	view *HubView
}

// HubView is one open Hub workspace (read context) of a registered project (M27.05).
type HubView struct {
	ContextID, ProjectID, Project, Engine, Workspace string
}

// In returns a handle on the same process whose launch routes go to the view's scoped
// routes. Stopping and cleanup stay with the process handle.
func (h *Hclaro) In(v HubView) *Hclaro {
	c := h.client
	view := *h
	view.view = &v
	view.client = &Client{h: &view, s: c.s, http: c.http, csrf: c.csrf, transcript: c.transcript, prefix: "contexts/" + v.ContextID + "/"}
	return &view
}

// View is the Hub workspace of a view handle, nil for the process.
func (h *Hclaro) View() *HubView { return h.view }

// StartHclaro starts the binary with args and the allowlisted env, waits for the session URL
// and reads the CSRF token.
func (s *Scenario) StartHclaro(dirs ProcessDirs, env []string, extra map[string]string, args ...string) (*Hclaro, error) {
	if err := CheckEnvironment(env, extra); err != nil {
		return nil, err
	}
	for k := range extra {
		s.result.ExtraEnvironment = append(s.result.ExtraEnvironment, k)
	}
	n := len(s.procs)
	h := &Hclaro{Dirs: dirs, Env: env, done: make(chan struct{}), scenario: s,
		Stdout: filepath.Join(s.Dir, fmt.Sprintf("hclaro-%d.stdout", n)),
		Stderr: filepath.Join(s.Dir, fmt.Sprintf("hclaro-%d.stderr", n))}
	stdout, err := os.Create(h.Stdout)
	if err != nil {
		return nil, err
	}
	stderr, err := os.Create(h.Stderr)
	if err != nil {
		stdout.Close()
		return nil, err
	}
	h.cmd = exec.Command(s.run.Cfg.Hclaro, args...)
	h.cmd.Env, h.cmd.Dir, h.cmd.Stderr = env, dirs.Home, stderr
	prepareStop(h.cmd)
	pipe, err := h.cmd.StdoutPipe()
	if err != nil {
		stdout.Close()
		stderr.Close()
		return nil, err
	}
	if err := h.cmd.Start(); err != nil {
		stdout.Close()
		stderr.Close()
		return nil, err
	}
	s.procs = append(s.procs, h)
	found := make(chan string, 1)
	go func() {
		r := bufio.NewReader(pipe)
		sent := false
		for {
			line, err := r.ReadString('\n')
			_, _ = stdout.WriteString(line)
			if m := sessionURL.FindString(strings.TrimSpace(line)); m != "" && !sent {
				found <- m
				sent = true
			}
			if err != nil {
				break
			}
		}
		_, _ = io.Copy(stdout, r)
		h.waitErr = h.cmd.Wait()
		stdout.Close()
		stderr.Close()
		close(h.done)
	}()
	select {
	case raw := <-found:
		m := sessionURL.FindStringSubmatch(raw)
		h.Host, h.Token, h.Origin = m[1], m[3], "http://"+m[1]
		// The token is a credential for this session: never in any artifact or result.
		s.run.San.Secret(h.Token, "<SESSION>")
	case <-h.done:
		return nil, fmt.Errorf("hclaro exited before printing the session URL: %v", h.waitErr)
	case <-time.After(60 * time.Second):
		return nil, errors.New("hclaro did not print the session URL within 60 s")
	}
	h.client = newClient(h, s)
	if err := h.client.session(); err != nil {
		return nil, err
	}
	return h, nil
}

// Client returns the HTTP client bound to this process.
func (h *Hclaro) Client() *Client { return h.client }

// Stop interrupts Hclaro (SIGINT on Unix, CTRL_BREAK on Windows) and waits up to timeout.
// It returns the exit code. A process that does not stop is killed and reported.
func (h *Hclaro) Stop(timeout time.Duration) (int, error) {
	select {
	case <-h.done:
	default:
		if err := interruptProcess(h.cmd.Process); err != nil {
			_ = h.cmd.Process.Kill()
			<-h.done
			return -1, fmt.Errorf("interrupt failed (%v); hclaro killed", err)
		}
		select {
		case <-h.done:
		case <-time.After(timeout):
			_ = h.cmd.Process.Kill()
			<-h.done
			return -1, fmt.Errorf("hclaro did not stop within %s after the interrupt; killed", timeout)
		}
	}
	if h.cmd.ProcessState == nil {
		return -1, h.waitErr
	}
	return h.cmd.ProcessState.ExitCode(), nil
}

// Interrupt sends the stop signal (SIGINT on Unix, CTRL_BREAK on Windows) without waiting;
// the scenario then observes the shutdown phases itself.
func (h *Hclaro) Interrupt() error {
	h.shutdown = true
	return interruptProcess(h.cmd.Process)
}

// Exited reports whether the process has ended.
func (h *Hclaro) Exited() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// Wait waits up to d for the process to end and returns its exit code.
func (h *Hclaro) Wait(d time.Duration) (int, bool) {
	select {
	case <-h.done:
		if h.cmd.ProcessState == nil {
			return -1, true
		}
		return h.cmd.ProcessState.ExitCode(), true
	case <-time.After(d):
		return 0, false
	}
}

// Pid of the running process.
func (h *Hclaro) Pid() int { return h.cmd.Process.Pid }

func (h *Hclaro) baseURL() *url.URL { return &url.URL{Scheme: "http", Host: h.Host} }

// leftoverProcesses lists running processes whose executable or command line is under root,
// other than the excluded PIDs. Engines are copied into <root>/.../bin and providers run
// from <root>/.../.terraform, so this covers orphans as well as Hclaro's process tree.
// shortcut: sampled once after the scenario, not continuously; a process that starts and
// exits between samples is not seen. Upgrade to continuous sampling if a scenario needs it.
func leftoverProcesses(root string, exclude map[int]bool) ([]string, error) {
	procs, err := listProcesses()
	if err != nil {
		return nil, err
	}
	forms := pathForms(root)
	var out []string
	for _, p := range procs {
		if exclude[p.pid] {
			continue
		}
	match:
		for _, field := range strings.Fields(p.command) {
			for _, f := range forms {
				if strings.Contains(field, f) {
					out = append(out, fmt.Sprintf("pid %d %s", p.pid, filepath.Base(field)))
					break match
				}
			}
		}
	}
	return out, nil
}

type process struct {
	pid     int
	command string
}

// enginePIDs lists running processes started from the engine binary in bin (a Process bin
// directory), whatever their subcommand.
func enginePIDs(bin, engine string) ([]int, error) {
	procs, err := listProcesses()
	if err != nil {
		return nil, err
	}
	var out []int
	for _, p := range procs {
		for _, form := range pathForms(filepath.Join(bin, exeName(engine))) {
			if strings.HasPrefix(p.command, form) || strings.Contains(p.command, " "+form) {
				out = append(out, p.pid)
				break
			}
		}
	}
	return out, nil
}
