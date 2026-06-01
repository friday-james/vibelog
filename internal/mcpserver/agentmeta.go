package mcpserver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type agentMetadata struct {
	Agent     string
	SessionID string
}

var (
	fallbackSessionID = fmt.Sprintf("mcp-%d-%d", os.Getpid(), time.Now().UTC().UnixNano())
	parentAgentOnce   sync.Once
	parentAgentHint   string
)

func resolveAgentMetadata(agentOverride, sessionOverride string) agentMetadata {
	agent, sessionID := detectAgentMetadata()
	if s := strings.TrimSpace(agentOverride); s != "" {
		agent = s
	}
	if s := strings.TrimSpace(sessionOverride); s != "" {
		sessionID = s
	}
	if agent == "" {
		agent = "mcp-client"
	}
	if sessionID == "" {
		sessionID = fallbackSessionID
	}
	return agentMetadata{Agent: agent, SessionID: sessionID}
}

func detectAgentMetadata() (string, string) {
	if sid := strings.TrimSpace(os.Getenv("CLAUDE_SESSION_ID")); sid != "" {
		return "claude-code", sid
	}
	if sid := firstNonEmptyEnv("CODEX_SESSION_ID", "OPENAI_SESSION_ID", "CHATGPT_SESSION_ID"); sid != "" {
		return "codex", sid
	}
	if hint := detectParentAgentHint(); hint != "" {
		return hint, fallbackSessionID
	}
	if firstNonEmptyEnv("CODEX_HOME", "CODEX_SANDBOX_NETWORK_DISABLED") != "" {
		return "codex", fallbackSessionID
	}
	return "mcp-client", fallbackSessionID
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

func detectParentAgentHint() string {
	parentAgentOnce.Do(func() {
		out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(os.Getppid())).Output()
		if err != nil {
			return
		}
		base := strings.ToLower(filepath.Base(strings.TrimSpace(string(out))))
		switch {
		case strings.Contains(base, "claude"):
			parentAgentHint = "claude-code"
		case strings.Contains(base, "codex"):
			parentAgentHint = "codex"
		}
	})
	return parentAgentHint
}
