package enroll

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/r3t2/ezgamelan/internal/invite"
	"github.com/r3t2/ezgamelan/internal/pki"
)

type fakeClaimer struct {
	wantToken string
	profile   invite.Profile
	calls     int
}

func (f *fakeClaimer) Claim(token string, _ time.Time) (invite.Profile, error) {
	f.calls++
	if token != f.wantToken {
		return invite.Profile{}, context.Canceled
	}
	return f.profile, nil
}

func TestEnrollSuccess(t *testing.T) {
	t.Parallel()
	now := time.Now()
	profile := enrollProfile(t, now)
	token, err := invite.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	claimer := &fakeClaimer{wantToken: token, profile: profile}
	var logs bytes.Buffer
	server := New(claimer, slog.New(slog.NewTextHandler(&logs, nil)))
	server.now = func() time.Time { return now }
	body, err := EncodeRequest(token)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/enroll", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, err := invite.Parse(rec.Body.Bytes(), now); err != nil {
		t.Fatalf("response profile: %v", err)
	}
	if strings.Contains(logs.String(), token) {
		t.Fatal("logs contain token")
	}
	if claimer.calls != 1 {
		t.Fatalf("Claim calls = %d", claimer.calls)
	}
}

func TestEnrollRejectsUnknownField(t *testing.T) {
	t.Parallel()
	token, _ := invite.NewToken()
	claimer := &fakeClaimer{wantToken: token}
	server := New(claimer, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req := httptest.NewRequest(
		http.MethodPost,
		"/v1/enroll",
		strings.NewReader(`{"token":"`+token+`","extra":true}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	if claimer.calls != 0 {
		t.Fatalf("Claim calls = %d", claimer.calls)
	}
}

func enrollProfile(t *testing.T, now time.Time) invite.Profile {
	t.Helper()
	root := t.TempDir()
	pkiDir := filepath.Join(root, "pki")
	if err := pki.Init(pkiDir, "ezgamelan-server", now); err != nil {
		t.Fatal(err)
	}
	material, err := pki.IssueClient(pkiDir, "alice", now)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := pki.ReadCACertificate(pkiDir)
	if err != nil {
		t.Fatal(err)
	}
	return invite.Profile{
		FormatVersion: invite.FormatVersion,
		ClientName:    "alice",
		Endpoint:      "127.0.0.1:21111",
		ServerName:    "ezgamelan-server",
		CAPEM:         string(ca),
		CertPEM:       string(material.CertPEM),
		KeyPEM:        string(material.KeyPEM),
		TLSCryptV2PEM: "-----BEGIN OpenVPN tls-crypt-v2 client key-----\nTEST\n-----END OpenVPN tls-crypt-v2 client key-----\n",
	}
}
