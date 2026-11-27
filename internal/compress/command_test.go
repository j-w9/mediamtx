package compress_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bluenviron/mediamtx/internal/compress"
	"github.com/bluenviron/mediamtx/internal/conf"
)

func TestCommand(t *testing.T) {
	for _, ca := range []struct {
		name string
		conf conf.Path
		cmd  string
	}{
		{
			"h264",
			conf.Path{
				CompressVideoCodec:   "h264",
				CompressVideoBitrate: "1000k",
				CompressPreset:       "veryfast",
				CompressPathSuffix:   "_compressed",
			},
			"ffmpeg -nostdin -hide_banner -loglevel warning -rtsp_transport tcp" +
				" -i rtsp://127.0.0.1:${RTSP_PORT}/my/path?mtx_internal=1 -map 0:v:0 -map 0:a?" +
				" -c:v libx264 -preset veryfast -tune zerolatency -pix_fmt yuv420p" +
				" -b:v 1000k -maxrate 1000k -bufsize 2000k" +
				" -force_key_frames 'expr:gte(t,n_forced*2)'" +
				" -c:a copy -max_muxing_queue_size 1024 -f rtsp -rtsp_transport tcp" +
				" rtsp://127.0.0.1:${RTSP_PORT}/my/path_compressed",
		},
		{
			"h265 scaled",
			conf.Path{
				CompressVideoCodec:   "h265",
				CompressVideoBitrate: "2M",
				CompressVideoHeight:  480,
				CompressVideoFPS:     15,
				CompressPreset:       "fast",
				CompressAudioBitrate: "64k",
				CompressPathSuffix:   "/low",
			},
			"ffmpeg -nostdin -hide_banner -loglevel warning -rtsp_transport tcp" +
				" -i rtsp://127.0.0.1:${RTSP_PORT}/my/path?mtx_internal=1 -map 0:v:0 -map 0:a?" +
				" -c:v libx265 -x265-params log-level=warning" +
				" -preset fast -tune zerolatency -pix_fmt yuv420p" +
				" -b:v 2M -maxrate 2M -bufsize 4M" +
				" -force_key_frames 'expr:gte(t,n_forced*2)'" +
				" -vf scale=-2:480 -r 15" +
				" -c:a libopus -b:a 64k -max_muxing_queue_size 1024 -f rtsp -rtsp_transport tcp" +
				" rtsp://127.0.0.1:${RTSP_PORT}/my/path/low",
		},
		{
			"av1",
			conf.Path{
				CompressVideoCodec:   "av1",
				CompressVideoBitrate: "500000",
				CompressPreset:       "veryfast",
				CompressPathSuffix:   "_compressed",
			},
			"ffmpeg -nostdin -hide_banner -loglevel warning -rtsp_transport tcp" +
				" -i rtsp://127.0.0.1:${RTSP_PORT}/my/path?mtx_internal=1 -map 0:v:0 -map 0:a?" +
				" -c:v libsvtav1 -preset 10 -svtav1-params pred-struct=1:rc=2 -pix_fmt yuv420p" +
				" -b:v 500000 -strict experimental" +
				" -force_key_frames 'expr:gte(t,n_forced*2)'" +
				" -c:a copy -max_muxing_queue_size 1024 -f rtsp -rtsp_transport tcp" +
				" rtsp://127.0.0.1:${RTSP_PORT}/my/path_compressed",
		},
	} {
		t.Run(ca.name, func(t *testing.T) {
			require.Equal(t, ca.cmd, compress.Command(&ca.conf, "my/path"))
		})
	}
}

func TestOnDemandCommand(t *testing.T) {
	cam := &conf.Path{
		Name:                 "cam",
		Source:               "publisher",
		Compress:             true,
		CompressOnDemand:     true,
		CompressVideoCodec:   "h264",
		CompressVideoBitrate: "1000k",
		CompressPreset:       "veryfast",
		CompressPathSuffix:   "_compressed",
	}

	alwaysOn := &conf.Path{
		Name:               "alwayson",
		Source:             "publisher",
		Compress:           true,
		CompressPathSuffix: "_compressed",
	}

	plain := &conf.Path{
		Name:               "plain",
		Source:             "publisher",
		CompressPathSuffix: "_compressed",
	}

	out := &conf.Path{
		Source:             "publisher",
		CompressPathSuffix: "_compressed",
	}

	pathConfs := map[string]*conf.Path{
		"cam":      cam,
		"alwayson": alwaysOn,
		"plain":    plain,
	}

	// output of an on-demand compression
	require.Equal(t, compress.Command(cam, "cam"),
		compress.OnDemandCommand(pathConfs, out, "cam_compressed"))

	// output of an always-on compression: the command is run by the input path
	require.Equal(t, "", compress.OnDemandCommand(pathConfs, out, "alwayson_compressed"))

	// input path is not compressed
	require.Equal(t, "", compress.OnDemandCommand(pathConfs, out, "plain_compressed"))

	// input path does not exist
	require.Equal(t, "", compress.OnDemandCommand(pathConfs, out, "other_compressed"))

	// not an output path
	require.Equal(t, "", compress.OnDemandCommand(pathConfs, cam, "cam"))
	require.Equal(t, "", compress.OnDemandCommand(pathConfs, out, "_compressed"))

	// output path has its own source
	require.Equal(t, "", compress.OnDemandCommand(pathConfs,
		&conf.Path{Source: "rtsp://localhost/stream"}, "cam_compressed"))
	require.Equal(t, "", compress.OnDemandCommand(pathConfs,
		&conf.Path{Source: "publisher", RunOnDemand: "mycommand"}, "cam_compressed"))
}

func TestIsOutputPath(t *testing.T) {
	cnf := &conf.Path{CompressPathSuffix: "_compressed"}
	require.True(t, compress.IsOutputPath("cam_compressed", cnf))
	require.False(t, compress.IsOutputPath("cam", cnf))
	require.Equal(t, "cam_compressed", compress.OutputPath("cam", cnf))
}
