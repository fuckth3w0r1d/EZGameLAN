package netcfg

import (
	"context"
	"testing"
)

func TestRenameAdapterValidatesNamesBeforePlatformCall(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"", "bad\nname", string(make([]byte, 129))} {
		if err := RenameAdapter(context.Background(), name, AdapterName); err == nil {
			t.Errorf("RenameAdapter accepted current name %q", name)
		}
		if err := RenameAdapter(context.Background(), AdapterName, name); err == nil {
			t.Errorf("RenameAdapter accepted new name %q", name)
		}
	}
}

func TestRenameAdapterIsIdempotent(t *testing.T) {
	t.Parallel()
	if err := RenameAdapter(
		context.Background(),
		AdapterName,
		AdapterName,
	); err != nil {
		t.Fatal(err)
	}
}
