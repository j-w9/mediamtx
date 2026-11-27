#!/usr/bin/env python3
"""Object detection worker of MediaMTX.

It loads a detection model once and runs it on the streams it is told to analyze.
Streams are added and removed with JSON commands, one per line, on standard input:

    {"cmd": "add", "id": "1", "source": "rtsp://...", "fps": "2", "conf": 0.4, "keyframes": false}
    {"cmd": "remove", "id": "1"}

Each stream is read with FFmpeg. Results are printed on standard output, one JSON document per line:

    {"type": "info", "classes": [...], "device": "...", "input": [w, h]}
    {"type": "frame", "id": "1", "t": 1700000000.0, "ms": 12.3, "w": 1280, "h": 720,
     "objects": [{"c": 0, "s": 0.91, "b": [x, y, w, h]}]}
    {"type": "error", "id": "1", "error": "..."}

Boxes are relative to the frame, between 0 and 1.

The model is either an ONNX file that is run here with ONNX Runtime, or a model
hosted by a server that speaks the KServe v2 inference protocol (NVIDIA Triton,
OpenVINO Model Server, ...). In both cases the model must be a detector of the
YOLO family, whose output layouts are recognized automatically.

Messages meant for humans go to standard error.
"""

import argparse
import ast
import json
import os
import signal
import subprocess
import sys
import threading
import time
import urllib.error
import urllib.request

import numpy as np

COCO = [
    "person", "bicycle", "car", "motorcycle", "airplane", "bus", "train", "truck", "boat",
    "traffic light", "fire hydrant", "stop sign", "parking meter", "bench", "bird", "cat", "dog",
    "horse", "sheep", "cow", "elephant", "bear", "zebra", "giraffe", "backpack", "umbrella",
    "handbag", "tie", "suitcase", "frisbee", "skis", "snowboard", "sports ball", "kite",
    "baseball bat", "baseball glove", "skateboard", "surfboard", "tennis racket", "bottle",
    "wine glass", "cup", "fork", "knife", "spoon", "bowl", "banana", "apple", "sandwich", "orange",
    "broccoli", "carrot", "hot dog", "pizza", "donut", "cake", "chair", "couch", "potted plant",
    "bed", "dining table", "toilet", "tv", "laptop", "mouse", "remote", "keyboard", "cell phone",
    "microwave", "oven", "toaster", "sink", "refrigerator", "book", "clock", "vase", "scissors",
    "teddy bear", "hair drier", "toothbrush",
]

DEFAULT_SIZE = 640


def log(msg):
    print(msg, file=sys.stderr, flush=True)


emit_lock = threading.Lock()


def emit(doc):
    line = json.dumps(doc, separators=(",", ":")) + "\n"
    with emit_lock:
        sys.stdout.write(line)
        sys.stdout.flush()


def parse_names(raw):
    """Parse class names stored in the metadata of a model.

    Ultralytics stores a Python dictionary ("{0: 'person', 1: 'bicycle'}"),
    other exporters store a JSON list or one name per line.
    """
    if not raw:
        return None
    raw = raw.strip()
    try:
        value = ast.literal_eval(raw)
    except (ValueError, SyntaxError):
        try:
            value = json.loads(raw)
        except ValueError:
            value = [line.strip() for line in raw.replace(",", "\n").split("\n") if line.strip()]
    if isinstance(value, dict):
        return [str(value[k]) for k in sorted(value, key=lambda k: int(k))]
    if isinstance(value, (list, tuple)):
        return [str(v) for v in value]
    return None


def names_from_sidecar(model_path):
    """Read class names from a text file next to the model, one name per line."""
    base = os.path.splitext(model_path)[0]
    for candidate in (base + ".names", base + ".txt", base + ".labels"):
        if os.path.isfile(candidate):
            with open(candidate, encoding="utf-8") as f:
                return [line.strip() for line in f if line.strip()]
    return None


def fill_names(names, count):
    if names and len(names) >= count:
        return names[:count]
    if count == len(COCO):
        return COCO
    return ["class " + str(i) for i in range(count)]


def grid_count(width, height, strides=(8, 16, 32)):
    return sum((width // s) * (height // s) for s in strides)


class Layout:
    """Describes how to turn the output of a model into boxes."""

    def __init__(self, kind, classes):
        self.kind = kind  # "v8", "v5" or "yolox"
        self.classes = classes


def guess_layout(shape, width, height, forced):
    """Recognize the output layout of a YOLO model from the shape of its output."""
    dims = [d for d in shape if isinstance(d, int) and d > 0]
    if len(shape) != 3:
        raise ValueError("unsupported model: output has shape %s, expected 3 dimensions" % (shape,))

    a, b = shape[1], shape[2]
    grids = grid_count(width, height)

    if forced and forced != "auto":
        if forced == "v8":
            return Layout("v8", (a if (isinstance(a, int) and a < b) else b) - 4)
        return Layout(forced, b - 5)

    # YOLOv8 and later: [1, 4 + classes, boxes], no objectness score
    if isinstance(a, int) and isinstance(b, int) and a < b:
        return Layout("v8", a - 4)

    if not isinstance(b, int) or b < 6:
        raise ValueError("unsupported model: output has shape %s" % (shape,))

    # [1, boxes, 5 + classes]. YOLOX has one box per grid cell and needs decoding,
    # YOLOv5 and YOLOv7 have three anchors per cell and are already decoded.
    if a == grids or (not isinstance(a, int) and len(dims) == 1):
        return Layout("yolox", b - 5)
    return Layout("v5", b - 5)


def decode(output, layout, width, height, conf_thres):
    """Return boxes as (x1, y1, x2, y2) in model input pixels, scores and class indexes."""
    out = np.asarray(output, dtype=np.float32)
    if out.ndim == 3:
        out = out[0]

    if layout.kind == "v8":
        if out.shape[0] < out.shape[1]:
            out = out.T
        boxes = out[:, :4]
        cls = out[:, 4:]
        scores = cls.max(axis=1)
        ids = cls.argmax(axis=1)
    else:
        boxes = out[:, :4].copy()
        if layout.kind == "yolox":
            grids = []
            strides = []
            for s in (8, 16, 32):
                gw, gh = width // s, height // s
                xv, yv = np.meshgrid(np.arange(gw), np.arange(gh))
                grids.append(np.stack((xv, yv), 2).reshape(-1, 2))
                strides.append(np.full((gw * gh, 1), s))
            grids = np.concatenate(grids, 0)
            strides = np.concatenate(strides, 0)
            boxes[:, :2] = (boxes[:, :2] + grids) * strides
            boxes[:, 2:4] = np.exp(boxes[:, 2:4]) * strides
        cls = out[:, 5:] * out[:, 4:5]
        scores = cls.max(axis=1)
        ids = cls.argmax(axis=1)

    keep = scores >= conf_thres
    boxes, scores, ids = boxes[keep], scores[keep], ids[keep]

    xyxy = np.empty_like(boxes)
    xyxy[:, 0] = boxes[:, 0] - boxes[:, 2] / 2
    xyxy[:, 1] = boxes[:, 1] - boxes[:, 3] / 2
    xyxy[:, 2] = boxes[:, 0] + boxes[:, 2] / 2
    xyxy[:, 3] = boxes[:, 1] + boxes[:, 3] / 2
    return xyxy, scores, ids


def nms(boxes, scores, ids, iou_thres=0.45, limit=100):
    """Non-maximum suppression, performed separately for each class."""
    if len(boxes) == 0:
        return []
    # move boxes of different classes apart so that they never overlap
    offset = ids.astype(np.float32)[:, None] * 8192.0
    b = boxes + offset
    x1, y1, x2, y2 = b[:, 0], b[:, 1], b[:, 2], b[:, 3]
    areas = (x2 - x1) * (y2 - y1)
    order = scores.argsort()[::-1]
    keep = []
    while order.size > 0 and len(keep) < limit:
        i = order[0]
        keep.append(int(i))
        xx1 = np.maximum(x1[i], x1[order[1:]])
        yy1 = np.maximum(y1[i], y1[order[1:]])
        xx2 = np.minimum(x2[i], x2[order[1:]])
        yy2 = np.minimum(y2[i], y2[order[1:]])
        inter = np.maximum(0.0, xx2 - xx1) * np.maximum(0.0, yy2 - yy1)
        iou = inter / (areas[i] + areas[order[1:]] - inter + 1e-9)
        order = order[1:][iou <= iou_thres]
    return keep


class LocalModel:
    """A model run in this process with ONNX Runtime."""

    def __init__(self, path, device):
        try:
            import onnxruntime as ort
        except ImportError:
            raise RuntimeError(
                "the onnxruntime Python package is not installed. Install it with "
                "'pip install onnxruntime' (or 'onnxruntime-gpu' for NVIDIA GPUs)") from None

        available = ort.get_available_providers()
        gpu = [p for p in (
            "CUDAExecutionProvider", "CoreMLExecutionProvider", "DmlExecutionProvider",
            "OpenVINOExecutionProvider", "ROCMExecutionProvider", "MIGraphXExecutionProvider",
        ) if p in available]

        if device == "cpu":
            providers = ["CPUExecutionProvider"]
        elif device == "gpu":
            if not gpu:
                raise RuntimeError(
                    "no GPU is available to ONNX Runtime (providers: %s). "
                    "Install a GPU build of onnxruntime or set the device to cpu" % ", ".join(available))
            providers = gpu + ["CPUExecutionProvider"]
        else:
            providers = gpu + ["CPUExecutionProvider"]

        options = ort.SessionOptions()
        options.log_severity_level = 3
        self.session = ort.InferenceSession(path, sess_options=options, providers=providers)
        self.device = self.session.get_providers()[0].replace("ExecutionProvider", "")

        inp = self.session.get_inputs()[0]
        self.input_name = inp.name
        self.input_type = inp.type
        shape = inp.shape
        self.height = shape[2] if isinstance(shape[2], int) and shape[2] > 0 else DEFAULT_SIZE
        self.width = shape[3] if isinstance(shape[3], int) and shape[3] > 0 else DEFAULT_SIZE
        self.output_shape = self.session.get_outputs()[0].shape

        meta = self.session.get_modelmeta().custom_metadata_map or {}
        self.names = parse_names(meta.get("names")) or names_from_sidecar(path)

    def infer(self, tensor):
        if "float16" in self.input_type:
            tensor = tensor.astype(np.float16)
        return self.session.run(None, {self.input_name: tensor})[0]


class RemoteModel:
    """A model hosted by a server that speaks the KServe v2 inference protocol."""

    def __init__(self, server, name, names_file=""):
        self.base = server.rstrip("/") + "/v2/models/" + urllib.request.quote(name, safe="")
        self.device = "remote"
        # tensors are sent in binary form, which is compact, as long as the server accepts it
        self.binary = True

        meta = self._get_json(self.base)
        inp = meta["inputs"][0]
        out = meta["outputs"][0]
        self.input_name = inp["name"]
        self.input_datatype = inp.get("datatype", "FP32")
        self.output_name = out["name"]

        shape = [int(d) for d in inp["shape"]]
        if len(shape) == 3:  # the batch dimension is implicit
            shape = [1] + shape
        self.batched = len(inp["shape"]) == 4 or bool(meta.get("max_batch_size"))
        self.height = shape[2] if shape[2] > 0 else DEFAULT_SIZE
        self.width = shape[3] if shape[3] > 0 else DEFAULT_SIZE

        oshape = [int(d) for d in out["shape"]]
        if len(oshape) == 2:
            oshape = [1] + oshape
        self.output_shape = [d if d > 0 else "n" for d in oshape]

        # class names are not part of the protocol. They are searched in the parameters
        # of the model metadata, in the model configuration of Triton,
        # and in a text file stored on this machine.
        self.names = None
        params = meta.get("parameters") or {}
        if isinstance(params.get("names"), str):
            self.names = parse_names(params["names"])
        if not self.names:
            try:
                config = self._get_json(self.base + "/config")
                params = config.get("parameters", {})
                if "names" in params:
                    self.names = parse_names(params["names"].get("string_value"))
            except (urllib.error.URLError, ValueError, KeyError, AttributeError):
                pass
        if not self.names and names_file and os.path.isfile(names_file):
            with open(names_file, encoding="utf-8") as f:
                self.names = [line.strip() for line in f if line.strip()]

    @staticmethod
    def _get_json(url):
        with urllib.request.urlopen(url, timeout=10) as res:
            return json.loads(res.read().decode("utf-8"))

    def infer(self, tensor):
        if self.binary:
            try:
                return self._infer_binary(tensor)
            except urllib.error.HTTPError as e:
                # the binary extension is optional: servers that do not implement it
                # (i.e. MLServer) reject the request, and get plain JSON from now on.
                log("the server refused binary tensors (status %d), switching to JSON" % e.code)
                self.binary = False
        return self._infer_json(tensor)

    def _infer_json(self, tensor):
        body = json.dumps({
            "inputs": [{
                "name": self.input_name,
                "shape": list(tensor.shape),
                "datatype": "FP32",
                # 4 decimals are more than enough for pixels and keep the request small
                "data": np.round(tensor.astype(np.float32), 4).ravel().tolist(),
            }],
            "outputs": [{"name": self.output_name}],
        }).encode("utf-8")

        req = urllib.request.Request(self.base + "/infer", data=body, method="POST",
                                     headers={"Content-Type": "application/json"})
        with urllib.request.urlopen(req, timeout=30) as res:
            doc = json.loads(res.read().decode("utf-8"))

        out = doc["outputs"][0]
        return np.array(out["data"], dtype=np.float32).reshape(out["shape"])

    def _infer_binary(self, tensor):
        dtype = np.float16 if self.input_datatype == "FP16" else np.float32
        data = np.ascontiguousarray(tensor.astype(dtype)).tobytes()
        header = json.dumps({
            "inputs": [{
                "name": self.input_name,
                "shape": list(tensor.shape),
                "datatype": self.input_datatype,
                "parameters": {"binary_data_size": len(data)},
            }],
            "outputs": [{"name": self.output_name, "parameters": {"binary_data": True}}],
        }).encode("utf-8")

        req = urllib.request.Request(self.base + "/infer", data=header + data, method="POST", headers={
            "Content-Type": "application/octet-stream",
            "Inference-Header-Content-Length": str(len(header)),
        })
        with urllib.request.urlopen(req, timeout=30) as res:
            body = res.read()
            header_len = res.headers.get("Inference-Header-Content-Length")

        if header_len is None:  # the server replied with JSON only
            doc = json.loads(body.decode("utf-8"))
            out = doc["outputs"][0]
            return np.array(out["data"], dtype=np.float32).reshape(out["shape"])

        header_len = int(header_len)
        doc = json.loads(body[:header_len].decode("utf-8"))
        out = doc["outputs"][0]
        dtype = {"FP16": np.float16, "FP64": np.float64}.get(out.get("datatype"), np.float32)
        return np.frombuffer(body[header_len:], dtype=dtype).reshape(out["shape"]).astype(np.float32)


def load_model(args):
    if args.server:
        return RemoteModel(args.server, args.model, args.names)
    if not os.path.isfile(args.model):
        raise RuntimeError("model file not found: " + args.model)
    return LocalModel(args.model, args.device)


def describe(args):
    """Print the properties of a model and exit."""
    model = load_model(args)
    layout = guess_layout(model.output_shape, model.width, model.height, args.type)
    emit({
        "type": "info",
        "classes": fill_names(model.names, layout.classes),
        "device": model.device,
        "input": [model.width, model.height],
        "layout": layout.kind,
    })


def probe(source):
    """Return the width and the height of the first video track of a stream."""
    cmd = ["ffprobe", "-v", "error", "-select_streams", "v:0",
           "-show_entries", "stream=width,height", "-of", "json"]
    if source.startswith("rtsp"):
        cmd += ["-rtsp_transport", "tcp"]
    out = subprocess.run(cmd + [source], capture_output=True, timeout=30, check=True).stdout
    stream = json.loads(out.decode("utf-8"))["streams"][0]
    return int(stream["width"]), int(stream["height"])


RETRY_PAUSE = 5


class Stream(threading.Thread):
    """Analyzes one stream with the shared model, until it is stopped."""

    def __init__(self, sid, params, model, layout, infer_lock):
        super().__init__(daemon=True)
        self.sid = sid
        self.source = params["source"]
        self.fps = str(params.get("fps", "2"))
        self.conf = float(params.get("conf", 0.4))
        self.keyframes = bool(params.get("keyframes", False))
        self.model = model
        self.layout = layout
        self.infer_lock = infer_lock
        self.stopped = threading.Event()
        self.ffmpeg = None

    def stop(self):
        self.stopped.set()
        # FFmpeg can be stuck writing a frame that nobody is going to read: it is killed, not asked
        ffmpeg = self.ffmpeg
        if ffmpeg is not None:
            ffmpeg.kill()

    def run(self):
        while not self.stopped.is_set():
            try:
                self.analyze()
            except Exception as e:  # pylint: disable=broad-except
                if not self.stopped.is_set():
                    emit({"type": "error", "id": self.sid, "error": str(e)})
            self.stopped.wait(RETRY_PAUSE)

    def analyze(self):
        width, height = self.model.width, self.model.height
        src_w, src_h = probe(self.source)
        ratio = min(width / src_w, height / src_h)

        # FFmpeg picks the frames, scales them to the size the model wants and pads them,
        # so that only the few frames that are analyzed are handed over to Python.
        vf = "fps=%s,scale=%d:%d:force_original_aspect_ratio=decrease,pad=%d:%d:0:0:color=0x727272" % (
            self.fps, width, height, width, height)
        cmd = ["ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error"]
        if self.source.startswith("rtsp"):
            cmd += ["-rtsp_transport", "tcp"]
        # decoding is most of the cost of analyzing a stream. Frames are only fed to a model,
        # so the deblocking filter is skipped, and so are frames that no other frame depends on.
        # When only key frames are wanted, nothing else is decoded at all.
        if self.keyframes:
            cmd += ["-skip_frame", "nokey"]
            vf = vf.split(",", 1)[1]
        else:
            cmd += ["-skip_loop_filter", "all", "-skip_frame", "noref"]
        cmd += ["-i", self.source, "-map", "0:v:0", "-an", "-vf", vf, "-fps_mode", "passthrough",
                "-f", "rawvideo", "-pix_fmt", "rgb24", "-"]

        self.ffmpeg = subprocess.Popen(cmd, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
        frame_size = width * height * 3

        try:
            while not self.stopped.is_set():
                raw = self.ffmpeg.stdout.read(frame_size)
                if len(raw) < frame_size:
                    break

                frame = np.frombuffer(raw, dtype=np.uint8).reshape(height, width, 3)
                if self.layout.kind == "yolox":
                    # YOLOX wants BGR pixels between 0 and 255
                    tensor = frame[:, :, ::-1].transpose(2, 0, 1).astype(np.float32)
                else:
                    tensor = frame.transpose(2, 0, 1).astype(np.float32) / 255.0
                tensor = np.ascontiguousarray(tensor[None])

                # the model is shared: streams take turns
                with self.infer_lock:
                    start = time.time()
                    output = self.model.infer(tensor)
                    elapsed = (time.time() - start) * 1000.0

                boxes, scores, ids = decode(output, self.layout, width, height, self.conf)
                keep = nms(boxes, scores, ids)

                objects = []
                for i in keep:
                    x1, y1, x2, y2 = boxes[i] / ratio
                    x1 = min(max(float(x1), 0.0), src_w)
                    y1 = min(max(float(y1), 0.0), src_h)
                    x2 = min(max(float(x2), 0.0), src_w)
                    y2 = min(max(float(y2), 0.0), src_h)
                    if x2 <= x1 or y2 <= y1:
                        continue
                    objects.append({
                        "c": int(ids[i]),
                        "s": round(float(scores[i]), 3),
                        "b": [round(x1 / src_w, 4), round(y1 / src_h, 4),
                              round((x2 - x1) / src_w, 4), round((y2 - y1) / src_h, 4)],
                    })

                emit({"type": "frame", "id": self.sid, "t": round(time.time(), 3), "ms": round(elapsed, 1),
                      "w": src_w, "h": src_h, "objects": objects})
        finally:
            self.ffmpeg.kill()
            self.ffmpeg.stdout.close()
            self.ffmpeg.wait()
            self.ffmpeg = None

        if not self.stopped.is_set():
            raise RuntimeError("the stream ended")


def serve(args):
    """Load the model, then analyze the streams requested on standard input."""
    model = load_model(args)
    layout = guess_layout(model.output_shape, model.width, model.height, args.type)

    emit({
        "type": "info",
        "classes": fill_names(model.names, layout.classes),
        "device": model.device,
        "input": [model.width, model.height],
        "layout": layout.kind,
    })

    infer_lock = threading.Lock()
    streams = {}

    def stop_all(*_):
        for st in streams.values():
            st.stop()
        os._exit(0)

    signal.signal(signal.SIGTERM, stop_all)
    signal.signal(signal.SIGINT, stop_all)

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            cmd = json.loads(line)
        except ValueError:
            continue

        sid = str(cmd.get("id", ""))
        old = streams.pop(sid, None)
        if old is not None:
            old.stop()

        if cmd.get("cmd") == "add":
            streams[sid] = Stream(sid, cmd, model, layout, infer_lock)
            streams[sid].start()

    # standard input has been closed: the server is gone
    stop_all()


def main():
    parser = argparse.ArgumentParser(description="MediaMTX object detection worker")
    parser.add_argument("--model", required=True, help="ONNX file, or name of the model on the server")
    parser.add_argument("--server", default="", help="URL of a KServe v2 inference server")
    parser.add_argument("--names", default="", help="text file with the class names of a model on a server")
    parser.add_argument("--device", default="auto", choices=["auto", "cpu", "gpu"])
    parser.add_argument("--type", default="auto", choices=["auto", "v8", "v5", "yolox"])
    parser.add_argument("--describe", action="store_true", help="print the properties of the model and exit")
    args = parser.parse_args()

    try:
        if args.describe:
            describe(args)
        else:
            serve(args)
    except KeyboardInterrupt:
        pass
    except Exception as e:  # pylint: disable=broad-except
        log("error: " + str(e))
        sys.exit(1)


if __name__ == "__main__":
    main()
