package quic

import (
	"net"
)

type sender interface {
	Send(p *packetBuffer)
	Run() error
	WouldBlock() bool
	Available() <-chan struct{}
	Close()
	ChangeAddr(addr *netAddrInfo) bool
}

type netAddrInfo struct {
	addr net.Addr
	info *packetInfo
}

type sendQueue struct {
	queue       chan *packetBuffer
	closeCalled chan struct{} // runStopped when Close() is called
	runStopped  chan struct{} // runStopped when the run loop returns
	available   chan struct{}
	conn        sendConn
	changeAddr  chan *netAddrInfo
}

var _ sender = &sendQueue{}

const sendQueueCapacity = 8

func newSendQueue(conn sendConn) sender {
	return &sendQueue{
		conn:        conn,
		runStopped:  make(chan struct{}),
		closeCalled: make(chan struct{}),
		available:   make(chan struct{}, 1),
		queue:       make(chan *packetBuffer, sendQueueCapacity),
		changeAddr:  make(chan *netAddrInfo, 1),
	}
}

// Send sends out a packet. It's guaranteed to not block.
// Callers need to make sure that there's actually space in the send queue by calling WouldBlock.
// Otherwise Send will panic.
func (h *sendQueue) Send(p *packetBuffer) {
	select {
	case h.queue <- p:
	case <-h.runStopped:
	default:
		panic("sendQueue.Send would have blocked")
	}
}

func (h *sendQueue) WouldBlock() bool {
	return len(h.queue) == sendQueueCapacity
}

func (h *sendQueue) Available() <-chan struct{} {
	return h.available
}

func (h *sendQueue) Run() error {
	defer close(h.runStopped)
	var shouldClose bool
	for {
		if shouldClose && len(h.queue) == 0 {
			return nil
		}
		select {
		case <-h.closeCalled:
			h.closeCalled = nil // prevent this case from being selected again
			// make sure that all queued packets are actually sent out
			shouldClose = true
		case p := <-h.queue:
			if err := h.conn.Write(p.Data); err != nil {
				return err
			}
			p.Release()
			select {
			case h.available <- struct{}{}:
			default:
			}
		case changeAddr := <-h.changeAddr:
			h.conn.SetRemoteAddr(changeAddr.addr, changeAddr.info)
		}
	}
}

func (h *sendQueue) ChangeAddr(addr *netAddrInfo) bool {
	select {
	case h.changeAddr <- addr:
		return true
	default:
		return false
	}
}

func (h *sendQueue) Close() {
	close(h.closeCalled)
	// wait until the run loop returned
	<-h.runStopped
}
