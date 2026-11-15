//go:build linux

package cascade

import "syscall"

// markControl sets SO_MARK on the socket so it is routed like Xray's egress.
// Needs CAP_NET_ADMIN (vlr runs as root).
func markControl(mark int) (func(network, address string, c syscall.RawConn) error, error) {
	return func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_MARK, mark)
		}); err != nil {
			return err
		}
		return serr
	}, nil
}
