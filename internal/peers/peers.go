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

	if base := rtspBase(p, g, true); base != "" {
		return func(name string) string { return base + name }
	}

	return nil
}

// rtspBase returns the RTSP address of a peer, to be followed by the name of a stream.
func rtspBase(p *conf.Peer, g *remoteGlobal, withCredentials bool) string {
	u, _ := url.Parse(p.URL)
	host := u.Hostname()

	if !g.RTSP {
		return ""
	}

	userinfo := ""
	if withCredentials && p.User != "" {
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

// state is what is known about a peer.
type state struct {
	version   string
	latencyMS float64
	global    remoteGlobal
	paths     []remotePath
	confs     map[string]*remotePathConf
}

// compressedName returns the name of the compressed copy of a stream, if the peer makes one.
func (st *state) compressedName(pa *remotePath) string {
	c := st.confs[pa.ConfName]
	if c == nil || !c.Compress || strings.HasSuffix(pa.Name, c.CompressPathSuffix) {
		return ""
	}
	return pa.Name + c.CompressPathSuffix
}

func fetch(ctx context.Context, p *conf.Peer) (*state, error) {
	tr := &http.Transport{TLSClientConfig: tls.MakeConfig(p.Fingerprint)}
	defer tr.CloseIdleConnections()
	hc := &http.Client{Transport: tr, Timeout: requestTimeout}

	st := &state{confs: make(map[string]*remotePathConf)}
	start := time.Now()

	var info struct {
		Version string `json:"version"`
	}
	err := get(ctx, hc, p, "/v3/info", &info)
	if err != nil {
		return nil, err
	}

	st.latencyMS = float64(time.Since(start).Microseconds()) / 1000
	st.version = info.Version

	var paths struct {
		Items []remotePath `json:"items"`
	}
	var confs struct {
		Items []remotePathConf `json:"items"`
	}

	for path, out := range map[string]any{
		"/v3/config/global/get":                   &st.global,
		"/v3/paths/list?itemsPerPage=1000":        &paths,
		"/v3/config/paths/list?itemsPerPage=1000": &confs,
	} {
		err = get(ctx, hc, p, path, out)
		if err != nil {
			return nil, err
		}
	}

	st.paths = paths.Items
	for i := range confs.Items {
		st.confs[confs.Items[i].Name] = &confs.Items[i]
	}

	return st, nil
}

// Sources are the addresses from which a stream of a peer can be pulled.
type Sources struct {
	Original string
	// empty when the peer does not make a compressed copy of the stream.
	Compressed string
}

// Resolve returns the addresses from which a stream of a peer can be pulled.
func Resolve(ctx context.Context, p *conf.Peer, feed string) (*Sources, error) {
	st, err := fetch(ctx, p)
	if err != nil {
		return nil, err
	}

	src := source(p, &st.global)
	if src == nil {
		return nil, fmt.Errorf("the peer has neither SRT nor RTSP enabled")
	}

	for i := range st.paths {
		if st.paths[i].Name == feed {
			ret := &Sources{Original: src(feed)}
			if cn := st.compressedName(&st.paths[i]); cn != "" {
				ret.Compressed = src(cn)
			}
			return ret, nil
		}
	}

	return nil, fmt.Errorf("the peer has no stream named '%s'", feed)
}

// Variant returns what a local path is pulling from a peer: "original", "compressed",
// or an empty string when the source of the path does not come from the peer.
func Variant(sources *Sources, current string) string {
	switch current {
	case sources.Original:
		return "original"
	case sources.Compressed:
		if current != "" {
			return "compressed"
		}
	}
	return ""
}

func query(ctx context.Context, p *conf.Peer, local map[string]*conf.Path) defs.APIPeer {
	ret := defs.APIPeer{Name: p.Name, URL: p.URL, Feeds: []defs.APIPeerFeed{}}

	st, err := fetch(ctx, p)
	if err != nil {
		ret.Error = err.Error()
		return ret
	}

	ret.Reachable = true
	ret.Version = st.version
	ret.LatencyMS = st.latencyMS
	ret.PushBase = rtspBase(p, &st.global, false)

	src := source(p, &st.global)

	// compressed copies are offered together with the stream they are made from
	copies := make(map[string]struct{})
	for i := range st.paths {
		if cn := st.compressedName(&st.paths[i]); cn != "" {
			copies[cn] = struct{}{}
		}
	}

	for i := range st.paths {
		pa := &st.paths[i]
		if _, ok := copies[pa.Name]; ok {
			continue
		}

		feed := defs.APIPeerFeed{Name: pa.Name, Available: pa.Available, Tracks: pa.Tracks}
		if feed.Tracks == nil {
			feed.Tracks = []string{}
		}

		cn := st.compressedName(pa)
		feed.Compressed = cn != ""

		for name, lp := range local {
			if lp.PeerName != p.Name || lp.PeerFeed != pa.Name || lp.Regexp != nil {
				continue
			}
			feed.PulledBy = name
			feed.PulledMode = lp.PeerMode
			if src != nil {
				sources := &Sources{Original: src(pa.Name)}
				if cn != "" {
					sources.Compressed = src(cn)
				}
				feed.PulledVariant = Variant(sources, lp.Source)
			}
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
