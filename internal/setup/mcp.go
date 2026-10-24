package setup

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MCPConfigPath is where the agent keeps user-level MCP servers:
// ~/.claude.json (under $CLAUDE_CONFIG_DIR if set) or ~/.codex/config.toml
// (under $CODEX_HOME).
func MCPConfigPath(agent string) (string, error) {
	var env, dir, file string
	switch agent {
	case "claude":
		env, dir, file = "CLAUDE_CONFIG_DIR", "", ".claude.json"
	case "codex":
		env, dir, file = "CODEX_HOME", ".codex", "config.toml"
	default:
		return "", fmt.Errorf("unknown agent %q", agent)
	}
	if d := os.Getenv(env); d != "" {
		return filepath.Join(d, file), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, dir, file), nil
}

// OldMCPServer is an MCP server registered under a previous name of the
// command, still running an old binary next to (or instead of) jotaside's.
type OldMCPServer struct {
	Name    string // the server's name in the config, "aside" or "jot"
	Command string // the program it runs
}

// FindOldMCPServers reads the agent's MCP config at path and returns the
// user-level servers registered under a previous name ("aside", "jot") that
// run this tool under any of its names. A server with one of those names that
// runs some other program isn't ours and isn't returned. It only reads: a
// missing or unreadable file returns nothing.
func FindOldMCPServers(agent, path string) []OldMCPServer {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var servers map[string]string // name -> command
	switch agent {
	case "claude":
		servers = claudeMCPServers(data)
	case "codex":
		servers = codexMCPServers(data)
	}
	var out []OldMCPServer
	for _, name := range previousNames {
		cmd, ok := servers[name]
		if ok && isOurProgram(cmd, "") {
			out = append(out, OldMCPServer{Name: name, Command: cmd})
		}
	}
	return out
}

// claudeMCPServers returns the user-scope servers in ~/.claude.json, the
// top-level "mcpServers" object.
func claudeMCPServers(data []byte) map[string]string {
	var cfg struct {
		MCPServers map[string]struct {
			Command string `json:"command"`
		} `json:"mcpServers"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return nil
	}
	out := map[string]string{}
	for name, s := range cfg.MCPServers {
		out[name] = s.Command
	}
	return out
}

// codexMCPServers returns the [mcp_servers.<name>] tables in Codex's
// config.toml with their command. It reads only what it needs (table headers
// and a top-level `command = "…"` line in each), not TOML in general.
func codexMCPServers(data []byte) map[string]string {
	out := map[string]string{}
	cur := ""
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") {
			cur = ""
			header := strings.TrimSpace(strings.Trim(line, "[]"))
			rest, ok := strings.CutPrefix(header, "mcp_servers.")
			if !ok || strings.HasPrefix(line, "[[") {
				continue
			}
			name := strings.TrimSpace(rest)
			if u, err := strconv.Unquote(name); err == nil {
				name = u
			} else if len(name) >= 2 && name[0] == '\'' && name[len(name)-1] == '\'' {
				name = name[1 : len(name)-1]
			}
			if strings.ContainsAny(name, ".\"'") {
				continue // a sub-table such as [mcp_servers.aside.env]
			}
			cur = name
			if _, ok := out[name]; !ok {
				out[name] = ""
			}
			continue
		}
		if cur == "" {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "command" {
			continue
		}
		val = strings.TrimSpace(val)
		if q, err := strconv.QuotedPrefix(val); err == nil && strings.HasPrefix(val, `"`) {
			out[cur], _ = strconv.Unquote(q)
		} else if lit, ok := strings.CutPrefix(val, "'"); ok {
			// A literal string: no escapes, and it can't contain a quote.
			if end := strings.IndexByte(lit, '\''); end >= 0 {
				out[cur] = lit[:end]
			}
		}
	}
	return out
}
