package api //nolint:revive

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/bluenviron/mediamtx/internal/peers"
)

// onPeersList returns the state of the peers and the streams they offer.
func (a *API) onPeersList(ctx *gin.Context) {
	c := a.Parent.APIConfigSnapshot()
	ctx.JSON(http.StatusOK, peers.List(ctx.Request.Context(), c.Peers, c.Paths))
}
