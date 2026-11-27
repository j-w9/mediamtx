package api_test

import (
	"compress/gzip"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bluenviron/mediamtx/internal/api"
	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/test"
)

func TestUI(t *testing.T) {
	a := api.API{
		Version:      "v1.2.3",
		Address:      "localhost:9997",
		ReadTimeout:  conf.Duration(10 * time.Second),
		WriteTimeout: conf.Duration(10 * time.Second),
		AuthManager:  test.NilAuthManager,
		Parent:       &testParent{},
	}
	err := a.Initialize()
	require.NoError(t, err)
	defer a.Close()

	// disable automatic decompression and redirects in order to check them
	tr := &http.Transport{DisableCompression: true}
	defer tr.CloseIdleConnections()
	hc := &http.Client{
		Transport: tr,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	get := func(ur string, header http.Header) (int, http.Header, []byte) {
		req, err2 := http.NewRequest(http.MethodGet, ur, nil)
		require.NoError(t, err2)
		req.Header = header

		res, err2 := hc.Do(req)
		require.NoError(t, err2)
		defer res.Body.Close()

		body, err2 := io.ReadAll(res.Body)
		require.NoError(t, err2)
		return res.StatusCode, res.Header, body
	}

	// root redirects to the page
	status, header, _ := get("http://localhost:9997/", http.Header{})
	require.Equal(t, http.StatusFound, status)
	require.Equal(t, "/ui/", header.Get("Location"))

	// plain
	status, header, plain := get("http://localhost:9997/ui/", http.Header{})
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "text/html; charset=utf-8", header.Get("Content-Type"))
	require.Equal(t, "", header.Get("Content-Encoding"))
	require.NotEmpty(t, header.Get("Content-Security-Policy"))
	require.Contains(t, string(plain), "<title>MediaMTX</title>")

	etag := header.Get("ETag")
	require.NotEmpty(t, etag)

	// compressed
	status, header, compressed := get("http://localhost:9997/ui/", http.Header{"Accept-Encoding": {"gzip"}})
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "gzip", header.Get("Content-Encoding"))
	require.Less(t, len(compressed), len(plain)/2)

	// not downloaded again when unchanged
	status, _, body := get("http://localhost:9997/ui/", http.Header{"If-None-Match": {etag}})
	require.Equal(t, http.StatusNotModified, status)
	require.Empty(t, body)

	// API responses are compressed on request
	status, header, body = get("http://localhost:9997/v3/info", http.Header{"Accept-Encoding": {"gzip"}})
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "gzip", header.Get("Content-Encoding"))

	_, err = gzip.NewReader(io.NopCloser(&sliceReader{b: body}))
	require.NoError(t, err)

	status, header, body = get("http://localhost:9997/v3/info", http.Header{})
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "", header.Get("Content-Encoding"))
	require.Contains(t, string(body), `"version":"v1.2.3"`)
}

type sliceReader struct {
	b []byte
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(p, r.b)
	r.b = r.b[n:]
	return n, nil
}
