//go:build !linux && !darwin

package envd

import (
	"errors"
	"net"
)

func vsockListen(uint32) (net.Listener, error) { return nil, errors.New("vsock needs Linux or macOS") }

func vsockDial(uint32, uint32) (net.Conn, error) {
	return nil, errors.New("vsock needs Linux or macOS")
}
