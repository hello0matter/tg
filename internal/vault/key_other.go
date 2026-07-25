//go:build !windows

package vault

func protectKey(key []byte) ([]byte, error) {
	return append([]byte(nil), key...), nil
}

func unprotectKey(stored []byte) ([]byte, error) {
	return append([]byte(nil), stored...), nil
}
