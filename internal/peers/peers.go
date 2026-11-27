// Package peers lists the streams of other servers, in order to share video between them.
package peers

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/protocols/tls"
)

const (
	requestTimeout = 4 * time.Second
	maxBodySize    = 8 * 1024 * 1024
)

type remoteGlobal struct {
	RTSP           bool   `json:"rtsp"`
	RTSPAddress    string `json:"rtspAddress"`
	RTSPSAddress   string `json:"rtspsAddress"`
	RTSPEncryption string `json:"rtspEncryption"`
	SRT            bool   `json:"srt"`
	SRTAddress     string `json:"srtAddress"`
}

// remotePath contains the fields of a path that are needed here. The path is not decoded
// as a whole, so that peers that run a different version are understood as well.
type remotePath struct {
	Name      string   `json:"name"`
	ConfName  string   `json:"confName"`
	Available bool     `json:"available"`
	Tracks    []string `json:"tracks"`
}

type remotePathConf struct {
	Name               string `json:"name"`
	Compress           bool   `json:"compress"`
	CompressPathSuffix string `json:"compressPathSuffix"`
}

func get(ctx context.Context, hc *http.Client, p *conf.Peer, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(p.URL, "/")+path, nil)
	if err != nil {
		return err
	}
	if p.User != "" {
		req.SetBasicAuth(p.User, p.Pass)
	}

	res, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()

	if res.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("the peer refused the credentials")
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("the peer replied with status %d", res.StatusCode)
	}

	return json.NewDecoder(http.MaxBytesReader(nil, res.Body, maxBodySize)).Decode(out)
}

func portOf(address string) string {
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return ""
	}
	return port
}

// source returns a function that gives the address from which a stream of a peer can be read.
// SRT is preferred since it recovers from the losses of long-distance links.
func source(p *conf.Peer, g *remoteGlobal) func(name string) string {
	u, _ := url.Parse(p.URL)
	host := u.Hostname()

	if g.SRT && portOf(g.SRTAddress) != "" {
		base := "srt://" + net.JoinHostPort(host, portOf(g.SRTAddress)) + "?streamid="
		creds := ""
		if p.User != "" {
			creds = ":" + p.User + ":" + p.Pass
		}
		return func(name string) string {
			return base + "read:" + name + creds
		}
	}

	if base := pushBase(p, g); base != "" {
		return func(name string) string { return base + name }
	}

	return nil
}

// pushBase returns the RTSP address of a peer, to be followed by the name of a stream.
// Streams can be read from it and pushed to it.
func pushBase(p *conf.Peer, g *remoteGlobal) string {
	u, _ := url.Parse(p.URL)
	host := u.Hostname()

	if !g.RTSP {
		return ""
	}

	userinfo := ""
	if p.User != "" {
		userinfo = url.UserPassword(p.User, p.Pass).String() + "@"
	}

	scheme, port := "rtsp", portOf(g.RTSPAddress)
	if g.RTSPEncryption == "strict" {
		scheme, port = "rtsps", portOf(g.RTSPSAddress)
	}
	if port == "" {
		return ""
	}

	return scheme + "://" + userinfo + net.JoinHostPort(host, port) + "/"
}

func query(ctx context.Context, p *conf.Peer, local map[string]*conf.Path) defs.APIPeer {
	ret := defs.APIPeer{Name: p.Name, URL: p.URL, Feeds: []defs.APIPeerFeed{}}

	tr := &http.Transport{TLSClientConfig: tls.MakeConfig(p.Fingerprint)}
	defer tr.CloseIdleConnections()
	hc := &http.Client{Transport: tr, Timeout: requestTimeout}

	start := time.Now()

	var info struct {
		Version string `json:"version"`
	}
	err := get(ctx, hc, p, "/v3/info", &info)
	if err != nil {
		ret.Error = err.Error()
		return ret
	}

	ret.LatencyMS = float64(time.Since(start).Microseconds()) / 1000
	ret.Version = info.Version

	var global remoteGlobal
	var paths struct {
		Items []remotePath `json:"items"`
	}
	var confs struct {
		Items []remotePathConf `json:"items"`
	}

	for path, out := range map[string]any{
		"/v3/config/global/get":                   &global,
		"/v3/paths/list?itemsPerPage=1000":        &paths,
		"/v3/config/paths/list?itemsPerPage=1000": &confs,
	} {
		err = get(ctx, hc, p, path, out)
		if err != nil {
			ret.Error = err.Error()
			return ret
		}
	}

	ret.Reachable = true
	ret.PushBase = pushBase(p, &global)

	src := source(p, &global)

	confByName := make(map[string]*remotePathConf)
	for i := range confs.Items {
		confByName[confs.Items[i].Name] = &confs.Items[i]
	}

	// compressed copies are offered together with the stream they are made from
	copies := make(map[string]struct{})
	for i := range paths.Items {
		pa := &paths.Items[i]
		if c := confByName[pa.ConfName]; c != nil && c.Compress && !strings.HasSuffix(pa.Name, c.CompressPathSuffix) {
			copies[pa.Name+c.CompressPathSuffix] = struct{}{}
		}
	}

	pulled := make(map[string]string)
	for name, pa := range local {
		pulled[pa.Source] = name
	}

	for i := range paths.Items {
		pa := &paths.Items[i]
		if _, ok := copies[pa.Name]; ok {
			continue
		}

		feed := defs.APIPeerFeed{Name: pa.Name, Available: pa.Available, Tracks: pa.Tracks}
		if feed.Tracks == nil {
			feed.Tracks = []string{}
		}

		c := confByName[pa.ConfName]
		feed.Compressed = c != nil && c.Compress && !strings.HasSuffix(pa.Name, c.CompressPathSuffix)

		if src != nil {
			feed.Source = src(pa.Name)
			if feed.Compressed {
				feed.SourceCompressed = src(pa.Name + c.CompressPathSuffix)
			}
		}

		if n, ok := pulled[feed.Source]; ok && feed.Source != "" {
			feed.PulledBy = n
		} else if n2, ok2 := pulled[feed.SourceCompressed]; ok2 && feed.SourceCompressed != "" {
			feed.PulledBy = n2
		}

		ret.Feeds = append(ret.Feeds, feed)
	}

	sort.Slice(ret.Feeds, func(i, j int) bool { return ret.Feeds[i].Name < ret.Feeds[j].Name })

	return ret
}

// List returns the state of the peers and their streams. Peers are queried at the same time.
func List(ctx context.Context, peers conf.Peers, local map[string]*conf.Path) *defs.APIPeerList {
	ret := &defs.APIPeerList{Items: make([]defs.APIPeer, len(peers))}

	var wg sync.WaitGroup
	for i := range peers {
		wg.Go(func() {
			ret.Items[i] = query(ctx, &peers[i], local)
		})
	}
	wg.Wait()

	return ret
}
