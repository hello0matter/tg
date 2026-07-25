//go:build windows

package startup

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows/registry"
)

const valueName = "TGWorkbench"

func Configure(enabled bool) error {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("打开 Windows 启动项: %w", err)
	}
	defer key.Close()
	if !enabled {
		if err := key.DeleteValue(valueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("删除 Windows 启动项: %w", err)
		}
		return nil
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return key.SetStringValue(valueName, `"`+executable+`"`)
}
