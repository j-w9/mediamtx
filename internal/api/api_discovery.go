package api //nolint:revive

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// onDiscoveryList returns the video sources found on the network.
func (a *API) onDiscoveryList(ctx *gin.Context) {
	ctx.JSON(http.StatusOK, a.Parent.APIDiscoveryList())
}
