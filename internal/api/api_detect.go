package api //nolint:revive

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/detect"
)

// onDetectModelsList returns the detection models that can be used, with their classes.
// Without parameters, it returns the models found in the models directory.
// With the "server" and "model" parameters, it returns a model hosted by an inference server.
func (a *API) onDetectModelsList(ctx *gin.Context) {
	c := a.Parent.APIConfigSnapshot()
	cnf := c.PathDefaults.Clone()

	data := &defs.APIDetectModelList{Items: []defs.APIDetectModel{}}

	if server := ctx.Query("server"); server != "" {
		cnf.DetectServer = server
		cnf.DetectModel = ctx.Query("model")
		data.Items = append(data.Items, detect.Describe(ctx.Request.Context(), cnf))
	} else {
		for _, name := range detect.LocalModels(cnf.DetectModelsDir) {
			cnf.DetectModel = name
			data.Items = append(data.Items, detect.Describe(ctx.Request.Context(), cnf))
		}
	}

	ctx.JSON(http.StatusOK, data)
}
