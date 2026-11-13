package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"time"

	"hclaro/internal/api"
	"hclaro/internal/cost"
	"hclaro/internal/discovery"
	"hclaro/internal/engine"
	"hclaro/internal/gitctx"
	"hclaro/internal/history"
	"hclaro/internal/operations"
	"hclaro/internal/preflight"
	"hclaro/internal/profiles"
	"hclaro/internal/projects"
	"hclaro/internal/security"
	"hclaro/internal/ui"
	"hclaro/internal/workspace"
)

type Options struct {
	Path, Engine       string
	Port               int
	NoBrowser, Version bool
	// Hub opens the Workspace Hub. PathSet records an explicit --path.
	Hub, PathSet bool
	// ExportHistory writes the redacted operation history to a new file and exits.
	ExportHistory string
	// StrictLocal refuses features that send data to a third-party service; today that is
	// cost estimation (M17).
	StrictLocal bool
	// HubExecution turns on plans and applies in Hub workspaces (ADR M24.03, M24.04–.06). It
	// is a hidden, experimental opt-in: not in --help, off by default, and not yet qualified
	// with real engines (M24.02).
	HubExecution bool
}

// hiddenFlags are accepted but not listed in --help.
var hiddenFlags = map[string]bool{"experimental-hub-execution": true}

func Parse(args []string, output io.Writer) (Options, error) {
	o := Options{}
	flags := flag.NewFlagSet("hclaro", flag.ContinueOnError)
	flags.SetOutput(output)
	flags.StringVar(&o.Path, "path", ".", "Terraform/OpenTofu project directory")
	flags.StringVar(&o.Engine, "engine", "", "Engine: terraform or tofu (default: Terraform first)")
	flags.IntVar(&o.Port, "port", 0, "Loopback port (default: available port)")
	flags.BoolVar(&o.NoBrowser, "no-browser", false, "Print URL without opening a browser")
	flags.BoolVar(&o.Version, "version", false, "Print version and exit")
	flags.BoolVar(&o.Hub, "hub", false, "Open the Workspace Hub; the current directory is not read unless --path is given")
	flags.BoolVar(&o.StrictLocal, "strict-local", false, "Strict local-only mode: disable features that send data to third-party services (cost estimation)")
	flags.StringVar(&o.ExportHistory, "export-history", "", "Write the redacted operation history to a new .json or .md `file` and exit")
	flags.BoolVar(&o.HubExecution, "experimental-hub-execution", false, "Experimental: allow plans and applies in Hub workspaces")
	flags.Usage = func() {
		fmt.Fprintln(output, "Hclaro — local Terraform & OpenTofu explorer\n\nUsage: hclaro [flags]\n\nRun inside a Terraform/OpenTofu root to open it; elsewhere, or with --hub, the Workspace\nHub opens with your registered projects.")
		visible := flag.NewFlagSet("hclaro", flag.ContinueOnError)
		visible.SetOutput(output)
		flags.VisitAll(func(f *flag.Flag) {
			if hiddenFlags[f.Name] {
				return
			}
			visible.Var(f.Value, f.Name, f.Usage)
			visible.Lookup(f.Name).DefValue = f.DefValue
		})
		visible.PrintDefaults()
		fmt.Fprintln(output, "\nLaunch is read-only: no init, plan or refresh runs automatically. Plans run only when\nrequested in the UI. A plan is applied only from its reviewed saved file, after explicit\nconfirmation in the UI, and only for locally executed backends.\nExit codes: 0 success, 1 startup failure, 2 invalid arguments.")
	}
	if err := flags.Parse(args); err != nil {
		return o, err
	}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "path" {
			o.PathSet = true
		}
	})
	if flags.NArg() > 0 {
		return o, errors.New("unexpected positional argument; use --path")
	}
	if o.Port < 0 || o.Port > 65535 {
		return o, errors.New("port must be between 0 and 65535")
	}
	if o.Engine != "" && o.Engine != "terraform" && o.Engine != "tofu" {
		return o, errors.New("engine must be terraform or tofu")
	}
	if o.ExportHistory != "" {
		if _, err := history.ExportFormat(o.ExportHistory); err != nil {
			return o, err
		}
	}
	return o, nil
}

func openBrowser(ctx context.Context, url string) error {
	var command string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		command = "open"
		args = []string{url}
	case "windows":
		command = "rundll32"
		args = []string{"url.dll,FileProtocolHandler", url}
	default:
		command = "xdg-open"
		args = []string{url}
	}
	return exec.CommandContext(ctx, command, args...).Run()
}

// ExportHistory writes the redacted history in the configuration directory to path. It
// starts no server and reads no project.
func ExportHistory(path string, output io.Writer, version string) error {
	dir, err := projects.ConfigDir()
	if err != nil {
		return errors.New("the per-user configuration directory is unavailable")
	}
	store := history.Open(dir, time.Now())
	status, notice, _ := store.Status()
	if notice != "" {
		fmt.Fprintln(output, "Operation history:", notice)
	}
	if status == history.Unavailable || status == history.ReadOnly {
		return errors.New("operation history cannot be exported")
	}
	entries := store.List(history.Filter{})
	report := history.NewReport(entries, history.Filter{}, version, time.Now())
	if err := history.WriteExport(path, report); err != nil {
		return err
	}
	fmt.Fprintf(output, "Wrote %d operation(s) to %s. Log excerpts are not exported; values that looked like secrets were withheld when recorded.\n", len(report.Entries), path)
	return nil
}

func Run(ctx context.Context, o Options, output io.Writer, version string) error {
	if o.ExportHistory != "" {
		return ExportHistory(o.ExportHistory, output, version)
	}
	// Without --hub, the current directory (or --path) is the launch project, as before.
	// Outside a project, an implicit current directory falls back to the Hub; an explicit
	// --path that is not a project still fails.
	root := ""
	if !o.Hub || o.PathSet {
		r, err := workspace.Root(o.Path)
		switch {
		case err == nil:
			root = r
		case o.PathSet:
			return err
		default:
			fmt.Fprintln(output, "No Terraform/OpenTofu project in the current directory; opening the Workspace Hub.")
		}
	}
	store := workspace.NoProject()
	var adapter engine.Adapter
	var planner engine.Planner
	var launchCLI *engine.CLI
	engineName := o.Engine
	if root != "" {
		store = workspace.New(workspace.Inspect(root))
		if cli, detectErr := engine.Detect(root, o.Engine); detectErr == nil {
			adapter, planner, launchCLI = cli, cli, cli
			engineName = cli.Name
		}
	}
	registry := projects.Unavailable("The per-user configuration directory is unavailable, so projects cannot be registered.")
	executionProfiles := profiles.Disabled("The per-user configuration directory is unavailable, so execution profiles cannot be saved.")
	operationHistory := history.Disabled("The per-user configuration directory is unavailable, so operation history is not kept.")
	if dir, err := projects.ConfigDir(); err == nil {
		registry = projects.Open(dir, time.Now())
		executionProfiles = profiles.Open(dir, time.Now())
		operationHistory = history.Open(dir, time.Now())
	}
	if _, notice, _ := operationHistory.Status(); notice != "" {
		fmt.Fprintln(output, "Operation history:", notice)
	}
	if _, notice, _ := registry.Status(); notice != "" {
		fmt.Fprintln(output, "Workspace Hub:", notice)
	}
	if _, notice := executionProfiles.Status(); notice != "" {
		fmt.Fprintln(output, "Execution profiles:", notice)
	}
	hub := projects.NewHub(registry, root)
	hub.Profiles = executionProfiles
	// Off unless the hidden experimental flag is given; every scoped route then answers
	// scoped_execution_disabled and Hub workspaces stay read-only.
	hub.ScopedExecution = o.HubExecution
	if o.HubExecution {
		fmt.Fprintln(output, "Experimental: plans and applies in Hub workspaces are enabled (--experimental-hub-execution). They are not yet qualified with real Terraform/OpenTofu engines; applies still need a reviewed saved plan and your confirmation.")
	}
	hub.UseHistory(operationHistory)
	defer hub.Close()
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(o.Port)))
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return errors.New("loopback TCP is denied by this environment; Hclaro requires permission to listen on 127.0.0.1")
		}
		return errors.New("unable to bind loopback port; choose another --port")
	}
	defer listener.Close()
	token, err := security.Token()
	if err != nil {
		return errors.New("unable to create secure session")
	}
	csrf, err := security.Token()
	if err != nil {
		return errors.New("unable to create secure session")
	}
	host := listener.Addr().String()
	manager := operations.NewManager(store, planner, root)
	manager.Terminal, hub.Terminal = output, output
	// Hub initialization and the launch project's plans share one claim per project root.
	manager.Locks = hub.Locks
	// Execution profiles of the launch project bind a plan to a profile's environment; the
	// launch adapter itself is never changed.
	manager.Binder = hub.LaunchBinder()
	manager.Recorder = operationHistory
	// Apply preflight: Git status is read with a fixed argv; the AWS caller identity probe
	// runs only when the user asks for it in the preflight dialog.
	gitProbe := func(ctx context.Context, root string) preflight.GitResult {
		return preflight.GitStatus(ctx, root, gitctx.Exec{}.Run)
	}
	manager.GitProbe = gitProbe
	// Git context (M15): read-only Git commands with fixed argv, source fingerprints of
	// plans and validated editor links. One service, so file IDs match across routes.
	gitService := gitctx.New()
	manager.Git = gitService
	hub.Git = gitService
	manager.IdentityProbe = preflight.AWSIdentity
	// Hub execution scopes (when enabled) gather the same preflight facts.
	hub.GitProbe, hub.IdentityProbe = gitProbe, preflight.AWSIdentity
	// AWS discovery (M16): allowlisted read-only AWS CLI calls, only on explicit request.
	manager.Discovery = discovery.New(discovery.Exec{})
	hub.Discovery = manager.Discovery
	// Cost estimation (M17): off until the user opts in after the disclosure; never in
	// strict local-only mode. Only sanitized aggregates are saved in the config folder.
	manager.Cost = cost.New(cost.Exec{}, o.StrictLocal)
	if dir, err := projects.ConfigDir(); err == nil {
		manager.Cost.Store = cost.OpenStore(dir)
	}
	cost.SweepStale("", time.Now())
	if launchCLI != nil {
		manager.Bind = func(environ []string, ws string) engine.Planner { return launchCLI.WithEnvironment(environ, ws) }
	}
	// A saved plan can hold secrets; remove any left by an earlier Hclaro that was killed.
	if n := manager.SweepStale(time.Now()); n > 0 {
		fmt.Fprintf(output, "Deleted %d saved plan%s left by an earlier session that did not stop cleanly.\n", n, map[bool]string{true: "", false: "s"}[n == 1])
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// After an apply, state is re-read so the explorer shows what the engine recorded.
	manager.Reload = func(ctx context.Context) { store.Load(ctx, adapter, engineName) }
	if root != "" {
		// "Read again" and a Hub init of this folder inspect it again before reading state.
		manager.Reinspect = func() workspace.Info { return workspace.Inspect(root) }
		hub.LaunchChanged = func() { _ = manager.Reread() }
	}
	// Request contexts derive from runCtx so event streams end promptly on shutdown.
	// WriteTimeout covers short handlers only: SSE responses extend their own write deadline per
	// event, and handlers that run engines or probes extend it past their own bound (withDeadline).
	server := &http.Server{Handler: api.Handler(store, manager, hub, ui.Assets(), host, token, csrf, version), BaseContext: func(net.Listener) context.Context { return runCtx }, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	loaded := make(chan struct{})
	// The startup read holds a shared claim, taken before the server accepts requests, so a
	// Hub init or workspace command waits for it.
	readRelease := func() {}
	if root != "" {
		if release, ok := hub.Locks.AcquireShared(root); ok {
			readRelease = release
		}
	}
	go func() {
		defer close(loaded)
		defer readRelease()
		if root != "" {
			store.Load(runCtx, adapter, engineName)
		}
	}()
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	url := "http://" + host + "/#session=" + token
	reading := "Reading recorded state; the CLI may contact the configured backend."
	if root == "" || o.Hub {
		url = "http://" + host + "/projects#session=" + token
	}
	if root == "" {
		reading = "Workspace Hub: nothing is read until you open a project."
	}
	fmt.Fprintf(output, "Hclaro %s · local Terraform & OpenTofu workspace\n%s\nOpen this private session URL:\n%s\nPress Ctrl+C to stop.\n", version, reading, url)
	if !o.NoBrowser {
		go func() {
			browserCtx, done := context.WithTimeout(runCtx, 5*time.Second)
			defer done()
			if openBrowser(browserCtx, url) != nil {
				fmt.Fprintln(output, "Browser did not open. Use the private session URL above.")
			}
		}()
	}
	var serveErr error
	select {
	case <-ctx.Done():
	case err := <-served:
		if !errors.Is(err, http.ErrServerClosed) {
			serveErr = errors.New("local HTTP server stopped unexpectedly")
		}
	}
	if manager.Applying() || hub.Applying() {
		fmt.Fprintf(output, "An apply is running. Hclaro interrupts it once and waits up to %s for the engine to finish writing state.\nDo not force-quit Hclaro: the engine writes to Hclaro and could be stopped mid-apply.\n", engine.ApplyGracePeriod)
	}
	cancel()
	// A running plan or init is interrupted and its private temp files removed before exit.
	manager.Close(20 * time.Second)
	manager.Discovery.Close(5 * time.Second)
	manager.Cost.Close(5 * time.Second)
	hub.Inits.Close(20 * time.Second)
	shutdownCtx, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	_ = server.Shutdown(shutdownCtx)
	select {
	case <-loaded:
	case <-shutdownCtx.Done():
	}
	return serveErr
}
