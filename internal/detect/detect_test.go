package detect

import (
	"context"
	"os"
	"path/filepath"
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

	w.onMessage(&workerMessage{Type: "info", Classes: []string{"person", "car"}, Device: "CPU"})

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
	w.onMessage(msg)

	item = w.APIItem()
	require.Equal(t, "yolo.onnx", item.Model)
	require.Equal(t, "CPU", item.Device)
	require.Equal(t, []string{"person", "car"}, item.Classes)
	require.NotNil(t, item.LastFrame)
	require.Equal(t, 12.5, item.InferenceMS)
	require.Equal(t, 1280, item.FrameWidth)
	require.Len(t, item.Objects, 1)
	require.Equal(t, "car", item.Objects[0].Class)
	require.Equal(t, 0.3, item.Objects[0].W)
	require.Len(t, item.Seen, 1)
	require.Equal(t, "car", item.Seen[0].Class)

	// objects of the previous frame are replaced, classes seen are remembered
	w.onMessage(&workerMessage{Type: "frame", W: 1280, H: 720})
	item = w.APIItem()
	require.Empty(t, item.Objects)
	require.Len(t, item.Seen, 1)
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
