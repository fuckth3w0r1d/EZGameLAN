//go:build windows

package join

import (
	"context"
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

func validateOwnerSID(ownerSID string) error {
	_, err := windows.StringToSid(ownerSID)
	return err
}

func ProtectPath(ctx context.Context, path string, directory bool, ownerSID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	systemSID, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("创建 SYSTEM SID: %w", err)
	}
	administratorsSID, err := windows.CreateWellKnownSid(
		windows.WinBuiltinAdministratorsSid,
	)
	if err != nil {
		return fmt.Errorf("创建 Administrators SID: %w", err)
	}
	inheritance := uint32(windows.NO_INHERITANCE)
	if directory {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	entries := []windows.EXPLICIT_ACCESS{
		fullAccessEntry(systemSID, windows.TRUSTEE_IS_USER, inheritance),
		fullAccessEntry(administratorsSID, windows.TRUSTEE_IS_GROUP, inheritance),
	}
	if ownerSID != "" {
		owner, err := windows.StringToSid(ownerSID)
		if err != nil {
			return err
		}
		entries = append(entries, fullAccessEntry(owner, windows.TRUSTEE_IS_USER, inheritance))
	}
	acl, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return fmt.Errorf("创建 Windows ACL: %w", err)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	flags := uint32(windows.FILE_FLAG_OPEN_REPARSE_POINT)
	if directory {
		flags |= windows.FILE_FLAG_BACKUP_SEMANTICS
	}
	access := uint32(windows.WRITE_DAC | windows.READ_CONTROL | windows.FILE_READ_ATTRIBUTES)
	info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION | windows.PROTECTED_DACL_SECURITY_INFORMATION)
	var fileOwner *windows.SID
	if ownerSID == "" {
		access |= windows.WRITE_OWNER
		info |= windows.OWNER_SECURITY_INFORMATION
		fileOwner = administratorsSID
	}
	handle, err := windows.CreateFile(name, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE, nil, windows.OPEN_EXISTING, flags, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(handle)
	var attributes windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &attributes); err != nil {
		return err
	}
	if attributes.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("credential path is a reparse point")
	}
	if err := windows.SetSecurityInfo(
		handle,
		windows.SE_FILE_OBJECT,
		info,
		fileOwner,
		nil,
		acl,
		nil,
	); err != nil {
		return fmt.Errorf("设置 Windows ACL: %w", err)
	}
	return nil
}

func fullAccessEntry(
	sid *windows.SID,
	trusteeType windows.TRUSTEE_TYPE,
	inheritance uint32,
) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  trusteeType,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}
