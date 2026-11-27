package detect

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/test"
)

func TestModelArgs(t *testing.T) {
	require.Equal(t,
		[]string{"--type", "auto", "--model", filepath.Join("models", "yolo.onnx"), "--device", "gpu"},
		modelArgs(&conf.Path{
			DetectModel:     "yolo.onnx",
			DetectModelsDir: "models",
			DetectModelType: "auto",
			DetectDevice:    "gpu",
		}))

	require.Equal(t,
		[]string{
			"--type", "v8", "--server", "http://triton:8000", "--model", "yolo",
			"--names", filepath.Join("models", "yolo.names"),
		},
		modelArgs(&conf.Path{
			DetectModel:     "yolo",
			DetectServer:    "http://triton:8000",
			DetectModelsDir: "models",
			DetectModelType: "v8",
			DetectDevice:    "gpu",
		}))
}

func TestWorkerMessages(t *testing.T) {
	w := &Worker{
		Conf:   &conf.Path{DetectModel: "yolo.onnx"},
		Parent: test.NilLogger,
	}
	w.seen = make(map[string]time.Time)

	item := w.APIItem()
	require.Equal(t, []string{}, item.Classes)
	require.Nil(t, item.LastFrame)

	classes := []string{"person", "car"}

	msg := &workerMessage{Type: "frame", MS: 12.5, W: 1280, H: 720}
	msg.Objects = append(msg.Objects,
		struct {
			C int        `json:"c"`
			S float64    `json:"s"`
			B [4]float64 `json:"b"`
		}{C: 1, S: 0.9, B: [4]float64{0.1, 0.2, 0.3, 0.4}},
		// class index outside the model's classes: ignored
		struct {
			C int        `json:"c"`
			S float64    `json:"s"`
			B [4]float64 `json:"b"`
		}{C: 7, S: 0.9},
	)
	w.onMessage(msg, classes)

	item = w.APIItem()
	require.Equal(t, "yolo.onnx", item.Model)
	require.NotNil(t, item.LastFrame)
	require.Equal(t, 12.5, item.InferenceMS)
	require.Equal(t, 1280, item.FrameWidth)
	require.Len(t, item.Objects, 1)
	require.Equal(t, "car", item.Objects[0].Class)
	require.Equal(t, 0.3, item.Objects[0].W)
	require.Len(t, item.Seen, 1)
	require.Equal(t, "car", item.Seen[0].Class)

	// objects of the previous frame are replaced, classes seen are remembered
	w.onMessage(&workerMessage{Type: "frame", W: 1280, H: 720}, classes)
	item = w.APIItem()
	require.Empty(t, item.Objects)
	require.Len(t, item.Seen, 1)

	// a stream that cannot be read is reported, and its objects are cleared
	w.onMessage(msg, classes)
	w.onMessage(&workerMessage{Type: "error", Error: "the stream ended"}, classes)
	item = w.APIItem()
	require.Equal(t, "the stream ended", item.Error)
	require.Empty(t, item.Objects)
	require.Nil(t, item.LastFrame)
}

// TestEngineSharing checks that streams that use the same model share one process,
// with a stand-in for the Python worker.
func TestEngineSharing(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip()
	}

	dir := t.TempDir()

	// a fake interpreter: it ignores the script, announces a model,
	// and reports one frame for each stream it is asked to analyze.
	fake := filepath.Join(dir, "python")
	require.NoError(t, os.WriteFile(fake, []byte(`#!/bin/sh
echo "$$" >> "`+dir+`/pids"
echo '{"type":"info","classes":["person","car"],"device":"CPU","input":[640,640]}'
while read -r line; do
  id=$(echo "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
  case "$line" in
    *'"cmd":"add"'*)
      printf '{"type":"frame","id":"%s","ms":1,"w":10,"h":10,' "$id"
      echo '"objects":[{"c":1,"s":0.9,"b":[0,0,1,1]}]}' ;;
  esac
done
`), 0o755))

	newWorker := func(model string) *Worker {
		return &Worker{
			Conf: &conf.Path{
				DetectPython:    fake,
				DetectModel:     model,
				DetectModelsDir: dir,
				DetectModelType: "auto",
				DetectDevice:    "cpu",
				DetectFPS:       2,
			},
			Source: "rtsp://127.0.0.1/x",
			Parent: test.NilLogger,
		}
	}

	a := newWorker("one.onnx")
	b := newWorker("one.onnx")
	c := newWorker("two.onnx")
	a.Start()
	b.Start()
	c.Start()

	for _, w := range []*Worker{a, b, c} {
		require.Eventually(t, func() bool {
			item := w.APIItem()
			return len(item.Objects) == 1 && item.Objects[0].Class == "car" && item.Device == "CPU"
		}, 5*time.Second, 20*time.Millisecond)
	}

	// two models, two processes, although there are three streams
	require.Same(t, a.engine, b.engine)
	require.NotSame(t, a.engine, c.engine)

	byts, err := os.ReadFile(filepath.Join(dir, "pids"))
	require.NoError(t, err)
	require.Len(t, strings.Fields(string(byts)), 2)

	// the process stays while one of its streams remains
	a.Close()
	enginesMutex.Lock()
	n := len(engines)
	enginesMutex.Unlock()
	require.Equal(t, 2, n)

	b.Close()
	c.Close()
	enginesMutex.Lock()
	n = len(engines)
	enginesMutex.Unlock()
	require.Equal(t, 0, n)
}

func TestLocalModels(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"b.onnx", "a.ONNX", "notes.txt"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), nil, 0o644))
	}
	require.NoError(t, os.Mkdir(filepath.Join(dir, "sub.onnx"), 0o755))

	require.Equal(t, []string{"a.ONNX", "b.onnx"}, LocalModels(dir))
	require.Equal(t, []string{}, LocalModels(filepath.Join(dir, "missing")))
}

func TestDescribeMissingModel(t *testing.T) {
	m := Describe(context.Background(), &conf.Path{
		DetectModel:     "missing.onnx",
		DetectModelsDir: t.TempDir(),
		DetectPython:    "python3",
	})
	require.Equal(t, "missing.onnx", m.Name)
	require.Equal(t, "model file not found", m.Error)
}
