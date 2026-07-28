package join

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/r3t2/ezgamelan/internal/invite"
)

func TestFetchProfile(t *testing.T) {
	t.Parallel()
	now := time.Now()
	profile := testProfile(t, now, "alice")
	data, err := invite.Marshal(profile, now)
	if err != nil {
		t.Fatal(err)
	}
	token, err := invite.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/ezgamelan/v1/enroll" {
			t.Errorf("path = %q", r.URL.Path)
		}
		return response(http.StatusOK, profileMediaType, data), nil
	})}

	got, err := Fetch(context.Background(), client, Invitation{
		URL:   "https://vpn.example.test/ezgamelan/v1/enroll",
		Token: token,
	})
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got.ClientName != profile.ClientName {
		t.Fatalf("client = %q", got.ClientName)
	}
}

func TestFetchDoesNotForwardTokenOnRedirect(t *testing.T) {
	t.Parallel()
	token, err := invite.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	var redirected atomic.Bool
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "redirected.example.test" {
			redirected.Store(true)
			return response(http.StatusOK, profileMediaType, nil), nil
		}
		resp := response(http.StatusTemporaryRedirect, "text/plain", nil)
		resp.Header.Set("Location", "https://redirected.example.test/ezgamelan/v1/enroll")
		return resp, nil
	})}

	_, err = Fetch(context.Background(), client, Invitation{
		URL:   "https://vpn.example.test/ezgamelan/v1/enroll",
		Token: token,
	})
	if err == nil {
		t.Fatal("Fetch followed a redirect")
	}
	if redirected.Load() {
		t.Fatal("redirect target received the request")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatal("error exposed the invitation token")
	}
}

func TestLoadFile(t *testing.T) {
	t.Parallel()
	now := time.Now()
	profile := testProfile(t, now, "offline")
	data, err := invite.Marshal(profile, now)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "offline.ezlan")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if got.ClientName != "offline" {
		t.Fatalf("client = %q", got.ClientName)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func response(status int, contentType string, body []byte) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header: http.Header{
			"Content-Type": []string{contentType},
		},
		Body: io.NopCloser(bytes.NewReader(body)),
	}
}
