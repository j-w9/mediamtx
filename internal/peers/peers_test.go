package peers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
)

func fakePeer(t *testing.T, srt bool) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		if user != "link" || pass != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		var out any

		switch r.URL.Path {
		case "/v3/info":
			out = map[string]any{"version": "v1.2.3"}

		case "/v3/config/global/get":
			out = map[string]any{
				"rtsp": true, "rtspAddress": ":8554", "rtspEncryption": "no",
				"srt": srt, "srtAddress": ":8890",
			}

		case "/v3/paths/list":
			out = map[string]any{"items": []map[string]any{
				{"name": "flight", "confName": "flight", "available": true, "tracks": []string{"H264", "KLV"}},
				{"name": "flight_compressed", "confName": "all_others", "available": false, "tracks": []string{}},
				{"name": "gate", "confName": "all_others", "available": true, "tracks": []string{"H265"}},
			}}

		case "/v3/config/paths/list":
			out = map[string]any{"items": []map[string]any{
				{"name": "flight", "compress": true, "compressPathSuffix": "_compressed"},
				{"name": "all_others", "compress": false, "compressPathSuffix": "_compressed"},
			}}

		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}

		json.NewEncoder(w).Encode(out) //nolint:errcheck
	}))
}

func TestList(t *testing.T) {
	withSRT := fakePeer(t, true)
	defer withSRT.Close()

	rtspOnly := fakePeer(t, false)
	defer rtspOnly.Close()

	site2 := conf.Peer{Name: "site2", URL: withSRT.URL, User: "link", Pass: "secret"}
	site3 := conf.Peer{Name: "site3", URL: rtspOnly.URL, User: "link", Pass: "secret"}

	list := List(context.Background(), conf.Peers{
		site2,
		site3,
		{Name: "wrongpass", URL: withSRT.URL, User: "link", Pass: "nope"},
		{Name: "down", URL: "http://127.0.0.1:1"},
	}, map[string]*conf.Path{
		"site2/flight": {
			Source:   "srt://127.0.0.1:8890?streamid=read:flight_compressed:link:secret",
			PeerName: "site2",
			PeerFeed: "flight",
			PeerMode: "auto",
		},
	})

	require.Len(t, list.Items, 4)

	p := list.Items[0]
	require.True(t, p.Reachable)
	require.Equal(t, "v1.2.3", p.Version)
	// nothing that is listed contains credentials
	require.Equal(t, "rtsp://127.0.0.1:8554/", p.PushBase)
	// the compressed copy is not listed by itself
	require.Equal(t, []defs.APIPeerFeed{
		{
			Name:          "flight",
			Available:     true,
			Tracks:        []string{"H264", "KLV"},
			Compressed:    true,
			PulledBy:      "site2/flight",
			PulledMode:    "auto",
			PulledVariant: "compressed",
		},
		{
			Name:      "gate",
			Available: true,
			Tracks:    []string{"H265"},
		},
	}, p.Feeds)

	require.Equal(t, "", list.Items[1].Feeds[0].PulledBy)

	require.False(t, list.Items[2].Reachable)
	require.Equal(t, "the peer refused the credentials", list.Items[2].Error)

	require.False(t, list.Items[3].Reachable)
	require.NotEmpty(t, list.Items[3].Error)
	require.Equal(t, []defs.APIPeerFeed{}, list.Items[3].Feeds)

	// addresses, with credentials, are resolved on request
	s, err := Resolve(context.Background(), &site2, "flight")
	require.NoError(t, err)
	require.Equal(t, &Sources{
		Original:   "srt://127.0.0.1:8890?streamid=read:flight:link:secret",
		Compressed: "srt://127.0.0.1:8890?streamid=read:flight_compressed:link:secret",
	}, s)

	// without SRT, streams are pulled with RTSP
	s, err = Resolve(context.Background(), &site3, "gate")
	require.NoError(t, err)
	require.Equal(t, &Sources{Original: "rtsp://link:secret@127.0.0.1:8554/gate"}, s)

	_, err = Resolve(context.Background(), &site2, "missing")
	require.ErrorContains(t, err, "no stream named")
}
