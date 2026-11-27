# Re-encoding

To change the format, codec or compression of a stream, use _FFmpeg_ or _GStreamer_ together with _MediaMTX_. For instance, to re-encode an existing stream, that is available in the `/original` path, and publish the resulting stream in the `/compressed` path, edit `mediamtx.yml` and replace everything inside section `paths` with the following content:

```yml
paths:
  compressed:
  original:
    runOnAvailable: >
      ffmpeg -i rtsp://localhost:$RTSP_PORT/$MTX_PATH
        -c:v libx264 -pix_fmt yuv420p -preset ultrafast -b:v 600k
        -max_muxing_queue_size 1024 -f rtsp rtsp://localhost:$RTSP_PORT/compressed
    runOnAvailableRestart: yes
```

## Built-in compression

The server can run _FFmpeg_ by itself in order to decrease the bitrate of a stream. Enable the `compress` setting on a path:

```yml
paths:
  cam:
    source: rtsp://my-camera/stream
    compress: yes
    compressVideoCodec: h265
    compressVideoBitrate: 800k
    compressVideoHeight: 720
  all_others:
```

The original stream stays available in `/cam`, while the compressed one is available in `/cam_compressed`, through every protocol supported by the server.

By default, compression is performed on demand: _FFmpeg_ is started when a reader requests `/cam_compressed` and is stopped 10 seconds after the last reader has left, so that streams that are not watched do not consume CPU. To compress a stream for as long as it is available, for instance in order to record or forward the compressed version, set `compressOnDemand: no`.

Requirements:

- _FFmpeg_ must be installed and available in `PATH`, with the encoder of the chosen codec (`libx264`, `libx265` or `libsvtav1`).
- The RTSP server must be enabled, since it is used to exchange streams with _FFmpeg_, and local connections must be allowed to read and publish.
- The compressed path must be allowed by the configuration. This is the case when `all_others` is present.

Re-encoding is the only way to significantly decrease the size of a video stream. Applying a general-purpose compression algorithm on a stream that is already encoded with H264, H265 or AV1 does not provide any benefit, since these codecs already remove redundancy from data.
