package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"path/filepath"

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
	projectsFlag := fs.String("projects", "", "comma-separated multi-project list: name=dir,name2=dir2 (overrides config)")
	configFlag := fs.String("config", serve.DefaultProjectsConfigPath(), "path to projects config (YAML list of {name, path})")
	fs.Parse(args)

	addr := fmt.Sprintf("localhost:%d", *port)

	// Resolution order: -projects flag → config file → single-project (cwd or arg).
	if projects, err := serve.ParseProjectsFlag(*projectsFlag); err != nil {
		fmt.Fprintln(os.Stderr, "vibelog serve: -projects:", err)
		os.Exit(1)
	} else if len(projects) > 0 {
		runMulti(projects, *configFlag, addr, "flag")
		return
	}

	if projects, err := serve.LoadProjectsConfig(*configFlag); err != nil {
		fmt.Fprintln(os.Stderr, "vibelog serve: config:", err)
		os.Exit(1)
	} else if len(projects) > 0 {
		runMulti(projects, *configFlag, addr, "config")
		return
	}

	// Auto-discover: walk cwd (or the positional arg if given) up to 3 levels
	// deep, treating any subdir with .sync/anchor.yaml as a project. If we find
	// ≥2, multi-project mode. If exactly 1, single-project mode using that dir.
	// If 0, fall back to single-project on the search root itself (which will
	// silent-skip in the dashboard if .sync/ doesn't exist there either).
	var root string
	if fs.NArg() > 0 {
		root = fs.Arg(0)
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "cwd:", err)
			os.Exit(1)
		}
		root = cwd
	}
	discovered, err := serve.DiscoverProjects(root, 3)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vibelog serve: discover:", err)
		os.Exit(1)
	}
	switch {
	case len(discovered) >= 2:
		runMulti(discovered, root, addr, "discover")
		return
	case len(discovered) == 1:
		dir := discovered[0].Path
		fmt.Printf("vibelog serving %s (discovered) on http://%s\n", dir, addr)
		if err := serve.Run(dir, addr); err != nil {
			fmt.Fprintln(os.Stderr, "vibelog serve:", err)
			os.Exit(1)
		}
		return
	}

	// Nothing discovered — serve the root as a single project (existing behavior).
	fmt.Printf("vibelog serving %s on http://%s\n", root, addr)
	if err := serve.Run(root, addr); err != nil {
		fmt.Fprintln(os.Stderr, "vibelog serve:", err)
		os.Exit(1)
	}
}

func runMulti(projects []serve.Project, src, addr, srcKind string) {
	switch srcKind {
	case "config":
		fmt.Printf("vibelog serving %d projects from %s on http://%s\n", len(projects), src, addr)
	case "discover":
		fmt.Printf("vibelog serving %d projects discovered under %s on http://%s\n", len(projects), src, addr)
	default:
		fmt.Printf("vibelog serving %d projects on http://%s\n", len(projects), addr)
	}
	for _, p := range projects {
		fmt.Printf("  /p/%s/  →  %s\n", p.Name, p.Path)
	}
	if err := serve.RunMulti(projects, addr); err != nil {
		fmt.Fprintln(os.Stderr, "vibelog serve:", err)
		os.Exit(1)
	}
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
	fmt.Fprintln(w, "  serve [dir] [-port N]  host the dashboard UI on http://localhost:7100 (default port)")
	fmt.Fprintln(w, "  ingest-git [dir] [-n N]  walk git log, append commits as kind=commit iterations")
}
