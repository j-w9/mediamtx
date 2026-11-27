package api //nolint:revive

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/bluenviron/mediacommon/v2/pkg/limitedreader"
	"github.com/gin-gonic/gin"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/peers"
)

// onPeersList returns the state of the peers and the streams they offer.
func (a *API) onPeersList(ctx *gin.Context) {
	c := a.Parent.APIConfigSnapshot()
	ctx.JSON(http.StatusOK, peers.List(ctx.Request.Context(), c.Peers, c.Paths))
}

// onPeersPull creates the path that pulls a stream from a peer.
// The address of the stream, which contains the credentials of the peer,
// is put together here, and is never sent to clients of the API.
func (a *API) onPeersPull(ctx *gin.Context) {
	var in defs.APIPeerPull
	err := json.NewDecoder(&limitedreader.Reader{R: ctx.Request.Body, N: maxInboundConfigSize}).Decode(&in)
	if err != nil {
		a.writeError(ctx, http.StatusBadRequest, err)
		return
	}

	if in.Mode == "" {
		in.Mode = "original"
	}

	c := a.Parent.APIConfigSnapshot()

	var peer *conf.Peer
	for i := range c.Peers {
		if c.Peers[i].Name == in.Peer {
			peer = &c.Peers[i]
		}
	}
	if peer == nil {
		a.writeError(ctx, http.StatusNotFound, fmt.Errorf("peer not found"))
		return
	}

	sources, err := peers.Resolve(ctx.Request.Context(), peer, in.Feed)
	if err != nil {
		a.writeError(ctx, http.StatusBadRequest, err)
		return
	}

	source := sources.Original
	if in.Mode == "compressed" {
		if sources.Compressed == "" {
			a.writeError(ctx, http.StatusBadRequest, fmt.Errorf("the peer does not compress this stream"))
			return
		}
		source = sources.Compressed
	}

	byts, _ := json.Marshal(map[string]string{
		"source":   source,
		"peerName": in.Peer,
		"peerFeed": in.Feed,
		"peerMode": in.Mode,
	})

	var p conf.OptionalPath
	err = json.Unmarshal(byts, &p)
	if err != nil {
		a.writeError(ctx, http.StatusBadRequest, err)
		return
	}

	err = a.Parent.APIConfigPathsReplace(a.ctx, in.Peer+"/"+in.Feed, p)
	if err != nil {
		a.writeConfigWriteError(ctx, err)
		return
	}

	a.writeOK(ctx)
}
