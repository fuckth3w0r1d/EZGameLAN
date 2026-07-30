package ipc

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"

	"github.com/r3t2/ezgamelan/internal/invite"
)

const testCredentialID = "0123456789abcdef0123456789abcdef"

func TestRequestRoundTrip(t *testing.T) {
	t.Parallel()
	token, err := invite.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	want := Request{
		Version: Version,
		Action:  ActionJoin,
		URL:     "https://vpn.example.test/ezgamelan/v1/enroll",
		Token:   token,
	}
	var encoded shortWriter
	if err := WriteRequest(&encoded, want); err != nil {
		t.Fatalf("WriteRequest: %v", err)
	}
	got, err := ReadRequest(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	if got != want {
		t.Fatalf("request = %#v, want %#v", got, want)
	}
}

func TestReadRequestRejectsDuplicateAndOversize(t *testing.T) {
	t.Parallel()
	token, err := invite.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"version":3,"version":3,"action":"join","url":"https://vpn.example.test/ezgamelan/v1/enroll","token":"` + token + `"}`)
	var frame bytes.Buffer
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(body)))
	frame.Write(header[:])
	frame.Write(body)
	if _, err := ReadRequest(&frame); err == nil {
		t.Fatal("ReadRequest accepted a duplicate field")
	} else if strings.Contains(err.Error(), token) {
		t.Fatal("error exposed the token")
	}

	frame.Reset()
	binary.LittleEndian.PutUint32(header[:], MaxRequestSize+1)
	frame.Write(header[:])
	if _, err := ReadRequest(&frame); err == nil {
		t.Fatal("ReadRequest accepted an oversized frame")
	}
}

func TestRequestRejectsExtraJoinFields(t *testing.T) {
	t.Parallel()
	for _, action := range []Action{
		ActionConnect,
		ActionDisconnect,
		ActionDeleteCredential,
		ActionStatus,
		ActionDiagnostics,
	} {
		request := Request{
			Version:      Version,
			Action:       action,
			URL:          "https://vpn.example.test/ezgamelan/v1/enroll",
			CredentialID: testCredentialID,
		}
		if err := request.Validate(); err == nil {
			t.Errorf("%s accepted URL", action)
		}
	}
}

func TestCredentialRequestRoundTrip(t *testing.T) {
	t.Parallel()
	want := Request{
		Version:      Version,
		Action:       ActionConnect,
		CredentialID: testCredentialID,
	}
	var encoded bytes.Buffer
	if err := WriteRequest(&encoded, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRequest(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("request = %#v, want %#v", got, want)
	}
}

func TestDiagnosticsResponseRoundTrip(t *testing.T) {
	t.Parallel()
	want := Response{
		Version: Version,
		OK:      true,
		Status: Status{
			State:        StateConnected,
			CredentialID: testCredentialID,
			VirtualIP:    "10.86.0.11",
			AdapterName:  "EZGameLAN",
		},
		Credentials: []Credential{{
			ID:         testCredentialID,
			ClientName: "alice",
			Endpoint:   "vpn.example.test:21111",
			ServerName: "ezgamelan-server",
		}},
		Diagnostics: strings.Repeat("diagnostic line\n", 4000),
	}
	var encoded shortWriter
	if err := WriteResponse(&encoded, want); err != nil {
		t.Fatalf("WriteResponse: %v", err)
	}
	got, err := ReadResponse(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatalf("ReadResponse: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("response mismatch: got %d diagnostic bytes", len(got.Diagnostics))
	}
}

func TestResponseRejectsInconsistentCredentialList(t *testing.T) {
	t.Parallel()
	response := Response{
		Version: Version,
		OK:      true,
		Status: Status{
			State:        StateDisconnected,
			CredentialID: testCredentialID,
		},
	}
	if err := response.Validate(); err == nil {
		t.Fatal("Validate accepted an active credential missing from the list")
	}
	credential := Credential{
		ID:         testCredentialID,
		ClientName: "alice",
		Endpoint:   "vpn.example.test:21111",
		ServerName: "ezgamelan-server",
	}
	response.Credentials = []Credential{credential, credential}
	if err := response.Validate(); err == nil {
		t.Fatal("Validate accepted duplicate credential IDs")
	}
}

func TestDiagnosticsResponseRejectsOversizeOrErrorPayload(t *testing.T) {
	t.Parallel()
	response := Response{
		Version:     Version,
		OK:          true,
		Status:      Status{State: StateDisconnected},
		Diagnostics: strings.Repeat("x", MaxDiagnostics+1),
	}
	if err := response.Validate(); err == nil {
		t.Fatal("Validate accepted oversized diagnostics")
	}
	response = Response{
		Version:     Version,
		OK:          false,
		Code:        "E_INTERNAL_LOG",
		Message:     "log failed",
		Status:      Status{State: StateError},
		Diagnostics: "unexpected",
	}
	if err := response.Validate(); err == nil {
		t.Fatal("Validate accepted diagnostics in an error response")
	}
}

type shortWriter struct {
	bytes.Buffer
}

func (w *shortWriter) Write(data []byte) (int, error) {
	if len(data) > 3 {
		data = data[:3]
	}
	return w.Buffer.Write(data)
}
