//go:build linux

package adminweb

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/r3t2/ezgamelan/internal/admin"
)

func TestSSHRemoteUsesJSONStdinAndFixedRemoteCommand(t *testing.T) {
	root := t.TempDir()
	argsPath := filepath.Join(root, "args.txt")
	inputPath := filepath.Join(root, "input.txt")
	scriptPath := filepath.Join(root, "ssh-fixture")
	script := `#!/bin/sh
printf '%s\n' "$@" >"$EZ_TEST_ARGS"
cat >"$EZ_TEST_INPUT"
printf '%s\n' '{"protocol_version":1,"ok":true,"message":"ok"}'
`
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EZ_TEST_ARGS", argsPath)
	t.Setenv("EZ_TEST_INPUT", inputPath)

	remote, err := NewSSHRemote(scriptPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := remote.Call(ctx, "s0", admin.Request{
		ProtocolVersion: admin.ProtocolVersion,
		Action:          admin.ActionDisconnect,
		Name:            "alice",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.OK || response.Message != "ok" {
		t.Fatalf("response = %#v", response)
	}
	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatal(err)
	}
	wantArgs := "-T\n-o\nBatchMode=yes\n-o\nConnectTimeout=10\ns0\n" +
		"sudo\n-n\n/usr/local/bin/ezlanctl\nadmin-stdio\n"
	if string(args) != wantArgs {
		t.Fatalf("arguments = %q, want %q", args, wantArgs)
	}
	input, err := os.ReadFile(inputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(input) != `{"protocol_version":1,"action":"disconnect","name":"alice"}`+"\n" {
		t.Fatalf("stdin = %q", input)
	}
}
