package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"path/filepath"

	"cockpit/internal/initcmd"
	"cockpit/internal/mcpserver"
	"cockpit/internal/observecmd"
	"cockpit/internal/serve"
	"cockpit/internal/store"
	"cockpit/internal/watchcmd"
)

const version = "0.1.0-dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Usage = func() { usage(os.Stderr) }
	flag.Parse()

	if *showVersion {
		fmt.Println("cockpit", version)
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
	default:
		fmt.Fprintf(os.Stderr, "cockpit: unknown subcommand %q\n\n", args[0])
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
		fmt.Fprintln(os.Stderr, "cockpit init:", err)
		os.Exit(1)
	}
	syncDir := filepath.Join(dir, ".sync")
	fmt.Println("initialized", syncDir)
	fmt.Println("next: edit", filepath.Join(syncDir, "anchor.yaml"), "to replace the TODOs")
}

func runLoad(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: cockpit load <dir>")
		os.Exit(2)
	}
	state, err := store.Load(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "cockpit load:", err)
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
		fmt.Fprintln(os.Stderr, "cockpit mcp:", err)
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
		fmt.Fprintln(os.Stderr, "cockpit watch:", err)
		os.Exit(1)
	}
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 7100, "port to listen on")
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
	addr := fmt.Sprintf("localhost:%d", *port)
	fmt.Printf("cockpit serving %s on http://%s\n", dir, addr)
	if err := serve.Run(dir, addr); err != nil {
		fmt.Fprintln(os.Stderr, "cockpit serve:", err)
		os.Exit(1)
	}
}

func runObserve(args []string) {
	var dir string
	if len(args) > 0 {
		dir = args[0]
	}
	// If no arg, observecmd will fall back to payload.cwd from stdin.
	if err := observecmd.Run(dir); err != nil {
		fmt.Fprintln(os.Stderr, "cockpit observe:", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: cockpit [--version] <subcommand> [args...]")
	fmt.Fprintln(w, "subcommands:")
	fmt.Fprintln(w, "  init [dir]    create <dir>/.sync/ skeleton (default dir: cwd)")
	fmt.Fprintln(w, "  load <dir>    parse <dir>/.sync/ and print the validated state as JSON")
	fmt.Fprintln(w, "  mcp [dir]     run MCP stdio server, mutating <dir>/.sync/ (default dir: cwd)")
	fmt.Fprintln(w, "  watch [dir]   tail <dir>/.sync/iterations.jsonl, pretty-print new entries")
	fmt.Fprintln(w, "  observe [dir] Stop-hook handler — reads payload from stdin, auto-records iteration")
	fmt.Fprintln(w, "  serve [dir] [-port N]  host the dashboard UI on http://localhost:7100 (default port)")
}
