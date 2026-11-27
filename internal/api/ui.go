package api //nolint:revive

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

//go:embed ui/index.html
var uiIndex []byte

// the page is compressed once, at startup, and identified by a hash of its content,
// so that browsers download it a single time and then just check that it has not changed.
var (
	uiIndexGzip = func() []byte {
		var buf bytes.Buffer
		w, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		w.Write(uiIndex) //nolint:errcheck
		w.Close()        //nolint:errcheck
		return buf.Bytes()
	}()

	uiIndexETag = func() string {
		sum := sha256.Sum256(uiIndex)
		return `"` + hex.EncodeToString(sum[:8]) + `"`
	}()
)

// the UI is a single page with inline styles and scripts.
// It talks to the API from the same origin, and embeds the pages of the
// WebRTC, HLS and playback servers, which listen on other ports.
const uiContentSecurityPolicy = "default-src 'none'; " +
	"style-src 'unsafe-inline'; " +
	"script-src 'unsafe-inline'; " +
	"connect-src 'self'; " +
	"frame-src http: https:; " +
	"media-src http: https:; " +
	"base-uri 'none'; " +
	"form-action 'none'; " +
	"frame-ancestors 'none'"

func acceptsGzip(req *http.Request) bool {
	return strings.Contains(req.Header.Get("Accept-Encoding"), "gzip")
}

func (a *API) onUIRedirect(ctx *gin.Context) {
	ctx.Redirect(http.StatusFound, "/ui/")
}

func (a *API) onUI(ctx *gin.Context) {
	ctx.Header("ETag", uiIndexETag)
	ctx.Header("Cache-Control", "no-cache")
	ctx.Header("Vary", "Accept-Encoding")

	if ctx.GetHeader("If-None-Match") == uiIndexETag {
		ctx.Status(http.StatusNotModified)
		return
	}

	ctx.Header("Content-Security-Policy", uiContentSecurityPolicy)
	ctx.Header("X-Content-Type-Options", "nosniff")

	if acceptsGzip(ctx.Request) {
		ctx.Header("Content-Encoding", "gzip")
		ctx.Data(http.StatusOK, "text/html; charset=utf-8", uiIndexGzip)
		return
	}

	ctx.Data(http.StatusOK, "text/html; charset=utf-8", uiIndex)
}

type gzipResponseWriter struct {
	gin.ResponseWriter
	gz *gzip.Writer
}

func (w *gzipResponseWriter) Write(b []byte) (int, error) {
	return w.gz.Write(b)
}

func (w *gzipResponseWriter) WriteString(s string) (int, error) {
	return w.gz.Write([]byte(s))
}

// middlewareGzip compresses the responses to GET requests of clients that support it.
// Lists of paths and configurations are verbose JSON documents that are requested
// periodically by the UI, and shrink to a fraction of their size.
func (a *API) middlewareGzip(ctx *gin.Context) {
	if ctx.Request.Method != http.MethodGet ||
		!strings.HasPrefix(ctx.Request.URL.Path, "/v3/") ||
		!acceptsGzip(ctx.Request) {
		return
	}

	gz, _ := gzip.NewWriterLevel(ctx.Writer, gzip.BestSpeed)

	ctx.Header("Content-Encoding", "gzip")
	ctx.Header("Vary", "Accept-Encoding")
	ctx.Writer = &gzipResponseWriter{ResponseWriter: ctx.Writer, gz: gz}

	ctx.Next()

	gz.Close() //nolint:errcheck
}
