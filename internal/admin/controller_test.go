package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/r3t2/ezgamelan/internal/config"
	"github.com/r3t2/ezgamelan/internal/ezlan"
	"github.com/r3t2/ezgamelan/internal/openvpn"
	"github.com/r3t2/ezgamelan/internal/store"
)

type fakeManager struct {
	crlWarning string
	cfg        config.Config
	clients    []store.Client
	invite     ezlan.InviteResult
	inviteErr  error
	revoked    string
	revokeErr  error
	inviteName string
}

func (f *fakeManager) Config() config.Config { return f.cfg }
func (f *fakeManager) CRLWarning() string    { return f.crlWarning }

func (f *fakeManager) List() ([]store.Client, error) {
	return append([]store.Client(nil), f.clients...), nil
}

func (f *fakeManager) Invite(
	_ context.Context,
	name string,
) (ezlan.InviteResult, error) {
	f.inviteName = name
	return f.invite, f.inviteErr
}

func (f *fakeManager) Revoke(name string) error {
	f.revoked = name
	return f.revokeErr
}

type fakeHost struct {
	sessions     []openvpn.Session
	sessionsErr  error
	disconnected []uint64
	restarted    string
}

func (f *fakeHost) Sessions(context.Context) ([]openvpn.Session, error) {
	return append([]openvpn.Session(nil), f.sessions...), f.sessionsErr
}

func (f *fakeHost) Disconnect(_ context.Context, id uint64) error {
	f.disconnected = append(f.disconnected, id)
	return nil
}

func (f *fakeHost) ServiceState(_ context.Context, unit string) (string, error) {
	if unit != OpenVPNUnit && unit != EnrollUnit {
		return "", errors.New("unexpected unit")
	}
	return "active", nil
}

func (f *fakeHost) RestartService(_ context.Context, unit string) error {
	f.restarted = unit
	return nil
}

func (*fakeHost) Diagnostics(context.Context) (string, error) {
	return "bounded diagnostics", nil
}

func TestSnapshotCombinesRecordsAndLiveSessions(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	manager := &fakeManager{
		cfg: config.Default("vpn.example.test"),
		clients: []store.Client{{
			Name: "alice", Status: store.StatusActive,
			CreatedAt: now.Add(-time.Hour), NotAfter: now.AddDate(1, 0, 0),
		}},
	}
	host := &fakeHost{sessions: []openvpn.Session{{
		Name: "alice", VirtualAddress: "10.86.0.10",
		RealAddress: "198.51.100.2:1234", ClientID: 9,
		ConnectedSince: now.Add(-time.Minute),
	}}}
	controller := NewController(manager, host)
	controller.now = func() time.Time { return now }
	response := controller.Execute(context.Background(), Request{
		ProtocolVersion: ProtocolVersion,
		Action:          ActionSnapshot,
	})
	if !response.OK || response.Snapshot == nil {
		t.Fatalf("response = %#v", response)
	}
	if len(response.Snapshot.Devices) != 1 ||
		!response.Snapshot.Devices[0].Online ||
		response.Snapshot.Devices[0].VirtualAddress != "10.86.0.10" {
		t.Fatalf("devices = %#v", response.Snapshot.Devices)
	}
	if response.Snapshot.OpenVPNState != "active" ||
		response.Snapshot.EnrollState != "active" {
		t.Fatalf("service states = %q/%q",
			response.Snapshot.OpenVPNState,
			response.Snapshot.EnrollState,
		)
	}
}

func TestRevokeAlsoDisconnectsOnlineDevice(t *testing.T) {
	t.Parallel()
	manager := &fakeManager{cfg: config.Default("vpn.example.test")}
	host := &fakeHost{sessions: []openvpn.Session{
		{Name: "alice", ClientID: 4},
		{Name: "bob", ClientID: 8},
	}}
	response := NewController(manager, host).Execute(context.Background(), Request{
		ProtocolVersion: ProtocolVersion,
		Action:          ActionRevoke,
		Name:            "alice",
	})
	if !response.OK || manager.revoked != "alice" {
		t.Fatalf("response = %#v, revoked = %q", response, manager.revoked)
	}
	if len(host.disconnected) != 1 || host.disconnected[0] != 4 {
		t.Fatalf("disconnected = %#v", host.disconnected)
	}
}

func TestServeStreamRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	manager := &fakeManager{cfg: config.Default("vpn.example.test")}
	host := &fakeHost{}
	input := strings.NewReader(
		`{"protocol_version":1,"action":"snapshot","unexpected":true}` + "\n",
	)
	var output bytes.Buffer
	if err := ServeStream(
		context.Background(),
		input,
		&output,
		NewController(manager, host),
	); err != nil {
		t.Fatal(err)
	}
	var response Response
	if err := json.Unmarshal(output.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Error == nil || response.Error.Code != "E_CONFIG_REQUEST" {
		t.Fatalf("response = %#v", response)
	}
}

func TestInviteDoesNotExposeInternalError(t *testing.T) {
	t.Parallel()
	manager := &fakeManager{
		cfg:       config.Default("vpn.example.test"),
		inviteErr: errors.New("private filesystem detail"),
	}
	response := NewController(manager, &fakeHost{}).Execute(
		context.Background(),
		Request{
			ProtocolVersion: ProtocolVersion,
			Action:          ActionInvite,
			Name:            "alice",
		},
	)
	data, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("private filesystem detail")) {
		t.Fatalf("response leaked internal error: %s", data)
	}
}
func TestSnapshotIncludesCRLWarnings(t *testing.T) {
	t.Parallel()
	for _, warning := range []string{"CRL 文件缺失", "CRL 即将过期", "吊销记录尚未发布"} {
		for _, sessionsFail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/sessionsFail=%t", warning, sessionsFail), func(t *testing.T) {
				manager := &fakeManager{cfg: config.Default("vpn.example.test"), crlWarning: warning}
				host := &fakeHost{}
				if sessionsFail {
					host.sessionsErr = errors.New("management unavailable")
				}
				response := NewController(manager, host).Execute(context.Background(), Request{
					ProtocolVersion: ProtocolVersion, Action: ActionSnapshot,
				})
				if !response.OK || response.Snapshot == nil || !strings.Contains(response.Snapshot.Warning, warning) {
					t.Fatalf("missing CRL warning: %#v", response)
				}
				if sessionsFail && !strings.Contains(response.Snapshot.Warning, "在线状态暂时读取失败") {
					t.Fatal("CRL warning replaced the session warning")
				}
			})
		}
	}
}

func TestServeStreamExecutesOnlyOneCompleteRequest(t *testing.T) {
	t.Parallel()
	const request = `{"protocol_version":1,"action":"revoke","name":"alice"}`
	for _, scenario := range []struct {
		name, input string
		valid       bool
	}{
		{"one request", request + "\n", true},
		{"second request", request + "\n" + request + "\n", false},
		{"trailing data", request + "invalid", false},
		{"oversized", request + strings.Repeat(" ", MaxRequestSize), false},
		{"empty", "", false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			manager := &fakeManager{cfg: config.Default("vpn.example.test")}
			var output bytes.Buffer
			err := ServeStream(context.Background(), strings.NewReader(scenario.input), &output, NewController(manager, &fakeHost{}))
			if err != nil {
				t.Fatal(err)
			}
			var response Response
			if err := json.Unmarshal(output.Bytes(), &response); err != nil {
				t.Fatalf("expected exactly one JSON response: %v", err)
			}
			if response.OK != scenario.valid {
				t.Fatalf("response = %#v", response)
			}
			if scenario.valid && manager.revoked != "alice" {
				t.Fatal("request was not executed")
			}
			if !scenario.valid && manager.revoked != "" {
				t.Fatal("incomplete request was executed")
			}
		})
	}
}
