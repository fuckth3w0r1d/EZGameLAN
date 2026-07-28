package openvpn_test

import (
	"strings"
	"testing"
	"time"

	"github.com/r3t2/ezgamelan/internal/openvpn"
)

func TestParseStatusVersion3(t *testing.T) {
	t.Parallel()
	input := strings.Join([]string{
		"TITLE\tOpenVPN 2.6.21 x86_64",
		"TIME\t2026-07-28 12:00:00\t1785230400",
		"HEADER\tCLIENT_LIST\tCommon Name\tReal Address\tVirtual Address\tVirtual IPv6 Address\tBytes Received\tBytes Sent\tConnected Since\tConnected Since (time_t)\tUsername\tClient ID\tPeer ID\tData Channel Cipher",
		"CLIENT_LIST\talice\t198.51.100.2:54321\t10.86.0.10\t\t1234\t5678\t2026-07-28 11:00:00\t1785226800\tUNDEF\t7\t0\tAES-256-GCM",
		"END",
	}, "\n")
	sessions, err := openvpn.ParseStatusVersion3(strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions = %d, want 1", len(sessions))
	}
	got := sessions[0]
	if got.Name != "alice" || got.VirtualAddress != "10.86.0.10" ||
		got.RealAddress != "198.51.100.2:54321" ||
		got.BytesReceived != 1234 || got.BytesSent != 5678 || got.ClientID != 7 {
		t.Fatalf("session = %#v", got)
	}
	if want := time.Unix(1785226800, 0).UTC(); !got.ConnectedSince.Equal(want) {
		t.Fatalf("connected since = %s, want %s", got.ConnectedSince, want)
	}
}

func TestParseStatusVersion3RejectsTruncatedClient(t *testing.T) {
	t.Parallel()
	_, err := openvpn.ParseStatusVersion3(strings.NewReader(
		"CLIENT_LIST\talice\t198.51.100.2:1\n",
	))
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestParseStatusVersion3IgnoresNonClientRecords(t *testing.T) {
	t.Parallel()
	sessions, err := openvpn.ParseStatusVersion3(strings.NewReader(
		"TITLE\tOpenVPN\nROUTING_TABLE\t10.86.0.10\talice\nEND\n",
	))
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Fatalf("sessions = %d", len(sessions))
	}
}
