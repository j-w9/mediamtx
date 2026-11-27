package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/test"
)

func optionalPath(t *testing.T, js string) *conf.OptionalPath {
	t.Helper()
	var out conf.OptionalPath
	require.NoError(t, json.Unmarshal([]byte(js), &out))
	return &out
}

func loadTestConf(t *testing.T, yml string) (*conf.Conf, string) {
	t.Helper()
	fpath := filepath.Join(t.TempDir(), "mediamtx.yml")
	require.NoError(t, os.WriteFile(fpath, []byte(yml), 0o644))
	c, _, err := conf.Load(fpath, nil, test.NilLogger)
	require.NoError(t, err)
	return c, fpath
}

func TestOverlayFilePath(t *testing.T) {
	require.Equal(t, "", overlayFilePath(""))
	require.Equal(t, filepath.Join("dir", "mediamtx.api.json"), overlayFilePath(filepath.Join("dir", "mediamtx.yml")))
}

func TestOverlay(t *testing.T) {
	base, fpath := loadTestConf(t, "paths:\n"+
		"  cam1:\n"+
		"    source: rtsp://localhost:8554/a\n"+
		"  cam2:\n"+
		"  cam3:\n")

	o := &overlay{}

	var global conf.OptionalGlobal
	require.NoError(t, json.Unmarshal([]byte(`{"playback": true}`), &global))
	o.recordGlobal(&global)

	o.recordPathDefaults(optionalPath(t, `{"recordSegmentDuration": "30m"}`))

	// existing path: two patches are merged
	o.recordPathPatch("cam1", toFields(optionalPath(t, `{"record": true}`)))
	o.recordPathPatch("cam1", toFields(optionalPath(t, `{"compress": true, "record": false}`)))

	// new path, then patched
	o.recordPathSet("new", optionalPath(t, `{"source": "rtsp://localhost:8554/b"}`))
	o.recordPathPatch("new", toFields(optionalPath(t, `{"record": true}`)))

	// deleted path
	o.recordPathDelete("cam2")

	// path deleted and created again
	o.recordPathDelete("cam3")
	o.recordPathSet("cam3", optionalPath(t, `{"maxReaders": 3}`))

	// the overlay survives a round trip to disk
	ofile := overlayFilePath(fpath)
	require.NoError(t, o.save(ofile))

	fi, err := os.Stat(ofile)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fi.Mode().Perm())

	o, err = loadOverlay(ofile)
	require.NoError(t, err)

	c := o.apply(base, test.NilLogger)

	require.True(t, c.Playback)
	require.Equal(t, conf.Duration(30*time.Minute), c.PathDefaults.RecordSegmentDuration)

	require.Equal(t, "rtsp://localhost:8554/a", c.Paths["cam1"].Source)
	require.False(t, c.Paths["cam1"].Record)
	require.True(t, c.Paths["cam1"].Compress)

	require.Equal(t, "rtsp://localhost:8554/b", c.Paths["new"].Source)
	require.True(t, c.Paths["new"].Record)

	require.NotContains(t, c.Paths, "cam2")
	require.Equal(t, 3, c.Paths["cam3"].MaxReaders)

	// the original configuration is not modified
	require.False(t, base.Playback)
	require.Contains(t, base.Paths, "cam2")
}

func TestOverlaySkipsInvalidChanges(t *testing.T) {
	base, _ := loadTestConf(t, "paths:\n  cam1:\n")

	o := &overlay{Paths: map[string]*overlayPath{
		"bad":  {Set: map[string]json.RawMessage{"source": json.RawMessage(`"not-a-valid-source"`)}},
		"good": {Set: map[string]json.RawMessage{"record": json.RawMessage(`true`)}},
	}}

	c := o.apply(base, test.NilLogger)

	require.NotContains(t, c.Paths, "bad")
	require.True(t, c.Paths["good"].Record)
}

func TestOverlayMissingFile(t *testing.T) {
	o, err := loadOverlay(filepath.Join(t.TempDir(), "missing.api.json"))
	require.NoError(t, err)

	base, _ := loadTestConf(t, "paths:\n  cam1:\n")
	require.Equal(t, base, o.apply(base, test.NilLogger))
}
