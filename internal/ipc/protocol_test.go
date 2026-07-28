package ipc

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"

	"github.com/r3t2/ezgamelan/internal/invite"
)

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
	body := []byte(`{"version":1,"version":1,"action":"join","url":"https://vpn.example.test/ezgamelan/v1/enroll","token":"` + token + `"}`)
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
	binary.LittleEndian.PutUint32(header[:], MaxMessageSize+1)
	frame.Write(header[:])
	if _, err := ReadRequest(&frame); err == nil {
		t.Fatal("ReadRequest accepted an oversized frame")
	}
}

func TestRequestRejectsExtraJoinFields(t *testing.T) {
	t.Parallel()
	for _, action := range []Action{ActionConnect, ActionDisconnect, ActionStatus} {
		request := Request{
			Version: Version,
			Action:  action,
			URL:     "https://vpn.example.test/ezgamelan/v1/enroll",
		}
		if err := request.Validate(); err == nil {
			t.Errorf("%s accepted URL", action)
		}
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
