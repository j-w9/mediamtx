// Package discovery finds video sources on the networks the server is connected to.
package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/protocols/udp"
)

const (
	sapAddress   = "224.2.127.254:9875"
	onvifAddress = "239.255.255.250:3702"
	takAddress   = "239.2.3.1:6969"

	// sources that have not been seen for this long are forgotten.
	expiration = 5 * time.Minute
	// pause before listening again after a failure.
	retryPause = 30 * time.Second
	// interval between probes for ONVIF cameras.
	onvifInterval = 60 * time.Second
	onvifWait     = 4 * time.Second
	// interval between checks of the multicast groups.
	multicastWait = 3 * time.Second
)

// methods through which a source can be found.
const (
	MethodSAP       = "sap"
	MethodMulticast = "multicast"
	MethodONVIF     = "onvif"
	MethodTAK       = "tak"
)

// Finder looks for video sources:
//   - streams announced with SAP
//   - MPEG-TS streams sent to the multicast groups it is told to check
//   - ONVIF cameras, that answer WS-Discovery probes
//   - streams announced by TAK clients and servers
type Finder struct {
	// multicast groups to check for MPEG-TS, as "group:port".
	Multicast []string
	// prefix of the announcements made by this server, which are ignored.
	OwnTAKPrefix string
	Parent       logger.Writer

	ctx       context.Context
	ctxCancel func()
	wg        sync.WaitGroup

	mutex   sync.Mutex
	sources map[string]*defs.APIDiscoveredSource
}

// Initialize initializes the finder.
func (f *Finder) Initialize() {
	f.ctx, f.ctxCancel = context.WithCancel(context.Background())
	f.sources = make(map[string]*defs.APIDiscoveredSource)

	f.loop("SAP", sapAddress, 0, f.onSAP)
	f.loop("TAK", takAddress, 0, f.onTAK)

	for _, addr := range f.Multicast {
		f.loop("multicast "+addr, addr, multicastWait, func(buf []byte, _ net.Addr) {
			f.onMulticast(addr, buf)
		})
	}

	f.wg.Add(1)
	go f.runONVIF()

	f.Log(logger.Info, "started")
}

// Close closes the finder.
func (f *Finder) Close() {
	f.ctxCancel()
	f.wg.Wait()
}

// Log implements logger.Writer.
func (f *Finder) Log(level logger.Level, format string, args ...any) {
	f.Parent.Log(level, "[discovery] "+format, args...)
}

// loop listens on a multicast address until the finder is closed.
// When pause is not zero, the address is left after each datagram and joined
// again later, so that streams that are not wanted do not keep flowing to this machine.
func (f *Finder) loop(name string, address string, pause time.Duration, cb func([]byte, net.Addr)) {
	f.wg.Go(func() {
		for {
			err := f.listen(address, pause, cb)

			select {
			case <-f.ctx.Done():
				return
			default:
			}

			wait := pause
			if err != nil {
				f.Log(logger.Debug, "%s: %v", name, err)
				wait = retryPause
			}

			select {
			case <-time.After(wait):
			case <-f.ctx.Done():
				return
			}
		}
	})
}

func (f *Finder) listen(address string, pause time.Duration, cb func([]byte, net.Addr)) error {
	l := &udp.Listener{Address: address, IntfName: "all"}
	err := l.Initialize()
	if err != nil {
		return err
	}

	stop := context.AfterFunc(f.ctx, func() { l.Close() }) //nolint:errcheck
	defer stop()
	defer l.Close() //nolint:errcheck

	buf := make([]byte, 65536)

	for {
		if pause != 0 {
			l.SetReadDeadline(time.Now().Add(pause)) //nolint:errcheck
		}

		n, err2 := l.Read(buf)
		if err2 != nil {
			if pause != 0 {
				return nil // nothing is being sent to this group
			}
			return err2
		}

		cb(buf[:n], nil)

		if pause != 0 {
			return nil
		}
	}
}

func (f *Finder) put(method string, key string, src *defs.APIDiscoveredSource) {
	sum := sha256.Sum256([]byte(method + "|" + key))
	id := hex.EncodeToString(sum[:8])
	now := time.Now()

	f.mutex.Lock()
	defer f.mutex.Unlock()

	if cur, ok := f.sources[id]; ok {
		cur.LastSeen = now
		cur.Name = src.Name
		cur.Source = src.Source
		cur.Info = src.Info
		if src.Conf != nil {
			cur.Conf = src.Conf
		}
		return
	}

	src.ID = id
	src.Method = method
	src.FirstSeen = now
	src.LastSeen = now
	if src.Conf == nil {
		src.Conf = map[string]string{}
	}
	f.sources[id] = src

	f.Log(logger.Info, "found %s (%s)", src.Source, method)
}

func (f *Finder) remove(method string, key string) {
	sum := sha256.Sum256([]byte(method + "|" + key))

	f.mutex.Lock()
	defer f.mutex.Unlock()

	delete(f.sources, hex.EncodeToString(sum[:8]))
}

func (f *Finder) onSAP(buf []byte, _ net.Addr) {
	p, err := parseSAP(buf)
	if err != nil {
		return
	}

	v, err := parseSDPVideo(p.sdp)
	if err != nil {
		return
	}

	key := net.JoinHostPort(v.address, strconv.Itoa(v.port))

	if p.deletion {
		f.remove(MethodSAP, key)
		return
	}

	// FFmpeg announces sessions that have no title as "No Name"
	if v.name == "No Name" {
		v.name = ""
	}

	src := &defs.APIDiscoveredSource{Name: v.name, Address: key}

	if v.rawMPEGTS {
		src.Source = "udp+mpegts://" + key
		src.Info = "MPEG-TS"
	} else {
		// RTP streams need their description in order to be read
		src.Source = "udp+rtp://" + key
		src.Info = "RTP"
		src.Conf = map[string]string{"rtpSDP": p.sdp}
	}

	f.put(MethodSAP, key, src)
}

func (f *Finder) onMulticast(address string, buf []byte) {
	if !isMPEGTS(buf) {
		return
	}

	f.put(MethodMulticast, address, &defs.APIDiscoveredSource{
		Source:  "udp+mpegts://" + address,
		Address: address,
		Info:    "MPEG-TS",
	})
}

func (f *Finder) onTAK(buf []byte, _ net.Addr) {
	v, err := parseCoTVideo(buf)
	if err != nil {
		return
	}

	// do not find what this server is announcing
	if f.OwnTAKPrefix != "" && strings.HasPrefix(v.uid, f.OwnTAKPrefix) {
		return
	}

	f.put(MethodTAK, v.url, &defs.APIDiscoveredSource{
		Name:    v.callsign,
		Source:  v.url,
		Address: v.url,
		Info:    "TAK",
	})
}

func (f *Finder) runONVIF() {
	defer f.wg.Done()

	for {
		err := f.probeONVIF()
		if err != nil {
			f.Log(logger.Debug, "ONVIF: %v", err)
		}

		select {
		case <-time.After(onvifInterval):
		case <-f.ctx.Done():
			return
		}
	}
}

func (f *Finder) probeONVIF() error {
	pc, err := net.ListenPacket("udp4", ":0")
	if err != nil {
		return err
	}

	stop := context.AfterFunc(f.ctx, func() { pc.Close() })
	defer stop()
	defer pc.Close()

	dst, _ := net.ResolveUDPAddr("udp4", onvifAddress)

	_, err = pc.WriteTo(onvifProbe(uuid.New().String()), dst)
	if err != nil {
		return err
	}

	pc.SetReadDeadline(time.Now().Add(onvifWait)) //nolint:errcheck
	buf := make([]byte, 65536)

	for {
		n, _, err2 := pc.ReadFrom(buf)
		if err2 != nil {
			return nil //nolint:nilerr
		}

		d, err2 := parseONVIFProbeMatch(buf[:n])
		if err2 != nil {
			continue
		}

		name := d.name
		if name == "" {
			name = d.host
		}

		// the address of the stream depends on the camera: the most common one is proposed
		f.put(MethodONVIF, d.host, &defs.APIDiscoveredSource{
			Name:    name,
			Source:  "rtsp://" + net.JoinHostPort(d.host, "554") + "/",
			Address: d.host,
			Info:    "ONVIF camera",
		})
	}
}

// List returns the sources found, the most recent first.
func (f *Finder) List() []defs.APIDiscoveredSource {
	f.mutex.Lock()
	defer f.mutex.Unlock()

	now := time.Now()
	ret := make([]defs.APIDiscoveredSource, 0, len(f.sources))

	for id, s := range f.sources {
		if now.Sub(s.LastSeen) > expiration {
			delete(f.sources, id)
			continue
		}
		ret = append(ret, *s)
	}

	sort.Slice(ret, func(i, j int) bool {
		if !ret[i].FirstSeen.Equal(ret[j].FirstSeen) {
			return ret[i].FirstSeen.After(ret[j].FirstSeen)
		}
		return ret[i].ID < ret[j].ID
	})

	return ret
}
