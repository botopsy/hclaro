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
	"hclaro/internal/engine"
	"hclaro/internal/operations"
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
}

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
	flags.Usage = func() {
		fmt.Fprintln(output, "Hclaro — local Terraform & OpenTofu explorer\n\nUsage: hclaro [flags]\n\nRun inside a Terraform/OpenTofu root to open it; elsewhere, or with --hub, the Workspace\nHub opens with your registered projects.")
		flags.PrintDefaults()
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

func Run(ctx context.Context, o Options, output io.Writer, version string) error {
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
	engineName := o.Engine
	if root != "" {
		store = workspace.New(workspace.Inspect(root))
		if cli, detectErr := engine.Detect(root, o.Engine); detectErr == nil {
			adapter, planner = cli, cli
			engineName = cli.Name
		}
	}
	registry := projects.Unavailable("The per-user configuration directory is unavailable, so projects cannot be registered.")
	if dir, err := projects.ConfigDir(); err == nil {
		registry = projects.Open(dir, time.Now())
	}
	if _, notice, _ := registry.Status(); notice != "" {
		fmt.Fprintln(output, "Workspace Hub:", notice)
	}
	hub := projects.NewHub(registry, root)
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
	// Hub initialization and the launch project's plans share one claim per project root.
	manager.Locks = hub.Locks
	// A saved plan can hold secrets; remove any left by an earlier Hclaro that was killed.
	if n := manager.SweepStale(time.Now()); n > 0 {
		fmt.Fprintf(output, "Deleted %d saved plan%s left by an earlier session that did not stop cleanly.\n", n, map[bool]string{true: "", false: "s"}[n == 1])
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// After an apply, state is re-read so the explorer shows what the engine recorded.
	manager.Reload = func(ctx context.Context) { store.Load(ctx, adapter, engineName) }
	// Request contexts derive from runCtx so event streams end promptly on shutdown.
	// SSE responses extend their own write deadline per event.
	server := &http.Server{Handler: api.Handler(store, manager, hub, ui.Assets(), host, token, csrf, version), BaseContext: func(net.Listener) context.Context { return runCtx }, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	loaded := make(chan struct{})
	go func() {
		defer close(loaded)
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
	cancel()
	// A running plan or init is interrupted and its private temp files removed before exit.
	manager.Close(20 * time.Second)
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
