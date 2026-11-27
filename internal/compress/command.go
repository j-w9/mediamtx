// Package compress contains utilities to re-encode streams with FFmpeg.
package compress

import (
	"strconv"
	"strings"

	"github.com/bluenviron/mediamtx/internal/conf"
)

// SVT-AV1 uses numeric presets. Map them to the names used by x264 and x265.
var svtAV1Presets = map[string]string{
	"ultrafast": "12",
	"superfast": "11",
	"veryfast":  "10",
	"faster":    "9",
	"fast":      "8",
	"medium":    "7",
	"slow":      "6",
	"slower":    "5",
	"veryslow":  "4",
}

// IsOutputPath returns whether a path with the given name
// is the output of a compression, and therefore must not be compressed again.
func IsOutputPath(pathName string, cnf *conf.Path) bool {
	return strings.HasSuffix(pathName, cnf.CompressPathSuffix)
}

// OutputPath returns the name of the path in which the compressed stream is published.
func OutputPath(pathName string, cnf *conf.Path) string {
	return pathName + cnf.CompressPathSuffix
}

// doubleBitrate doubles a bitrate expressed in the FFmpeg format (i.e. "1000k").
func doubleBitrate(v string) string {
	unit := ""
	if last := v[len(v)-1]; last < '0' || last > '9' {
		unit = v[len(v)-1:]
		v = v[:len(v)-1]
	}

	n, _ := strconv.ParseUint(v, 10, 64)
	return strconv.FormatUint(n*2, 10) + unit
}

// Command returns the FFmpeg command that reads the stream of a path from the local RTSP server,
// re-encodes it and publishes the result to the output path.
// The command contains the ${RTSP_PORT} variable, that is filled by the external command runner.
func Command(cnf *conf.Path, pathName string) string {
	args := []string{
		"ffmpeg",
		"-nostdin",
		"-hide_banner",
		"-loglevel", "warning",
		"-rtsp_transport", "tcp",
		"-i", "rtsp://127.0.0.1:${RTSP_PORT}/" + pathName,
		// first video track, every audio track (if present)
		"-map", "0:v:0",
		"-map", "0:a?",
	}

	switch cnf.CompressVideoCodec {
	case "av1":
		args = append(args,
			"-c:v", "libsvtav1",
			"-preset", svtAV1Presets[cnf.CompressPreset],
			// pred-struct=1 is the low-delay mode, without frame reordering.
			// It requires constant bitrate rate control (rc=2).
			"-svtav1-params", "pred-struct=1:rc=2",
			"-pix_fmt", "yuv420p",
			"-b:v", cnf.CompressVideoBitrate,
			// FFmpeg marks the AV1 RTP packetizer as experimental
			"-strict", "experimental",
		)

	case "h265":
		args = append(args,
			"-c:v", "libx265",
			"-x265-params", "log-level=warning",
		)

	default:
		args = append(args, "-c:v", "libx264")
	}

	if cnf.CompressVideoCodec != "av1" {
		args = append(args,
			"-preset", cnf.CompressPreset,
			// zerolatency disables B-frames, that are not supported by WebRTC,
			// and removes the encoder lookahead delay.
			"-tune", "zerolatency",
			"-pix_fmt", "yuv420p",
			"-b:v", cnf.CompressVideoBitrate,
			"-maxrate", cnf.CompressVideoBitrate,
			"-bufsize", doubleBitrate(cnf.CompressVideoBitrate),
		)
	}

	// a key frame every 2 seconds, regardless of the frame rate,
	// in order to allow readers to start decoding quickly.
	args = append(args, "-force_key_frames", "'expr:gte(t,n_forced*2)'")

	if cnf.CompressVideoHeight != 0 {
		// -2 keeps the aspect ratio and rounds the width to an even number
		args = append(args, "-vf", "scale=-2:"+strconv.Itoa(cnf.CompressVideoHeight))
	}

	if cnf.CompressVideoFPS != 0 {
		args = append(args, "-r", strconv.Itoa(cnf.CompressVideoFPS))
	}

	if cnf.CompressAudioBitrate != "" {
		args = append(args, "-c:a", "libopus", "-b:a", cnf.CompressAudioBitrate)
	} else {
		args = append(args, "-c:a", "copy")
	}

	args = append(args,
		"-max_muxing_queue_size", "1024",
		"-f", "rtsp",
		"-rtsp_transport", "tcp",
		"rtsp://127.0.0.1:${RTSP_PORT}/"+OutputPath(pathName, cnf),
	)

	return strings.Join(args, " ")
}

// OnDemandCommand returns the command to run when a path is requested by a reader,
// in case the path is the output of an on-demand compression.
// It returns an empty string otherwise.
func OnDemandCommand(pathConfs map[string]*conf.Path, outConf *conf.Path, outName string) string {
	// do not interfere with paths that have their own source
	if outConf.Source != "publisher" || outConf.RunOnDemand != "" {
		return ""
	}

	for _, cnf := range pathConfs {
		if !cnf.Compress || !cnf.CompressOnDemand ||
			len(outName) <= len(cnf.CompressPathSuffix) ||
			!strings.HasSuffix(outName, cnf.CompressPathSuffix) {
			continue
		}

		inName := outName[:len(outName)-len(cnf.CompressPathSuffix)]

		inConf, _, err := conf.FindPathConf(pathConfs, inName)
		if err == nil && inConf == cnf {
			return Command(cnf, inName)
		}
	}

	return ""
}
