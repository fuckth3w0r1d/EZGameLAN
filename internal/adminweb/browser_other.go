//go:build !windows && !linux

package adminweb

import "errors"

func OpenBrowser(string) error {
	return errors.New("请手动打开管理地址")
}
