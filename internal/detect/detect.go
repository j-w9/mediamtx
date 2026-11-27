// Package detect runs object detection models on streams.
package detect

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
)

// the worker is a Python script that is embedded in the executable
// and passed to the interpreter through standard input.
//
//go:embed worker.py
var workerScript []byte

const (
	restartPause    = 5 * time.Second
	stopTimeout     = 3 * time.Second
	describeTimeout = 60 * time.Second
)

type workerMessage struct {
	Type    string   `json:"type"`
	Classes []string `json:"classes"`
	Device  string   `json:"device"`
	Input   []int    `json:"input"`
	MS      float64  `json:"ms"`
	W       int      `json:"w"`
	H       int      `json:"h"`
	Objects []struct {
		C int        `json:"c"`
		S float64    `json:"s"`
		B [4]float64 `json:"b"`
	} `json:"objects"`
}

// modelArgs returns the arguments that select a model.
func modelArgs(cnf *conf.Path) []string {
	args := []string{"--type", cnf.DetectModelType}

	if cnf.DetectServer != "" {
		return append(args, "--server", cnf.DetectServer, "--model", cnf.DetectModel)
	}

	return append(args,
		"--model", filepath.Join(cnf.DetectModelsDir, filepath.FromSlash(cnf.DetectModel)),
		"--device", cnf.DetectDevice)
}

func command(ctx context.Context, python string, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, python, append([]string{"-"}, args...)...)
	cmd.Stdin = bytes.NewReader(workerScript)

	// ask the worker to exit, so that it can stop FFmpeg, and kill it if it does not.
	cmd.Cancel = func() error {
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = stopTimeout

	return cmd
}

// Worker runs a detection model on the stream of a path.
type Worker struct {
	Conf   *conf.Path
	Source string
	Parent logger.Writer

	ctx       context.Context
	ctxCancel func()
	done      chan struct{}

	mutex       sync.Mutex
	classes     []string
	device      string
	lastFrame   *time.Time
	inferenceMS float64
	frameWidth  int
	frameHeight int
	objects     []defs.APIPathDetectionObject
	seen        map[string]time.Time
	lastError   string
}

// Start starts the worker.
func (w *Worker) Start() {
	w.ctx, w.ctxCancel = context.WithCancel(context.Background())
	w.done = make(chan struct{})
	w.seen = make(map[string]time.Time)

	go w.run()
}

// Close closes the worker.
func (w *Worker) Close() {
	w.ctxCancel()
	<-w.done
}

// Log implements logger.Writer.
func (w *Worker) Log(level logger.Level, format string, args ...any) {
	w.Parent.Log(level, "[detect] "+format, args...)
}

func (w *Worker) run() {
	defer close(w.done)

	for {
		err := w.runInner()

		select {
		case <-w.ctx.Done():
			return
		default:
		}

		w.mutex.Lock()
		if w.lastError == "" {
			w.lastError = err.Error()
		}
		w.objects = nil
		w.lastFrame = nil
		msg := w.lastError
		w.mutex.Unlock()

		w.Log(logger.Error, "%s, retrying in %v", msg, restartPause)

		select {
		case <-time.After(restartPause):
		case <-w.ctx.Done():
			return
		}
	}
}

func (w *Worker) runInner() error {
	args := append(modelArgs(w.Conf),
		"--source", w.Source,
		"--fps", strconv.FormatFloat(w.Conf.DetectFPS, 'f', -1, 64),
		"--conf", strconv.FormatFloat(w.Conf.DetectConfidence, 'f', -1, 64))

	cmd := command(w.ctx, w.Conf.DetectPython, args)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	err = cmd.Start()
	if err != nil {
		return fmt.Errorf("unable to start '%s': %w", w.Conf.DetectPython, err)
	}

	w.mutex.Lock()
	w.lastError = ""
	w.mutex.Unlock()

	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)

		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}

			if msg, ok := strings.CutPrefix(line, "error: "); ok {
				w.mutex.Lock()
				w.lastError = msg
				w.mutex.Unlock()
			} else {
				w.Log(logger.Debug, "%s", line)
			}
		}
	}()

	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for sc.Scan() {
		var msg workerMessage
		err = json.Unmarshal(sc.Bytes(), &msg)
		if err != nil {
			continue
		}

		w.onMessage(&msg)
	}

	<-stderrDone

	err = cmd.Wait()
	if err != nil {
		return fmt.Errorf("worker exited: %w", err)
	}

	return fmt.Errorf("worker exited")
}

func (w *Worker) onMessage(msg *workerMessage) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	switch msg.Type {
	case "info":
		w.classes = msg.Classes
		w.device = msg.Device

		w.Log(logger.Info, "running model '%s' on %s, %d classes",
			w.Conf.DetectModel, msg.Device, len(msg.Classes))

	case "frame":
		now := time.Now()
		w.lastFrame = &now
		w.inferenceMS = msg.MS
		w.frameWidth = msg.W
		w.frameHeight = msg.H
		w.lastError = ""

		w.objects = make([]defs.APIPathDetectionObject, 0, len(msg.Objects))

		for _, o := range msg.Objects {
			if o.C < 0 || o.C >= len(w.classes) {
				continue
			}

			class := w.classes[o.C]
			w.seen[class] = now

			w.objects = append(w.objects, defs.APIPathDetectionObject{
				Class: class,
				Score: o.S,
				X:     o.B[0],
				Y:     o.B[1],
				W:     o.B[2],
				H:     o.B[3],
			})
		}
	}
}

// APIItem returns the state of the detection.
func (w *Worker) APIItem() *defs.APIPathDetection {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	seen := make([]defs.APIPathDetectionSeen, 0, len(w.seen))
	for class, t := range w.seen {
		seen = append(seen, defs.APIPathDetectionSeen{Class: class, Time: t})
	}
	sort.Slice(seen, func(i, j int) bool {
		return seen[i].Time.After(seen[j].Time)
	})

	objects := make([]defs.APIPathDetectionObject, len(w.objects))
	copy(objects, w.objects)

	classes := w.classes
	if classes == nil {
		classes = []string{}
	}

	return &defs.APIPathDetection{
		Model:       w.Conf.DetectModel,
		Device:      w.device,
		Classes:     classes,
		LastFrame:   w.lastFrame,
		InferenceMS: w.inferenceMS,
		FrameWidth:  w.frameWidth,
		FrameHeight: w.frameHeight,
		Objects:     objects,
		Seen:        seen,
		Error:       w.lastError,
	}
}

type describeKey struct {
	python string
	args   string
	stamp  string
}

var (
	describeMutex sync.Mutex
	describeCache = make(map[describeKey]defs.APIDetectModel)
)

// Describe loads a model and returns its properties, in particular its classes.
// Results of local models are cached until the file changes.
func Describe(ctx context.Context, cnf *conf.Path) defs.APIDetectModel {
	args := append(modelArgs(cnf), "--describe")

	key := describeKey{python: cnf.DetectPython, args: strings.Join(args, "\x00")}

	if cnf.DetectServer == "" {
		fi, err := os.Stat(filepath.Join(cnf.DetectModelsDir, filepath.FromSlash(cnf.DetectModel)))
		if err != nil {
			return defs.APIDetectModel{Name: cnf.DetectModel, Classes: []string{}, Error: "model file not found"}
		}
		key.stamp = fi.ModTime().String() + strconv.FormatInt(fi.Size(), 10)

		describeMutex.Lock()
		cached, ok := describeCache[key]
		describeMutex.Unlock()
		if ok {
			return cached
		}
	}

	ctx, cancel := context.WithTimeout(ctx, describeTimeout)
	defer cancel()

	cmd := command(ctx, cnf.DetectPython, args)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	ret := defs.APIDetectModel{Name: cnf.DetectModel, Classes: []string{}}

	err := cmd.Run()
	if err != nil {
		ret.Error = err.Error()

		for line := range strings.SplitSeq(stderr.String(), "\n") {
			if msg, ok := strings.CutPrefix(strings.TrimSpace(line), "error: "); ok {
				ret.Error = msg
			}
		}

		return ret
	}

	var msg workerMessage
	err = json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &msg)
	if err != nil || len(msg.Input) != 2 {
		ret.Error = "invalid reply from the worker"
		return ret
	}

	ret.Classes = msg.Classes
	ret.InputWidth = msg.Input[0]
	ret.InputHeight = msg.Input[1]

	if cnf.DetectServer == "" {
		describeMutex.Lock()
		describeCache[key] = ret
		describeMutex.Unlock()
	}

	return ret
}

// LocalModels returns the names of the models that are available in the models directory.
func LocalModels(dir string) []string {
	ret := []string{}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return ret
	}

	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".onnx") {
			ret = append(ret, e.Name())
		}
	}

	sort.Strings(ret)
	return ret
}
