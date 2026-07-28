package adminweb

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/r3t2/ezgamelan/internal/admin"
)

type fakeRemote struct {
	server  string
	request admin.Request
}

func (f *fakeRemote) Call(
	_ context.Context,
	server string,
	request admin.Request,
) (admin.Response, error) {
	f.server = server
	f.request = request
	return admin.Response{
		ProtocolVersion: admin.ProtocolVersion,
		OK:              true,
		Snapshot: &admin.Snapshot{
			PublicHost:   "vpn.example.test",
			OpenVPNState: "active",
			EnrollState:  "active",
			Devices:      []admin.Device{},
		},
	}, nil
}

func TestLocalServerUsesSecretPathAndChecksOrigin(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	remote := &fakeRemote{}
	server, err := Start(ctx, remote, "s0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
		defer closeCancel()
		_ = server.Close(closeCtx)
	}()

	client := &http.Client{Timeout: 3 * time.Second}
	page, err := client.Get(server.URL())
	if err != nil {
		t.Fatal(err)
	}
	defer page.Body.Close()
	if page.StatusCode != http.StatusOK {
		t.Fatalf("page status = %d", page.StatusCode)
	}
	if csp := page.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Fatalf("CSP = %q", csp)
	}
	configResponse, err := client.Get(server.URL() + "api/config")
	if err != nil {
		t.Fatal(err)
	}
	defer configResponse.Body.Close()
	var config localConfig
	if err := json.NewDecoder(configResponse.Body).Decode(&config); err != nil {
		t.Fatal(err)
	}
	if config.Server != "s0" {
		t.Fatalf("server config = %#v", config)
	}

	payload, err := json.Marshal(localActionRequest{
		Request: admin.Request{
			ProtocolVersion: admin.ProtocolVersion,
			Action:          admin.ActionSnapshot,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	actionURL := server.URL() + "api/action"
	request, err := http.NewRequest(http.MethodPost, actionURL, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	parsed, _ := url.Parse(server.URL())
	request.Header.Set("Origin", parsed.Scheme+"://"+parsed.Host)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("action status = %d: %s", response.StatusCode, data)
	}
	var got admin.Response
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !got.OK || remote.server != "s0" ||
		remote.request.Action != admin.ActionSnapshot {
		t.Fatalf("response=%#v remote=%#v", got, remote)
	}

	overridePayload := []byte(
		`{"server":"other","request":{"protocol_version":1,"action":"snapshot"}}`,
	)
	overrideRequest, err := http.NewRequest(
		http.MethodPost,
		actionURL,
		bytes.NewReader(overridePayload),
	)
	if err != nil {
		t.Fatal(err)
	}
	overrideRequest.Header.Set("Content-Type", "application/json")
	overrideRequest.Header.Set("Origin", parsed.Scheme+"://"+parsed.Host)
	overrideResponse, err := client.Do(overrideRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer overrideResponse.Body.Close()
	if overrideResponse.StatusCode != http.StatusBadRequest {
		t.Fatalf("server override status = %d", overrideResponse.StatusCode)
	}

	badRequest, err := http.NewRequest(
		http.MethodPost,
		actionURL,
		bytes.NewReader(payload),
	)
	if err != nil {
		t.Fatal(err)
	}
	badRequest.Header.Set("Content-Type", "application/json")
	badRequest.Header.Set("Origin", "http://example.test")
	badResponse, err := client.Do(badRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer badResponse.Body.Close()
	if badResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign origin status = %d", badResponse.StatusCode)
	}
}

func TestValidateServerAlias(t *testing.T) {
	t.Parallel()
	for _, valid := range []string{"s0", "home-vpn", "user@host.example"} {
		if err := ValidateServerAlias(valid); err != nil {
			t.Errorf("ValidateServerAlias(%q): %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "-oProxyCommand=x", "host name", "host;cmd", "主机"} {
		if err := ValidateServerAlias(invalid); err == nil {
			t.Errorf("ValidateServerAlias(%q) succeeded", invalid)
		}
	}
}
