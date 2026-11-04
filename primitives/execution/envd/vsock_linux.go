package envd

import (
	"net"

	"github.com/mdlayher/vsock"
)

func vsockListen(port uint32) (net.Listener, error) { return vsock.Listen(port, nil) }

func vsockDial(cid, port uint32) (net.Conn, error) { return vsock.Dial(cid, port, nil) }
