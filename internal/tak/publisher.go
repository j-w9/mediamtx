package tak

import (
	"crypto/tls"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/bluenviron/mediamtx/internal/compress"
	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
	ptls "github.com/bluenviron/mediamtx/internal/protocols/tls"
)

type publisherPathManager interface {
	APIPathsList() (*defs.APIPathList, error)
}

// Publisher announces the streams of the server to TAK clients and TAK servers.
type Publisher struct {
	Addresses         []string
	VideoHost         string
	Interval          conf.Duration
	ClientCert        string
	ClientKey         string
	ServerFingerprint string
	RTSPAddress       string
	PathManager       publisherPathManager
	// returns the current configuration.
	Conf   func() *conf.Conf
	Parent logger.Writer

	uidPrefix string
	rtspPort  int
	dests     []*dest
	terminate chan struct{}
	done      chan struct{}
}

// Initialize initializes the publisher.
func (p *Publisher) Initialize() error {
	tlsConfig := ptls.MakeConfig(p.ServerFingerprint)
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	}

	if p.ClientCert != "" {
		cert, err := tls.LoadX509KeyPair(p.ClientCert, p.ClientKey)
		if err != nil {
			return fmt.Errorf("unable to load the TAK client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	_, port, err := net.SplitHostPort(p.RTSPAddress)
	if err != nil {
		return err
	}
	p.rtspPort, _ = strconv.Atoi(port)

	host, _ := os.Hostname()
	if host == "" {
		host = "server"
	}
	p.uidPrefix = "MediaMTX-" + host + "-"

	for _, addr := range p.Addresses {
		d := &dest{
			address:   addr,
			tlsConfig: tlsConfig,
			parent:    p.Parent,
		}
		d.initialize()
		p.dests = append(p.dests, d)
	}

	p.terminate = make(chan struct{})
	p.done = make(chan struct{})

	p.Parent.Log(logger.Info, "[TAK] started, announcing streams to %s", strings.Join(p.Addresses, ", "))

	go p.run()

	return nil
}

// Close closes the publisher.
func (p *Publisher) Close() {
	close(p.terminate)
	<-p.done

	for _, d := range p.dests {
		d.close()
	}
}

func (p *Publisher) run() {
	defer close(p.done)

	interval := time.Duration(p.Interval)
	t := time.NewTicker(interval)
	defer t.Stop()

	for {
		select {
		case <-t.C:
			p.publish(interval)

		case <-p.terminate:
			return
		}
	}
}

// Feeds returns what is announced, given the state of the server.
// videoHost is the address at which receivers can reach the server.
func Feeds(
	paths []defs.APIPath,
	pathConfs map[string]*conf.Path,
	uidPrefix string,
	videoHost string,
	rtspPort int,
) []*Feed {
	var ret []*Feed

	for i := range paths {
		pa := &paths[i]
		if !pa.Available {
			continue
		}

		cnf, _, err := conf.FindPathConf(pathConfs, pa.Name)
		if err != nil || !cnf.TAKShare {
			continue
		}

		// compressed copies are announced through the stream they are made from
		if cnf.Compress && compress.IsOutputPath(pa.Name, cnf) {
			continue
		}

		f := &Feed{
			UID:      uidPrefix + strings.ReplaceAll(pa.Name, "/", "-"),
			Callsign: pa.Name,
			Model:    "MediaMTX",
		}

		switch t := pa.Telemetry; {
		case t != nil && t.SensorLat != nil && t.SensorLon != nil:
			f.Type = TypeUAS
			f.Lat = *t.SensorLat
			f.Lon = *t.SensorLon
			f.Alt = t.SensorAlt
			f.Heading = t.PlatformHeading
			f.Speed = t.GroundSpeed
			f.TargetLat = t.FrameCenterLat
			f.TargetLon = t.FrameCenterLon
			f.TargetAlt = t.FrameCenterElev
			f.HFOV = t.HFOV
			f.VFOV = t.VFOV
			if t.CallSign != "" {
				f.Callsign = t.CallSign
			}
			if t.Sensor != "" {
				f.Model = t.Sensor
			}

		case cnf.Latitude != 0 || cnf.Longitude != 0:
			// a camera whose position has been entered by hand
			f.Type = TypeGroundSensor
			f.Lat = cnf.Latitude
			f.Lon = cnf.Longitude

		default:
			// a stream cannot be placed on a map without a position
			continue
		}

		// on constrained links, clients are better served by the compressed copy
		videoPath := pa.Name
		if cnf.Compress {
			videoPath = compress.OutputPath(pa.Name, cnf)
		}

		if videoHost != "" {
			f.Video = &Video{Protocol: "rtsp", Host: videoHost, Port: rtspPort, Path: videoPath}
		}

		ret = append(ret, f)
	}

	return ret
}

func (p *Publisher) publish(interval time.Duration) {
	list, err := p.PathManager.APIPathsList()
	if err != nil {
		return
	}

	cnf := p.Conf()
	now := time.Now()

	// markers disappear from maps a few intervals after the last message
	stale := 5 * interval

	for _, d := range p.dests {
		host := p.VideoHost
		if host == "" {
			if ip := d.local(); ip != nil && !ip.IsUnspecified() {
				host = ip.String()
			}
		}

		for _, f := range Feeds(list.Items, cnf.Paths, p.uidPrefix, host, p.rtspPort) {
			for _, ev := range Events(f, now, stale) {
				d.send(ev)
			}
		}
	}
}
