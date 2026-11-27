<h1 align="center">
  <a href="https://mediamtx.org">
    <img src="logo.png" alt="MediaMTX">
  </a>
</h1>

<br>

This is a fork of [MediaMTX](https://github.com/bluenviron/mediamtx), the live media server and media proxy that allows to publish, read, proxy, record and playback real-time video and audio streams. Everything _MediaMTX_ does is still here and is described in the [upstream documentation](https://mediamtx.org/docs/kickoff/introduction).

The fork adds what is needed to distribute video from drones, aircraft sensors and cameras over constrained networks: a web UI, compression, metadata handling, delivery to TAK, discovery of sources, sharing between servers and object detection. The additions live in the `custom` branch.

<h3>What the fork adds</h3>

- **Web UI**, served by the Control API at `/ui/`: live feeds with bitrate, a video wall, a map, recordings, peers and settings. Changes made in the UI are saved in a file next to the configuration file (`mediamtx.api.json`) and survive restarts; the configuration file itself is never modified.
- **Compression**: a stream is re-encoded with _FFmpeg_ into a second path (`<name>_compressed`) in H264, H265 or AV1, by default only while someone is reading it. The original stays available.
- **KLV metadata** (MISB ST 0601): streams that carry it are decoded automatically. The position of the sensor, of the area it is looking at and its footprint are shown on the map and returned by the API. Metadata can be repaired before being forwarded (checksum, item order, timestamp, version), and is carried into compressed copies.
- **TAK**: streams that have a position are announced to ATAK, WinTAK and iTAK clients and to TAK servers with Cursor-on-Target messages, over UDP, TCP or TLS. Cameras without metadata can be given a fixed position.
- **Discovery** of video sources: streams announced with SAP, MPEG-TS streams on multicast groups, ONVIF cameras, streams announced by TAK. Sources are listed, and added with one click.
- **Peers**: a server lists the streams of other servers and pulls them, with SRT when available. In automatic mode it pulls the original and switches to the compressed copy when the link loses packets.
- **Restreaming** from the UI, built on the upstream forward feature.
- **Object detection** with YOLO-family models in ONNX format, run on the machine (CPU or GPU) or on an inference server that speaks the KServe v2 protocol. Results are shown over the video and can be used to filter feeds.
- **Users** can be managed from the UI.

<h3>Requirements</h3>

The server is still a single executable. Some of the additions need other software on the machine, only when they are used:

| Feature                          | Needs                                                                |
| -------------------------------- | -------------------------------------------------------------------- |
| Compression                      | _FFmpeg_ with `libx264`, `libx265` or `libsvtav1`                    |
| Object detection                 | _FFmpeg_, Python 3 with `numpy`                                      |
| Object detection on this machine | the `onnxruntime` Python package (`onnxruntime-gpu` for NVIDIA GPUs) |
| Compression, detection, TAK      | the RTSP server enabled (it is by default)                           |

<h3>Build and run</h3>

Go 1.27 or later is required.

```sh
go generate ./...
go build .
MTX_API=yes ./mediamtx
```

Then open http://localhost:9997/ui/. By default the Control API, and therefore the UI, is reachable from the machine itself only; see [Authentication](https://mediamtx.org/docs/features/authentication) and the Users section of the UI to open it to others.

<h3>Configuration</h3>

Every addition is configured in `mediamtx.yml`, where each setting is documented, or from the UI. For instance:

```yml
api: yes

# announce streams to TAK clients on the local network
tak: yes

# look for video sources, including MPEG-TS sent to this multicast group
discovery: yes
discoveryMulticast: [239.1.1.1:5000]

# another server to share video with
peers:
  - name: site2
    url: http://192.168.1.10:9997
    user: link
    pass: secret

paths:
  uav1:
    source: udp+mpegts://239.1.1.1:5000
    compress: yes
    compressVideoCodec: h265
    compressVideoBitrate: 800k
    klvRepair: yes
    detect: yes
    detectModel: yolov8n.onnx
  all_others:
```

Detection models are `.onnx` files placed in the `models` directory. The interpreter used to run them is set with `detectPython`.

The API endpoints added by the fork are `/v3/discovery/list`, `/v3/peers/list`, `/v3/peers/pull` and `/v3/detect/models/list`. Paths returned by `/v3/paths/list` have two more fields, `telemetry` and `detection`.

<h3>What has and has not been tested</h3>

Tested: compression in the three codecs, KLV decoding against a public sample clip, KLV repair end to end, TAK messages against the [goatak](https://github.com/kdudkov/goatak) server and against the attribute names used by the ATAK source code, TAK over multicast and over TLS with certificates, discovery of SAP, multicast and TAK sources and of a simulated ONVIF camera, sharing and automatic switching between two servers with packet loss on the link, detection with YOLOX, YOLOv5, YOLOv8 and YOLO11 models on CPU and on Apple GPUs, and against NVIDIA Triton and MLServer.

Not tested: a real ATAK, WinTAK or iTAK client opening the video, a real ONVIF camera, detection on NVIDIA GPUs.

Known limits: changes to users made through the API are saved, but edits to the same users in the configuration file are then overridden until `mediamtx.api.json` is deleted; a peer must give this server a user that can use its API and read its streams; the UI has not been checked on screens narrower than 500 pixels.

<h3>Upstream</h3>

Features, documentation and releases of _MediaMTX_ are at [mediamtx.org](https://mediamtx.org) and [bluenviron/mediamtx](https://github.com/bluenviron/mediamtx). The upstream license applies.
