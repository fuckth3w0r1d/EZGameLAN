// Package netcfg contains the small set of privileged Windows network changes
// owned by the EZGameLAN service.
package netcfg

import (
	"context"
	"errors"
	"strings"
)

const AdapterName = "EZGameLAN"

func RenameAdapter(ctx context.Context, currentName, newName string) error {
	currentName = strings.TrimSpace(currentName)
	newName = strings.TrimSpace(newName)
	if err := validateAdapterName(currentName); err != nil {
		return err
	}
	if err := validateAdapterName(newName); err != nil {
		return err
	}
	if currentName == newName {
		return nil
	}
	return renameAdapter(ctx, currentName, newName)
}

func validateAdapterName(name string) error {
	if name == "" || len(name) > 128 {
		return errors.New("E_ADAPTER_NAME: 网卡名称为空或过长")
	}
	if strings.ContainsAny(name, "\x00\r\n") {
		return errors.New("E_ADAPTER_NAME: 网卡名称包含控制字符")
	}
	return nil
}
