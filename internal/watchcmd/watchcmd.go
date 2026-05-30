// Package watchcmd implements `vibelog watch` — a terminal observer that
// tails iterations.jsonl and pretty-prints each entry as it lands. Pre-UI
// observability for the dogfood loop.
package watchcmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"vibelog/internal/model"
)

// Run tails projectDir/.sync/iterations.jsonl forever (or until stdin closes
// the process). Prints history on startup, then waits for new lines via
// polling (200ms tick).
func Run(projectDir string) error {
	path := filepath.Join(projectDir, ".sync", "iterations.jsonl")
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	fmt.Fprintf(os.Stderr, "—— watching %s ——\n", path)

	if err := drain(reader); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "—— waiting for new iterations (Ctrl-C to exit) ——")
	return tail(reader)
}

func drain(r *bufio.Reader) error {
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			fmt.Println(Format(strings.TrimRight(line, "\n")))
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
	}
}

func tail(r *bufio.Reader) error {
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			fmt.Println(Format(strings.TrimRight(line, "\n")))
		}
		if err == io.EOF {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if err != nil {
			return fmt.Errorf("tail: %w", err)
		}
	}
}

// Format pretty-prints one iterations.jsonl line. Exported so tests can verify
// formatting without exercising the file-tail loop. Falls back to the raw
// line if the JSON doesn't parse as an Iteration.
func Format(line string) string {
	var it model.Iteration
	if err := json.Unmarshal([]byte(line), &it); err != nil {
		return line
	}
	var b strings.Builder
	ts := it.Ts.Local().Format("15:04:05")
	label := string(it.Kind)
	if label == "" {
		label = "?"
	}
	fmt.Fprintf(&b, "#%-3d  %s  [%-9s]  %s", it.ID, ts, label, it.Summary)
	if it.Kind == model.KindCommit && it.SHA != "" {
		fmt.Fprintf(&b, "  (%s)", it.SHA)
	}
	if len(it.FilesChanged) > 0 {
		fmt.Fprintf(&b, "\n        files:  %s", strings.Join(it.FilesChanged, ", "))
	}
	if len(it.ClaimsAdded) > 0 || len(it.ClaimsViolated) > 0 {
		var parts []string
		for _, c := range it.ClaimsAdded {
			parts = append(parts, "+"+c)
		}
		for _, c := range it.ClaimsViolated {
			parts = append(parts, "✗"+c)
		}
		fmt.Fprintf(&b, "\n        claims: %s", strings.Join(parts, "  "))
	}
	if it.SupersededAt != nil {
		fmt.Fprintf(&b, "\n        SUPERSEDED (%s) at %s", it.SupersededReason, it.SupersededAt.Local().Format("15:04:05"))
	}
	return b.String()
}
