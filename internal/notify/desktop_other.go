//go:build !darwin

package notify

func NewDesktopSender() Sender {
	return nil
}
