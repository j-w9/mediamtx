// Package detect runs object detection models on streams.
package detect

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/logger"
)

// the worker is a Python script that is embedded in the executable.
// It is written to a temporary file, since its standard input is used to send it commands.
//
//go:embed worker.py
var workerScript []byte

const (
	restartPause    = 5 * time.Second
	stopTimeout     = 3 * time.Second
	describeTimeout = 60 * time.Second
)

var (
	scriptOnce sync.Once
	scriptPath string
	errScript  error
)

// script returns the path of the worker script, after writing it to disk if needed.
func script() (string, error) {
	scriptOnce.Do(func() {
		sum := sha256.Sum256(workerScript)
		fpath := filepath.Join(os.TempDir(), "mediamtx-detect-"+hex.EncodeToString(sum[:6])+".py")

		// the file is written again at every start, in case it has been tampered with
		tmp := fpath + "." + strconv.Itoa(os.Getpid())
		errScript = os.WriteFile(tmp, workerScript, 0o600)
		if errScript != nil {
			return
		}
		errScript = os.Rename(tmp, fpath)
		scriptPath = fpath
	})
	return scriptPath, errScript
}

type workerMessage struct {
	Type    string   `json:"type"`
	ID      string   `json:"id"`
	Error   string   `json:"error"`
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
		return append(args,
			"--server", cnf.DetectServer,
			"--model", cnf.DetectModel,
			// class names of models on a server can be provided by a local file
			"--names", filepath.Join(cnf.DetectModelsDir, filepath.FromSlash(cnf.DetectModel)+".names"))
	}

	return append(args,
		"--model", filepath.Join(cnf.DetectModelsDir, filepath.FromSlash(cnf.DetectModel)),
		"--device", cnf.DetectDevice)
}

func command(ctx context.Context, python string, args []string) (*exec.Cmd, error) {
	fpath, err := script()
	if err != nil {
		return nil, fmt.Errorf("unable to write the worker script: %w", err)
	}

	cmd := exec.CommandContext(ctx, python, append([]string{fpath}, args...)...)

	// ask the worker to exit, so that it can stop FFmpeg, and kill it if it does not.
	cmd.Cancel = func() error {
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = stopTimeout

	return cmd, nil
}

// engine is a worker process, that holds a model in memory and analyzes
// every stream that uses that model. Loading a model once, instead of once per stream,
// is what keeps memory usage flat when many streams are analyzed.
type engine struct {
	key    string
	python string
	args   []string
	logger logger.Writer

	ctx       context.Context
	ctxCancel func()
	done      chan struct{}

	mutex   sync.Mutex
	workers map[string]*Worker
	stdin   io.WriteCloser
	classes []string
	device  string
	// error that concerns the model, and therefore every stream
	lastError string
}

var (
	enginesMutex sync.Mutex
	engines      = make(map[string]*engine)
	workerSeq    atomic.Uint64
)

// attach adds a worker to the engine of its model, starting the engine if needed.
func attach(w *Worker) *engine {
	args := modelArgs(w.Conf)
	key := w.Conf.DetectPython + "\x00" + strings.Join(args, "\x00")

	enginesMutex.Lock()
	defer enginesMutex.Unlock()

	e, ok := engines[key]
	if !ok {
		e = &engine{
			key:     key,
			python:  w.Conf.DetectPython,
			args:    args,
			logger:  w,
			workers: make(map[string]*Worker),
			done:    make(chan struct{}),
		}
		e.ctx, e.ctxCancel = context.WithCancel(context.Background())
		engines[key] = e
		go e.run()
	}

	e.mutex.Lock()
	e.workers[w.id] = w
	e.sendAdd(w)
	e.mutex.Unlock()

	return e
}

// detach removes a worker from its engine, and stops the engine when it has no workers left.
func (e *engine) detach(w *Worker) {
	enginesMutex.Lock()

	e.mutex.Lock()
	delete(e.workers, w.id)
	e.send(map[string]any{"cmd": "remove", "id": w.id})
	empty := len(e.workers) == 0
	e.mutex.Unlock()

	if empty {
		delete(engines, e.key)
	}

	enginesMutex.Unlock()

	if empty {
		e.ctxCancel()
		<-e.done
	}
}

// send writes a command to the worker. The mutex must be held.
func (e *engine) send(cmd map[string]any) {
	if e.stdin == nil {
		return
	}
	byts, _ := json.Marshal(cmd)
	e.stdin.Write(append(byts, '\n')) //nolint:errcheck
}

// sendAdd asks the worker to analyze a stream. The mutex must be held.
func (e *engine) sendAdd(w *Worker) {
	e.send(map[string]any{
		"cmd":       "add",
		"id":        w.id,
		"source":    w.Source,
		"fps":       strconv.FormatFloat(w.Conf.DetectFPS, 'f', -1, 64),
		"conf":      w.Conf.DetectConfidence,
		"keyframes": w.Conf.DetectKeyFramesOnly,
	})
}

func (e *engine) run() {
	defer close(e.done)

	for {
		err := e.runInner()

		select {
		case <-e.ctx.Done():
			return
		default:
		}

		e.mutex.Lock()
		e.stdin = nil
		if e.lastError == "" {
			e.lastError = err.Error()
		}
		msg := e.lastError
		for _, w := range e.workers {
			w.reset()
		}
		e.mutex.Unlock()

		e.logger.Log(logger.Error, "%s, retrying in %v", msg, restartPause)

		select {
		case <-time.After(restartPause):
		case <-e.ctx.Done():
			return
		}
	}
}

func (e *engine) runInner() error {
	cmd, err := command(e.ctx, e.python, e.args)
	if err != nil {
		return err
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}

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
		return fmt.Errorf("unable to start '%s': %w", e.python, err)
	}

	// streams that were being analyzed are handed to the new process
	e.mutex.Lock()
	e.stdin = stdin
	e.lastError = ""
	for _, w := range e.workers {
		e.sendAdd(w)
	}
	e.mutex.Unlock()

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
				e.mutex.Lock()
				e.lastError = msg
				e.mutex.Unlock()
			} else {
				e.logger.Log(logger.Debug, "%s", line)
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

		e.onMessage(&msg)
	}

	<-stderrDone

	err = cmd.Wait()
	if err != nil {
		return fmt.Errorf("worker exited: %w", err)
	}

	return fmt.Errorf("worker exited")
}

func (e *engine) onMessage(msg *workerMessage) {
	e.mutex.Lock()

	if msg.Type == "info" {
		e.classes = msg.Classes
		e.device = msg.Device
		e.lastError = ""
		e.mutex.Unlock()

		e.logger.Log(logger.Info, "model '%s' loaded on %s, %d classes",
			filepath.Base(e.args[len(e.args)-3]), msg.Device, len(msg.Classes))
		return
	}

	w := e.workers[msg.ID]
	classes := e.classes
	e.mutex.Unlock()

	if w != nil {
		w.onMessage(msg, classes)
	}
}

// state returns what concerns every stream of the engine.
func (e *engine) state() ([]string, string, string) {
	e.mutex.Lock()
	defer e.mutex.Unlock()
	return e.classes, e.device, e.lastError
}

// Worker analyzes the stream of a path with a detection model.
// Workers that use the same model share the process that runs it.
type Worker struct {
	Conf   *conf.Path
	Source string
	Parent logger.Writer

	id     string
	engine *engine

	mutex       sync.Mutex
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
	w.id = strconv.FormatUint(workerSeq.Add(1), 10)
	w.seen = make(map[string]time.Time)
	w.engine = attach(w)
}

// Close closes the worker.
func (w *Worker) Close() {
	w.engine.detach(w)
}

// Log implements logger.Writer.
func (w *Worker) Log(level logger.Level, format string, args ...any) {
	w.Parent.Log(level, "[detect] "+format, args...)
}

// reset is called when the process that analyzes the stream exits.
func (w *Worker) reset() {
	w.mutex.Lock()
	defer w.mutex.Unlock()
	w.objects = nil
	w.lastFrame = nil
}

func (w *Worker) onMessage(msg *workerMessage, classes []string) {
	w.mutex.Lock()
	defer w.mutex.Unlock()

	switch msg.Type {
	case "error":
		w.lastError = msg.Error
		w.objects = nil
		w.lastFrame = nil

	case "frame":
		now := time.Now()
		w.lastFrame = &now
		w.inferenceMS = msg.MS
		w.frameWidth = msg.W
		w.frameHeight = msg.H
		w.lastError = ""

		w.objects = make([]defs.APIPathDetectionObject, 0, len(msg.Objects))

		for _, o := range msg.Objects {
			if o.C < 0 || o.C >= len(classes) {
				continue
			}

			class := classes[o.C]
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
	var classes []string
	var device, engineError string
	if w.engine != nil {
		classes, device, engineError = w.engine.state()
	}

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

	if classes == nil {
		classes = []string{}
	}

	// a problem with the model matters more than one with the stream
	lastError := engineError
	if lastError == "" {
		lastError = w.lastError
	}

	return &defs.APIPathDetection{
		Model:       w.Conf.DetectModel,
		Device:      device,
		Classes:     classes,
		LastFrame:   w.lastFrame,
		InferenceMS: w.inferenceMS,
		FrameWidth:  w.frameWidth,
		FrameHeight: w.frameHeight,
		Objects:     objects,
		Seen:        seen,
		Error:       lastError,
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

	ret := defs.APIDetectModel{Name: cnf.DetectModel, Classes: []string{}}

	cmd, err := command(ctx, cnf.DetectPython, args)
	if err != nil {
		ret.Error = err.Error()
		return ret
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err = cmd.Run()

	var msg workerMessage

	switch {
	case err != nil:
		ret.Error = err.Error()

		for line := range strings.SplitSeq(stderr.String(), "\n") {
			if m, ok := strings.CutPrefix(strings.TrimSpace(line), "error: "); ok {
				ret.Error = m
			}
		}

		// do not remember failures that are not caused by the model
		if ctx.Err() != nil || errors.Is(err, exec.ErrNotFound) {
			return ret
		}

	case json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &msg) != nil || len(msg.Input) != 2:
		ret.Error = "invalid reply from the worker"

	default:
		ret.Classes = msg.Classes
		ret.InputWidth = msg.Input[0]
		ret.InputHeight = msg.Input[1]
	}

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
