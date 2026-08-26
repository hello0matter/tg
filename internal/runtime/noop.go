package runtime

import (
	"errors"
	"sync"

	"tgworkbench/internal/domain"
)

type Noop struct {
	mu sync.Mutex
}

func (n *Noop) Connect(string) error    { return errors.New("Telegram 运行时尚未初始化") }
func (n *Noop) Disconnect(string) error { return nil }
func (n *Noop) IdentifyAccount(string) error {
	return errors.New("Telegram 运行时尚未初始化")
}
func (n *Noop) SubmitCode(string, string) error {
	return errors.New("当前没有等待验证码的登录")
}
func (n *Noop) SubmitPassword(string, string) error {
	return errors.New("当前没有等待两步验证密码的登录")
}
func (n *Noop) Approve(string) error { return nil }
func (n *Noop) SendManual(string, string, domain.ManualDestination) error {
	return errors.New("Telegram 运行时尚未初始化")
}
func (n *Noop) ListPeers(string) ([]domain.PeerRef, error) {
	return nil, errors.New("Telegram 运行时尚未初始化")
}
