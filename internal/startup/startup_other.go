//go:build !windows

package startup

import "errors"

func Configure(enabled bool) error {
	if enabled {
		return errors.New("当前系统不支持自动启动设置")
	}
	return nil
}
