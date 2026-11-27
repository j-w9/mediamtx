package tak

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/bluenviron/mediamtx/internal/logger"
)

const (
	dialTimeout    = 10 * time.Second
	writeTimeout   = 10 * time.Second
	reconnectPause = 5 * time.Second
	// messages waiting to be sent to a destination. When a destination is slow or down,
	// old messages are dropped, since positions are only useful when they are fresh.
	queueSize = 64
)

// ValidateAddress checks the address of a destination.
func ValidateAddress(addr string) error {
	u, err := url.Parse(addr)
	if err != nil {
		return err
	}

	switch u.Scheme {
	case "udp", "tcp", "tls":
	default:
		return fmt.Errorf("unsupported scheme '%s' (supported ones are udp, tcp and tls)", u.Scheme)
	}

	_, _, err = net.SplitHostPort(u.Host)
	if err != nil {
		return fmt.Errorf("address must contain host and port")
	}

	return nil
}

// dest is a destination of messages.
type dest struct {
	address   string
	tlsConfig *tls.Config
	parent    logger.Writer

	ctx       context.Context
	ctxCancel func()
	queue     chan []byte
	done      chan struct{}

	mutex     sync.Mutex
	localIP   net.IP
	connected bool
}

func (d *dest) initialize() {
	d.ctx, d.ctxCancel = context.WithCancel(context.Background())
	d.queue = make(chan []byte, queueSize)
	d.done = make(chan struct{})
	go d.run()
}

func (d *dest) close() {
	d.ctxCancel()
	<-d.done
}

func (d *dest) send(msg []byte) {
	select {
	case d.queue <- msg:
	default:
		// make room by dropping the oldest message
		select {
		case <-d.queue:
		default:
		}
		select {
		case d.queue <- msg:
		default:
		}
	}
}

// local address used to reach the destination, which is the one that
// receivers on that network can use to reach this server.
func (d *dest) local() net.IP {
	d.mutex.Lock()
	defer d.mutex.Unlock()
	return d.localIP
}

func (d *dest) dial() (net.Conn, error) {
	u, _ := url.Parse(d.address)

	ctx, cancel := context.WithTimeout(d.ctx, dialTimeout)
	defer cancel()

	switch u.Scheme {
	case "udp":
		return (&net.Dialer{}).DialContext(ctx, "udp", u.Host)

	case "tls":
		cnf := d.tlsConfig.Clone()
		if cnf.ServerName == "" {
			cnf.ServerName, _, _ = net.SplitHostPort(u.Host)
		}
		return (&tls.Dialer{Config: cnf}).DialContext(ctx, "tcp", u.Host)

	default:
		return (&net.Dialer{}).DialContext(ctx, "tcp", u.Host)
	}
}

func (d *dest) run() {
	defer close(d.done)

	for {
		err := d.runInner()

		select {
		case <-d.ctx.Done():
			return
		default:
		}

		d.parent.Log(logger.Warn, "[TAK] %s: %v, retrying in %v", d.address, err, reconnectPause)

		select {
		case <-time.After(reconnectPause):
		case <-d.ctx.Done():
			return
		}
	}
}

func (d *dest) runInner() error {
	conn, err := d.dial()
	if err != nil {
		return err
	}
	defer conn.Close()

	d.mutex.Lock()
	d.connected = true
	if a, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		d.localIP = a.IP
	} else if a, ok2 := conn.LocalAddr().(*net.TCPAddr); ok2 {
		d.localIP = a.IP
	}
	d.mutex.Unlock()

	defer func() {
		d.mutex.Lock()
		d.connected = false
		d.mutex.Unlock()
	}()

	d.parent.Log(logger.Info, "[TAK] sending to %s", d.address)

	// servers close connections they do not like: notice it even when there is nothing to send
	readErr := make(chan error, 1)
	if _, isUDP := conn.(*net.UDPConn); !isUDP {
		go func() {
			buf := make([]byte, 2048)
			for {
				_, err2 := conn.Read(buf)
				if err2 != nil {
					readErr <- err2
					return
				}
			}
		}()
	}

	for {
		select {
		case msg := <-d.queue:
			conn.SetWriteDeadline(time.Now().Add(writeTimeout)) //nolint:errcheck
			_, err = conn.Write(msg)
			if err != nil {
				// a datagram that cannot be delivered right now is not a reason to reconnect
				if _, isUDP := conn.(*net.UDPConn); isUDP {
					continue
				}
				return err
			}

		case err = <-readErr:
			return err

		case <-d.ctx.Done():
			return fmt.Errorf("terminated")
		}
	}
}
