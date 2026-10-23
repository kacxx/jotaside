package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strings"
	"time"
	"unicode"

	"github.com/kacxx/aside-jot/internal/store"
)

// Runner runs an external command. Promote runs git and gh through it, so
// tests can fake both.
type Runner interface {
	// Run runs name with args, feeding it stdin (nil for none), and returns
	// its stdout. A failed command's error includes its trimmed stderr.
	Run(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error)
}

// ExecRunner runs real commands.
type ExecRunner struct{}

// Run implements Runner.
func (ExecRunner) Run(ctx context.Context, stdin io.Reader, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return out.Bytes(), fmt.Errorf("%w: %s", err, msg)
		}
		return out.Bytes(), err
	}
	return out.Bytes(), nil
}

// PromoteRequest asks for a jot to become a GitHub issue.
type PromoteRequest struct {
	ID     int64
	Repo   string // [HOST/]OWNER/NAME; empty means the jot's origin remote
	DryRun bool
	// WithReply adds the agent's last reply before the jot to the body.
	WithReply bool
	// Confirm, if set, is shown the issue just before it is created; the
	// issue is created only if it returns nil. Return ErrNotConfirmed to
	// decline; any other error is returned as is. It is not called on a dry
	// run, and runs after the jot, repo, gh login and the repo's visibility
	// have been checked, so it can show the Promotion's Warning.
	Confirm func(Promotion) error
	// Public lets the issue be created without Confirm in a repo that is
	// public, or whose visibility could not be checked. Without Confirm and
	// without Public, Promote refuses such a repo. With Confirm it changes
	// nothing: whoever confirms is shown the Warning.
	Public bool
}

// ErrNotConfirmed is returned when Confirm declines the issue.
var ErrNotConfirmed = errors.New("issue not created")

// Promotion describes the issue a jot became, or would become on a dry run.
type Promotion struct {
	Repo  string
	Title string
	Body  string
	URL   string // empty on a dry run
	// Visibility is the repo's visibility as gh reports it: PUBLIC, PRIVATE
	// or INTERNAL, or "" if it could not be checked.
	Visibility string
	// Warning, if not empty, says the issue may be visible to anyone: the
	// repo is public, or its visibility could not be checked.
	Warning string

	visibilityError string // why the visibility is unknown
}

// AlreadyPromotedError is returned for a jot that already has an issue.
type AlreadyPromotedError struct {
	ID  int64
	URL string
}

func (e *AlreadyPromotedError) Error() string {
	return fmt.Sprintf("jot #%d was already promoted to %s", e.ID, e.URL)
}

// Promote creates a GitHub issue from a jot with the gh CLI, then records the
// issue URL in the jot's metadata and marks it done, in one transaction.
// Before creating it, Promote asks gh whether the repo is public; without
// Confirm, a public repo, or one it couldn't check, is refused unless
// req.Public is set. A dry run only builds the issue and checks the repo's
// visibility: it creates nothing and changes nothing.
func (s *Service) Promote(ctx context.Context, r Runner, req PromoteRequest) (Promotion, error) {
	e, err := s.store.Get(ctx, req.ID)
	if err != nil {
		return Promotion{}, err
	}
	if e.IssueURL != "" {
		return Promotion{}, &AlreadyPromotedError{ID: e.ID, URL: e.IssueURL}
	}
	reply := ""
	if req.WithReply {
		if reply, err = agentReply(e); err != nil {
			return Promotion{}, err
		}
	}
	p := Promotion{Title: IssueTitle(e), Body: issueBody(e, reply)}

	if req.Repo != "" {
		if !validRepo(req.Repo) {
			return Promotion{}, fmt.Errorf("invalid --repo %q (want owner/name)", req.Repo)
		}
		p.Repo = req.Repo
	} else {
		if e.RepoRoot == "" {
			return Promotion{}, fmt.Errorf("jot #%d was not captured in a git repository; pass --repo owner/name", e.ID)
		}
		// Reading the remote is local, so a dry run does it too and shows the
		// real target.
		out, err := r.Run(ctx, nil, "git", "-C", e.RepoRoot, "remote", "get-url", "origin")
		if err != nil {
			return Promotion{}, fmt.Errorf("jot #%d: no origin remote in %s (%w); pass --repo owner/name",
				e.ID, e.RepoRoot, commandError("git", err))
		}
		if p.Repo, err = ParseRemote(strings.TrimSpace(string(out))); err != nil {
			return Promotion{}, fmt.Errorf("jot #%d: %w; pass --repo owner/name", e.ID, err)
		}
	}
	if req.DryRun {
		// Looking the visibility up only reads, so a dry run warns too.
		p.setVisibility(repoVisibility(ctx, r, p.Repo))
		return p, nil
	}

	host := "github.com"
	if parts := strings.Split(p.Repo, "/"); len(parts) == 3 {
		host = parts[0]
	}
	if _, err := r.Run(ctx, nil, "gh", "auth", "status", "--hostname", host); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return Promotion{}, errGHMissing
		}
		return Promotion{}, fmt.Errorf("gh is not logged in to %s; run 'gh auth login' (%w)", host, err)
	}
	p.setVisibility(repoVisibility(ctx, r, p.Repo))
	if req.Confirm == nil && !req.Public && p.Warning != "" {
		// No one will see the warning, so fail safe: --yes alone is not
		// enough to publish a private note where anyone can read it.
		if p.Visibility == "" {
			return Promotion{}, fmt.Errorf("%s; pass --public with --yes to create the issue anyway, or leave out --yes to review it first", p.visibilityError)
		}
		return Promotion{}, fmt.Errorf("%s is PUBLIC, so the issue, including the jot and any reply, would be visible to anyone; pass --public with --yes to create it anyway, or leave out --yes to review it first", p.Repo)
	}
	if req.Confirm != nil {
		if err := req.Confirm(p); err != nil {
			return Promotion{}, err
		}
	}
	out, err := r.Run(ctx, strings.NewReader(p.Body), "gh", "issue", "create",
		"--repo="+p.Repo, "--title="+p.Title, "--body-file=-")
	if err != nil {
		return Promotion{}, fmt.Errorf("gh issue create: %w", commandError("gh", err))
	}
	p.URL = issueURL(out)
	if p.URL == "" {
		return Promotion{}, fmt.Errorf("gh issue create printed no issue URL: %q", strings.TrimSpace(string(out)))
	}
	if err := s.store.SetMetadata(ctx, e.ID, store.MetaIssueURL, p.URL, store.StatusDone); err != nil {
		return p, fmt.Errorf("created %s but could not record it on jot #%d: %w", p.URL, e.ID, err)
	}
	return p, nil
}

// repoVisibility asks gh for repo's visibility: PUBLIC, PRIVATE or INTERNAL.
func repoVisibility(ctx context.Context, r Runner, repo string) (string, error) {
	out, err := r.Run(ctx, nil, "gh", "repo", "view", repo, "--json", "visibility")
	if err != nil {
		return "", commandError("gh", err)
	}
	var v struct {
		Visibility string `json:"visibility"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return "", errors.New("gh repo view printed no visibility")
	}
	switch vis := strings.ToUpper(v.Visibility); vis {
	case "PUBLIC", "PRIVATE", "INTERNAL":
		return vis, nil
	}
	return "", fmt.Errorf("gh repo view printed visibility %q", v.Visibility)
}

// setVisibility records the result of repoVisibility on p, with the warning
// to show for a repo that is public or whose visibility is unknown.
func (p *Promotion) setVisibility(vis string, err error) {
	p.Visibility, p.Warning, p.visibilityError = vis, "", ""
	switch {
	case err != nil:
		// The reason comes from gh's stderr; keep it to one printable line.
		p.visibilityError = fmt.Sprintf("couldn't check whether %s is public (%s)", p.Repo, cleanTitle(err.Error()))
		p.Warning = "⚠ " + p.visibilityError + ": treat this issue, including the jot and any reply, as visible to anyone."
	case vis == "PUBLIC":
		p.Warning = "⚠ " + p.Repo + " is PUBLIC: this issue, including the jot and any reply, will be visible to anyone."
	}
}

var errGHMissing = errors.New("gh (GitHub CLI) not found on PATH; install it from https://cli.github.com and run 'gh auth login'")

func commandError(name string, err error) error {
	if errors.Is(err, exec.ErrNotFound) {
		if name == "gh" {
			return errGHMissing
		}
		return fmt.Errorf("%s not found on PATH", name)
	}
	return err
}

// issueURL returns the last line of gh's output if it is a URL.
func issueURL(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if strings.HasPrefix(last, "https://") || strings.HasPrefix(last, "http://") {
		return last
	}
	return ""
}

// IssueTitle is the jot's first line, trimmed. A jot's text comes from a hook
// payload or stdin and can hold terminal escapes or control characters, so,
// like the body, it is cleaned: the preview shows exactly what is posted, and
// neither can rewrite the terminal it is shown on.
func IssueTitle(e Entry) string {
	line, _, _ := strings.Cut(strings.TrimSpace(cleanText(e.Text)), "\n")
	return strings.TrimSpace(line)
}

// IssueBody is the jot's full text, then where and when it was captured,
// leaving out whatever is unknown.
func IssueBody(e Entry) string { return issueBody(e, "") }

// issueBody is IssueBody with, if reply is not empty, the agent's reply under
// its own heading between the jot and the footer. The whole body is cleaned
// as IssueTitle is.
func issueBody(e Entry, reply string) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(e.Text))
	if reply != "" {
		b.WriteString("\n\n## Agent reply\n\n")
		b.WriteString(reply)
	}
	b.WriteString("\n\nCaptured ")
	b.WriteString(e.CreatedAt.Local().Format(time.RFC3339))
	if e.Source != "" {
		b.WriteString(" from " + e.Source)
	}
	if e.RepoName != "" {
		b.WriteString(" on " + e.RepoName)
		if e.Branch != "" {
			b.WriteString("@" + e.Branch)
		}
		if e.CommitSHA != "" {
			b.WriteString(" (" + e.CommitSHA + ")")
		}
	}
	b.WriteString("\n")
	return cleanText(b.String())
}

// ParseRemote turns a git remote URL into the repo form gh's --repo takes:
// OWNER/NAME for github.com, HOST/OWNER/NAME for any other host. It accepts
// https, ssh:// and scp-like (git@host:owner/name.git) URLs.
func ParseRemote(remote string) (string, error) {
	if remote == "" {
		return "", errors.New("origin remote is empty")
	}
	var host, path string
	if u, err := url.Parse(remote); err == nil && u.Scheme != "" && u.Host != "" {
		host, path = u.Hostname(), u.Path
	} else if at, rest, ok := strings.Cut(remote, ":"); ok && !strings.Contains(at, "/") {
		// scp-like: [user@]host:path
		_, host, _ = strings.Cut(at, "@")
		if host == "" {
			host = at
		}
		path = rest
	} else {
		return "", remoteError(remote)
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	repo := path
	if !strings.EqualFold(host, "github.com") {
		repo = host + "/" + path
	}
	if host == "" || strings.Count(path, "/") != 1 || !validRepo(repo) {
		return "", remoteError(remote)
	}
	return repo, nil
}

// remoteError reports an origin remote ParseRemote cannot use. The remote is
// quoted only in a form that cannot carry credentials: a URL whose userinfo
// url.Parse has stripped, or an scp-like user@host:path. Anything else, such
// as a URL that fails to parse or still contains "@", is not shown, because
// re-deriving its structure by hand is how a token leaks.
func remoteError(remote string) error {
	if shown, ok := safeRemote(remote); ok {
		return fmt.Errorf("cannot read a GitHub repository from remote %q", shown)
	}
	return errors.New("cannot read a GitHub repository from the origin remote (not shown: it may contain credentials)")
}

func safeRemote(remote string) (string, bool) {
	if strings.Contains(remote, "://") {
		u, err := url.Parse(remote)
		if err != nil {
			return "", false
		}
		u.User = nil
		remote = u.String()
		return remote, !strings.Contains(remote, "@")
	}
	// scp-like: an "@" must end a user that has no ":" (so no password).
	at := strings.Index(remote, "@")
	return remote, at < 0 || at < strings.Index(remote, ":")
}

// validRepo reports whether repo is OWNER/NAME or HOST/OWNER/NAME.
func validRepo(repo string) bool {
	parts := strings.Split(repo, "/")
	if len(parts) != 2 && len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		// A leading "-" would read as a flag to gh or git.
		if p == "" || strings.HasPrefix(p, "-") || strings.ContainsAny(p, " \\@") || strings.IndexFunc(p, unicode.IsControl) >= 0 {
			return false
		}
	}
	return true
}
