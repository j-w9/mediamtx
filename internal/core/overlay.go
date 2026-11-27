package core

import (
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/logger"
)

// overlayPath contains the changes made to a path through the API.
type overlayPath struct {
	// the path has been deleted.
	Delete bool `json:"delete,omitempty"`
	// the path has been added or replaced, and these are all its settings.
	Set map[string]json.RawMessage `json:"set,omitempty"`
	// some settings of a path of the configuration file have been changed.
	Patch map[string]json.RawMessage `json:"patch,omitempty"`
}

// overlay contains the changes made to the configuration through the API.
// It is saved next to the configuration file and applied on top of it at startup,
// so that changes survive restarts while the configuration file is left untouched.
type overlay struct {
	Global       map[string]json.RawMessage `json:"global,omitempty"`
	PathDefaults map[string]json.RawMessage `json:"pathDefaults,omitempty"`
	Paths        map[string]*overlayPath    `json:"paths,omitempty"`
}

// overlayFilePath returns the path of the overlay of a configuration file:
// mediamtx.yml -> mediamtx.api.json
func overlayFilePath(confPath string) string {
	if confPath == "" {
		return ""
	}
	return strings.TrimSuffix(confPath, filepath.Ext(confPath)) + ".api.json"
}

func loadOverlay(fpath string) (*overlay, error) {
	o := &overlay{}

	if fpath == "" {
		return o, nil
	}

	byts, err := os.ReadFile(fpath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return o, nil
		}
		return o, err
	}

	err = json.Unmarshal(byts, o)
	if err != nil {
		return &overlay{}, err
	}

	return o, nil
}

// save writes the overlay to disk. The file is replaced atomically and is readable
// by the owner only, since it can contain credentials.
func (o *overlay) save(fpath string) error {
	byts, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return err
	}

	tmp := fpath + ".tmp"

	err = os.WriteFile(tmp, append(byts, '\n'), 0o600)
	if err != nil {
		return err
	}

	return os.Rename(tmp, fpath)
}

// toFields converts a partial configuration into a map of the fields that are set.
func toFields(in any) map[string]json.RawMessage {
	byts, err := json.Marshal(in)
	if err != nil {
		return nil
	}

	var all map[string]json.RawMessage
	err = json.Unmarshal(byts, &all)
	if err != nil {
		return nil
	}

	for k, v := range all {
		if string(v) == "null" {
			delete(all, k)
		}
	}

	return all
}

func mergeFields(dest map[string]json.RawMessage, src map[string]json.RawMessage) map[string]json.RawMessage {
	if dest == nil {
		dest = make(map[string]json.RawMessage)
	}
	maps.Copy(dest, src)
	return dest
}

func (o *overlay) recordGlobal(in *conf.OptionalGlobal) {
	o.Global = mergeFields(o.Global, toFields(in))
}

// recordUsers is called when a user is added, changed or removed. The whole list is saved.
func (o *overlay) recordUsers(users []conf.AuthInternalUser) {
	byts, err := json.Marshal(users)
	if err != nil {
		return
	}
	o.Global = mergeFields(o.Global, map[string]json.RawMessage{"authInternalUsers": byts})
}

func (o *overlay) recordPathDefaults(in *conf.OptionalPath) {
	o.PathDefaults = mergeFields(o.PathDefaults, toFields(in))
}

// recordPathSet is called when a path is added or replaced.
func (o *overlay) recordPathSet(name string, in *conf.OptionalPath) {
	if o.Paths == nil {
		o.Paths = make(map[string]*overlayPath)
	}
	o.Paths[name] = &overlayPath{Set: mergeFields(nil, toFields(in))}
}

func (o *overlay) recordPathPatch(name string, fields map[string]json.RawMessage) {
	if o.Paths == nil {
		o.Paths = make(map[string]*overlayPath)
	}

	switch cur := o.Paths[name]; {
	case cur != nil && cur.Set != nil:
		cur.Set = mergeFields(cur.Set, fields)

	case cur != nil && !cur.Delete:
		cur.Patch = mergeFields(cur.Patch, fields)

	default:
		o.Paths[name] = &overlayPath{Patch: mergeFields(nil, fields)}
	}
}

func (o *overlay) recordPathDelete(name string) {
	if o.Paths == nil {
		o.Paths = make(map[string]*overlayPath)
	}
	o.Paths[name] = &overlayPath{Delete: true}
}

func optionalPathFromFields(fields map[string]json.RawMessage) (*conf.OptionalPath, error) {
	byts, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}

	var out conf.OptionalPath
	err = json.Unmarshal(byts, &out)
	if err != nil {
		return nil, err
	}

	return &out, nil
}

// apply applies the overlay to a configuration and returns the result.
// A change that cannot be applied anymore, for instance because it is not compatible
// with a newer configuration file, is reported and skipped.
func (o *overlay) apply(base *conf.Conf, l logger.Writer) *conf.Conf {
	cur := base

	step := func(desc string, cb func(c *conf.Conf) error) {
		next := cur.Clone()

		err := cb(next)
		if err != nil {
			l.Log(logger.Warn, "unable to apply saved API change (%s): %v", desc, err)
			return
		}

		cur = next
	}

	if len(o.Global) != 0 {
		step("global settings", func(c *conf.Conf) error {
			byts, err := json.Marshal(o.Global)
			if err != nil {
				return err
			}

			var in conf.OptionalGlobal
			err = json.Unmarshal(byts, &in)
			if err != nil {
				return err
			}

			return c.PatchGlobal(&in)
		})
	}

	if len(o.PathDefaults) != 0 {
		step("path defaults", func(c *conf.Conf) error {
			in, err := optionalPathFromFields(o.PathDefaults)
			if err != nil {
				return err
			}

			return c.PatchPathDefaults(in)
		})
	}

	names := make([]string, 0, len(o.Paths))
	for name := range o.Paths {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		pa := o.Paths[name]

		step("path '"+name+"'", func(c *conf.Conf) error {
			_, exists := c.OptionalPaths[name]

			switch {
			case pa.Delete:
				if !exists {
					return nil
				}
				return c.RemovePath(name)

			case pa.Set != nil:
				in, err := optionalPathFromFields(pa.Set)
				if err != nil {
					return err
				}
				return c.ReplacePath(name, in)

			default:
				in, err := optionalPathFromFields(pa.Patch)
				if err != nil {
					return err
				}
				if !exists {
					return c.AddPath(name, in)
				}
				return c.PatchPath(name, in)
			}
		})
	}

	return cur
}

// loadConfOverlay loads the overlay of the configuration file and applies it.
func (p *Core) loadConfOverlay(base *conf.Conf, l logger.Writer) *conf.Conf {
	o, err := loadOverlay(overlayFilePath(p.confPath))
	if err != nil {
		l.Log(logger.Warn, "unable to read saved API changes: %v", err)
	}

	p.overlay = o

	return o.apply(base, l)
}

// saveConfOverlay records a change made through the API.
// Changes are saved only when a configuration file is in use.
func (p *Core) saveConfOverlay(record func(o *overlay)) {
	fpath := overlayFilePath(p.confPath)
	if fpath == "" || p.overlay == nil {
		return
	}

	record(p.overlay)

	err := p.overlay.save(fpath)
	if err != nil {
		p.Log(logger.Warn, "unable to save API changes: %v", err)
	}
}
