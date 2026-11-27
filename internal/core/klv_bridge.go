package core

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"

	"github.com/bluenviron/mediamtx/internal/compress"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/stream"
	"github.com/bluenviron/mediamtx/internal/unit"
)

const klvBridgeRetryPause = 2 * time.Second

// newKLVMedia returns the description of a KLV track.
func newKLVMedia() *description.Media {
	return &description.Media{
		Type:    description.MediaTypeApplication,
		Formats: []format.Format{&format.KLV{PayloadTyp: 96}},
	}
}

func findKLV(desc *description.Session) (*description.Media, format.Format) {
	// streams that are always available get their description later
	if desc == nil {
		return nil, nil
	}

	for _, medi := range desc.Medias {
		for _, forma := range medi.Formats {
			if _, ok := forma.(*format.KLV); ok {
				return medi, forma
			}
		}
	}
	return nil, nil
}

// klvBridge copies the KLV metadata of a stream into its compressed copy.
// The encoder that produces the copy handles video and audio only, so metadata travels
// around it: it is read from the original stream and written into the copy as it is.
type klvBridge struct {
	baseName string
	out      *stream.SubStream
	outMedia *description.Media
	parent   pathParent
	logger   logger.Writer

	ctx       context.Context
	ctxCancel func()

	// prevents writing into the copy after it has been closed
	mutex  sync.RWMutex
	closed bool
}

func (b *klvBridge) start() {
	b.ctx, b.ctxCancel = context.WithCancel(context.Background())
	go b.run()
}

// stop does not wait for the routine to exit, since the routine can be waiting for the
// path manager, that in turn can be waiting for the path that is stopping the bridge.
func (b *klvBridge) stop() {
	b.ctxCancel()

	b.mutex.Lock()
	b.closed = true
	b.mutex.Unlock()
}

// Close implements defs.Reader.
func (b *klvBridge) Close() {}

// Log implements logger.Writer.
func (b *klvBridge) Log(level logger.Level, format string, args ...any) {
	b.logger.Log(level, "[KLV bridge] "+format, args...)
}

// APIReaderDescribe implements defs.Reader.
func (b *klvBridge) APIReaderDescribe() *defs.APIPathReader {
	return &defs.APIPathReader{Type: defs.APIPathReaderTypeHidden}
}

func (b *klvBridge) run() {
	for {
		err := b.runInner()

		select {
		case <-b.ctx.Done():
			return
		default:
		}

		if err != nil {
			b.Log(logger.Debug, "%v", err)
		}

		select {
		case <-time.After(klvBridgeRetryPause):
		case <-b.ctx.Done():
			return
		}
	}
}

func (b *klvBridge) runInner() error {
	res, err := b.parent.AddReader(defs.PathAddReaderReq{
		Author: b,
		AccessRequest: defs.PathAccessRequest{
			Name:     b.baseName,
			Query:    compress.InternalQuery(),
			SkipAuth: true,
			IP:       net.IPv4(127, 0, 0, 1),
		},
	})
	if err != nil {
		return err
	}
	defer res.Path.RemoveReader(defs.PathRemoveReaderReq{Author: b})

	medi, forma := findKLV(res.Stream.OrigDesc)
	if medi == nil {
		return fmt.Errorf("stream has no metadata")
	}

	start := time.Now()

	reader := &stream.Reader{SkipOutboundBytes: true, Parent: b}

	reader.OnData(medi, forma, func(u *unit.Unit) error {
		if u.NilPayload() {
			return nil
		}

		b.mutex.RLock()
		defer b.mutex.RUnlock()

		if b.closed {
			return nil
		}

		// the copy has its own timeline, that starts when the encoder connects
		b.out.WriteUnit(b.outMedia, b.outMedia.Formats[0], &unit.Unit{
			PTS:     int64(time.Since(start).Seconds() * 90000),
			NTP:     time.Now(),
			Payload: u.Payload,
		})

		return nil
	})

	res.Stream.AddReader(reader)
	defer res.Stream.RemoveReader(reader)

	select {
	case err = <-reader.Error():
		return err
	case <-b.ctx.Done():
		return nil
	}
}
