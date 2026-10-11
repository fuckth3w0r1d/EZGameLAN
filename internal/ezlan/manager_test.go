package ezlan

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureExactFileReplacesGeneratedContent(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "server.conf")
	if err := os.WriteFile(path, []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureExactFile(path, []byte("new\n"), 0o600); err != nil {
		t.Fatalf("ensureExactFile: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new\n" {
		t.Fatalf("contents = %q", data)
	}
}

func TestBuildJoinLinkKeepsTokenInFragment(t *testing.T) {
	t.Parallel()
	enrollURL := "https://vpn.example.test/ezgamelan/v1/enroll"
	token := "secret-token"
	raw := buildJoinLink(enrollURL, token)
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "ezgamelan" || parsed.Host != "join" {
		t.Fatalf("link target = %s://%s", parsed.Scheme, parsed.Host)
	}
	if got := parsed.Query().Get("url"); got != enrollURL {
		t.Fatalf("url = %q, want %q", got, enrollURL)
	}
	if parsed.Fragment != token {
		t.Fatalf("fragment = %q", parsed.Fragment)
	}
	if parsed.Query().Get("token") != "" {
		t.Fatal("token appeared in query")
	}
}
