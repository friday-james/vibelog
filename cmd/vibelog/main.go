package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/friday-james/vibelog/internal/gitcmd"
	"github.com/friday-james/vibelog/internal/initcmd"
	"github.com/friday-james/vibelog/internal/mcpserver"
	"github.com/friday-james/vibelog/internal/observecmd"
	"github.com/friday-james/vibelog/internal/serve"
	"github.com/friday-james/vibelog/internal/store"
	"github.com/friday-james/vibelog/internal/watchcmd"
)

const version = "0.1.0-dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() { usage(os.Stderr) }
	flag.Parse()

	if *showVersion {
		fmt.Println("vibelog", version)
		return
	}

	args := flag.Args()
	if len(args) == 0 {
		usage(os.Stderr)
		os.Exit(2)
	}

	switch args[0] {
	case "init":
		runInit(args[1:])
	case "load":
		runLoad(args[1:])
	case "mcp":
		runMCP(args[1:])
	case "watch":
		runWatch(args[1:])
	case "observe":
		runObserve(args[1:])
	case "serve":
		runServe(args[1:])
	case "ingest-git":
		runIngestGit(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "vibelog: unknown subcommand %q\n\n", args[0])
		usage(os.Stderr)
		os.Exit(2)
	}
}

func runInit(args []string) {
	var dir string
	if len(args) > 0 {
		dir = args[0]
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "cwd:", err)
			os.Exit(1)
		}
		dir = cwd
	}
	if err := initcmd.Run(dir); err != nil {
		fmt.Fprintln(os.Stderr, "vibelog init:", err)
		os.Exit(1)
	}
	syncDir := filepath.Join(dir, ".sync")
	fmt.Println("initialized", syncDir)
	fmt.Println("next: edit", filepath.Join(syncDir, "anchor.yaml"), "to replace the TODOs")
}

func runLoad(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: vibelog load <dir>")
		os.Exit(2)
	}
	state, err := store.Load(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "vibelog load:", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(state); err != nil {
		fmt.Fprintln(os.Stderr, "encode:", err)
		os.Exit(1)
	}
}

func runMCP(args []string) {
	var dir string
	if len(args) > 0 {
		dir = args[0]
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "cwd:", err)
			os.Exit(1)
		}
		dir = cwd
	}
	if err := mcpserver.Serve(dir); err != nil {
		fmt.Fprintln(os.Stderr, "vibelog mcp:", err)
		os.Exit(1)
	}
}

func runWatch(args []string) {
	var dir string
	if len(args) > 0 {
		dir = args[0]
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "cwd:", err)
			os.Exit(1)
		}
		dir = cwd
	}
	if err := watchcmd.Run(dir); err != nil {
		fmt.Fprintln(os.Stderr, "vibelog watch:", err)
		os.Exit(1)
	}
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 7100, "port to listen on")
	projectsFlag := fs.String("projects", "", "comma-separated multi-project list: name=dir,name2=dir2 (no auto-register)")
	configFlag := fs.String("config", "", "path to projects config (YAML list of {name, path}); read-only snapshot, no auto-register")
	fs.Parse(args)

	addr := fmt.Sprintf("localhost:%d", *port)

	// Explicit modes: -projects flag and -config file bypass the auto-register
	// dance. Useful for CI or for serving a fixed list without persisting.
	if projects, err := serve.ParseProjectsFlag(*projectsFlag); err != nil {
		fmt.Fprintln(os.Stderr, "vibelog serve: -projects:", err)
		os.Exit(1)
	} else if len(projects) > 0 {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		releaseMarkers, err := acquireActiveMarkers(projects)
		if err != nil {
			fmt.Fprintln(os.Stderr, "vibelog serve:", err)
			os.Exit(1)
		}
		defer releaseMarkers()
		if err := serve.RunMultiContext(ctx, projects, addr); err != nil {
			fmt.Fprintln(os.Stderr, "vibelog serve:", err)
			os.Exit(1)
		}
		return
	}
	if *configFlag != "" {
		projects, err := serve.LoadProjectsConfig(*configFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "vibelog serve: -config:", err)
			os.Exit(1)
		}
		if len(projects) == 0 {
			fmt.Fprintln(os.Stderr, "vibelog serve: -config:", *configFlag, "has no project entries")
			os.Exit(1)
		}
		fmt.Printf("vibelog: %d projects loaded from %s\n", len(projects), *configFlag)
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()
		releaseMarkers, err := acquireActiveMarkers(projects)
		if err != nil {
			fmt.Fprintln(os.Stderr, "vibelog serve:", err)
			os.Exit(1)
		}
		defer releaseMarkers()
		if err := serve.RunMultiContext(ctx, projects, addr); err != nil {
			fmt.Fprintln(os.Stderr, "vibelog serve:", err)
			os.Exit(1)
		}
		return
	}

	// Default behavior: resolve this invocation's project, then either register
	// with a running vibelog or start a fresh server.
	var dir string
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "cwd:", err)
			os.Exit(1)
		}
		dir = cwd
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vibelog serve: abs path:", err)
		os.Exit(1)
	}
	project := serve.Project{Name: filepath.Base(absDir), Path: absDir}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	releaseMarkers, err := acquireActiveMarkers([]serve.Project{project})
	if err != nil {
		fmt.Fprintln(os.Stderr, "vibelog serve:", err)
		os.Exit(1)
	}
	defer releaseMarkers()

	// Is another vibelog already running on this addr? If so, open a long-lived
	// lease — the project stays registered as long as THIS process stays alive.
	// Ctrl+C drops the connection, server deregisters automatically.
	if serve.ProbeRunning(addr) {
		fmt.Printf("vibelog: leased %s with running serve at http://%s\n", project.Name, addr)
		fmt.Printf("  → http://%s/p/%s/   (Ctrl+C to deregister)\n", addr, project.Name)
		if err := serve.LeaseProject(ctx, addr, project); err != nil && ctx.Err() == nil {
			fmt.Fprintln(os.Stderr, "vibelog serve:", err)
			os.Exit(1)
		}
		fmt.Println("vibelog: lease released")
		return
	}

	// Nothing running. Start a fresh serve with this project as the seed.
	if err := serve.RunMultiContext(ctx, []serve.Project{project}, addr); err != nil {
		fmt.Fprintln(os.Stderr, "vibelog serve:", err)
		os.Exit(1)
	}
}

func acquireActiveMarkers(projects []serve.Project) (func(), error) {
	releases := make([]func(), 0, len(projects))
	for _, p := range projects {
		release, err := serve.AcquireActiveMarker(p.Path)
		if err != nil {
			for i := len(releases) - 1; i >= 0; i-- {
				releases[i]()
			}
			return nil, err
		}
		releases = append(releases, release)
	}
	return func() {
		for i := len(releases) - 1; i >= 0; i-- {
			releases[i]()
		}
	}, nil
}

func runIngestGit(args []string) {
	fs := flag.NewFlagSet("ingest-git", flag.ExitOnError)
	limit := fs.Int("n", 0, "max commits to ingest (0 = all)")
	fs.Parse(args)

	var dir string
	if fs.NArg() > 0 {
		dir = fs.Arg(0)
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "cwd:", err)
			os.Exit(1)
		}
		dir = cwd
	}
	res, err := gitcmd.Run(dir, *limit)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vibelog ingest-git:", err)
		os.Exit(1)
	}
	fmt.Printf("ingest-git: %d added, %d skipped\n", res.Added, res.Skipped)
}

func runObserve(args []string) {
	var dir string
	if len(args) > 0 {
		dir = args[0]
	}
	// If no arg, observecmd will fall back to payload.cwd from stdin.
	if err := observecmd.Run(dir); err != nil {
		fmt.Fprintln(os.Stderr, "vibelog observe:", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: vibelog [--version] <subcommand> [args...]")
	fmt.Fprintln(w, "subcommands:")
	fmt.Fprintln(w, "  init [dir]    create <dir>/.sync/ skeleton (default dir: cwd)")
	fmt.Fprintln(w, "  load <dir>    parse <dir>/.sync/ and print the validated state as JSON")
	fmt.Fprintln(w, "  mcp [dir]     run MCP stdio server, mutating <dir>/.sync/ (default dir: cwd)")
	fmt.Fprintln(w, "  watch [dir]   tail <dir>/.sync/iterations.jsonl, pretty-print new entries")
	fmt.Fprintln(w, "  observe [dir] Stop-hook handler — reads payload from stdin, auto-records iteration")
	fmt.Fprintln(w, "  serve [dir] [-port N]  host the dashboard UI on http://localhost:7100 and enable logging while it runs")
	fmt.Fprintln(w, "  ingest-git [dir] [-n N]  walk git log, append commits as kind=commit iterations")
}
