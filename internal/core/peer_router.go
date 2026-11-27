package core

import (
	"context"
	"encoding/json"
	"time"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
	"github.com/bluenviron/mediamtx/internal/peers"
)

const (
	peerRouterInterval = 5 * time.Second
	// addresses of the streams of peers are asked again after this time.
	peerSourcesTTL = time.Minute
)

type peerRouterPathManager interface {
	APIPathsList() (*defs.APIPathList, error)
	APIStaticSourcesGet(string) (*defs.APIStaticSource, error)
}

type peerSources struct {
	sources *peers.Sources
	time    time.Time
}

// peerRouter watches the streams that are pulled from peers in "auto" mode,
// and switches each one between the original and the compressed copy
// according to how well the link is carrying it.
type peerRouter struct {
	pathManager peerRouterPathManager
	conf        func() *conf.Conf
	patch       func(ctx context.Context, name string, in conf.OptionalPath) error
	parent      logger.Writer

	ctx       context.Context
	ctxCancel func()

	links   map[string]*peers.Link
	sources map[string]*peerSources
}

func (r *peerRouter) initialize() {
	r.ctx, r.ctxCancel = context.WithCancel(context.Background())
	r.links = make(map[string]*peers.Link)
	r.sources = make(map[string]*peerSources)

	go r.run()
}

// close does not wait for the routine to exit, since the routine can be waiting for a
// configuration change to be applied, and that change can be what is closing the router.
func (r *peerRouter) close() {
	r.ctxCancel()
}

func (r *peerRouter) run() {
	t := time.NewTicker(peerRouterInterval)
	defer t.Stop()

	for {
		select {
		case <-t.C:
			r.check()
		case <-r.ctx.Done():
			return
		}
	}
}

func (r *peerRouter) resolve(peer *conf.Peer, feed string, now time.Time) *peers.Sources {
	key := peer.Name + "\x00" + feed

	if cached, ok := r.sources[key]; ok && now.Sub(cached.time) < peerSourcesTTL {
		return cached.sources
	}

	s, err := peers.Resolve(r.ctx, peer, feed)
	if err != nil {
		// the peer cannot be asked right now: keep what was known
		if cached, ok := r.sources[key]; ok {
			return cached.sources
		}
		return nil
	}

	r.sources[key] = &peerSources{sources: s, time: now}
	return s
}

func (r *peerRouter) check() {
	cnf := r.conf()

	// streams pulled in auto mode
	auto := make(map[string]*conf.Path)
	for name, pa := range cnf.Paths {
		if pa.PeerMode == "auto" && pa.PeerName != "" && pa.Regexp == nil {
			auto[name] = pa
		}
	}

	for name := range r.links {
		if _, ok := auto[name]; !ok {
			delete(r.links, name)
		}
	}

	if len(auto) == 0 {
		return
	}

	list, err := r.pathManager.APIPathsList()
	if err != nil {
		return
	}

	live := make(map[string]*defs.APIPath)
	for i := range list.Items {
		live[list.Items[i].Name] = &list.Items[i]
	}

	now := time.Now()

	for name, pa := range auto {
		var peer *conf.Peer
		for i := range cnf.Peers {
			if cnf.Peers[i].Name == pa.PeerName {
				peer = &cnf.Peers[i]
			}
		}
		if peer == nil {
			continue
		}

		sources := r.resolve(peer, pa.PeerFeed, now)
		if sources == nil || sources.Compressed == "" {
			continue // there is no choice to make
		}

		current := peers.Variant(sources, pa.Source)
		if current == "" {
			current = "original"
		}

		link, ok := r.links[name]
		if !ok {
			link = &peers.Link{Variant: current}
			r.links[name] = link
		}

		var health peers.Health
		if p := live[name]; p != nil {
			health = peers.Health{Available: p.Available, FramesInError: p.InboundFramesInError}
		}

		// what the connection itself knows about the link
		if src, err2 := r.pathManager.APIStaticSourcesGet(name); err2 == nil {
			switch ts := src.TypeSpecific.(type) {
			case *defs.APIStaticSourceTypeSpecificSRT:
				health.PacketsReceived = ts.PacketsReceived
				health.PacketsLost = ts.PacketsReceivedLoss
				health.FramesInError += ts.PacketsReceivedDrop

			case *defs.APIStaticSourceTypeSpecificRTSP:
				health.PacketsReceived = ts.InboundRTPPackets
				health.PacketsLost = ts.InboundRTPPacketsLost
			}
		}

		want := link.Update(health, now)
		if want == current {
			continue
		}

		source := sources.Original
		if want == "compressed" {
			source = sources.Compressed
		}

		byts, _ := json.Marshal(map[string]string{"source": source})
		var in conf.OptionalPath
		err = json.Unmarshal(byts, &in)
		if err != nil {
			continue
		}

		if want == "compressed" {
			r.parent.Log(logger.Warn, "[peers] link to '%s' cannot carry '%s', switching to its compressed copy",
				peer.Name, pa.PeerFeed)
		} else {
			r.parent.Log(logger.Info, "[peers] trying the original of '%s' from '%s' again", pa.PeerFeed, peer.Name)
		}

		err = r.patch(r.ctx, name, in)
		if err != nil {
			r.parent.Log(logger.Warn, "[peers] unable to switch '%s': %v", name, err)
		}
	}
}
