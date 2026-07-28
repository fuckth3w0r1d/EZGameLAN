package join

import (
	"net/url"
	"strings"
	"testing"

	"github.com/r3t2/ezgamelan/internal/invite"
)

func TestParseLink(t *testing.T) {
	t.Parallel()
	token, err := invite.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	enrollURL := "https://vpn.example.test/ezgamelan/v1/enroll"
	raw := "ezgamelan://join?url=" + url.QueryEscape(enrollURL) + "#" + token
	got, err := ParseLink(raw)
	if err != nil {
		t.Fatalf("ParseLink: %v", err)
	}
	if got.URL != enrollURL || got.Token != token {
		t.Fatalf("ParseLink = %#v", got)
	}
}

func TestNewInvitationAcceptsURLAndTokenFields(t *testing.T) {
	t.Parallel()
	token, err := invite.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	got, err := NewInvitation(
		"  https://vpn.example.test/ezgamelan/v1/enroll  ",
		"  "+token+"  ",
	)
	if err != nil {
		t.Fatalf("NewInvitation: %v", err)
	}
	if got.URL != "https://vpn.example.test/ezgamelan/v1/enroll" || got.Token != token {
		t.Fatalf("invitation = %#v", got)
	}
}

func TestParseLinkRejectsUnexpectedInputWithoutLeakingToken(t *testing.T) {
	t.Parallel()
	token, err := invite.NewToken()
	if err != nil {
		t.Fatal(err)
	}
	validURL := url.QueryEscape("https://vpn.example.test/ezgamelan/v1/enroll")
	cases := []string{
		"ezgamelan://other?url=" + validURL + "#" + token,
		"ezgamelan://join/path?url=" + validURL + "#" + token,
		"ezgamelan://join?url=" + validURL + "&extra=1#" + token,
		"ezgamelan://join?url=" + validURL + "&url=" + validURL + "#" + token,
		"ezgamelan://join?url=" + url.QueryEscape("http://vpn.example.test/ezgamelan/v1/enroll") + "#" + token,
		"ezgamelan://join?url=" + url.QueryEscape("https://vpn.example.test/v1/enroll") + "#" + token,
		"ezgamelan://join?url=" + validURL,
	}
	for _, raw := range cases {
		if _, err := ParseLink(raw); err == nil {
			t.Errorf("ParseLink accepted %q", raw)
		} else if strings.Contains(err.Error(), token) {
			t.Fatal("error exposed the invitation token")
		}
	}
}
