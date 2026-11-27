// Package telemetry keeps track of the position of sensors, read from KLV metadata.
package telemetry

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/klv"
)

const (
	// points of the track of the platform that are remembered.
	maxTrackPoints = 300
	// minimum time between two points of the track.
	trackInterval = 2 * time.Second
	// distance in degrees between two points of the track, beyond which the track is restarted.
	maxTrackJump = 0.5
)

// Tracker follows the metadata of a stream.
// Every KLV unit of the stream passes through Process().
type Tracker struct {
	repair atomic.Bool

	mutex          sync.Mutex
	last           *klv.Telemetry
	lastPacket     time.Time
	lastTrackPoint time.Time
	track          []klv.Point
	packets        uint64
	packetsInvalid uint64
	packetsFixed   uint64
}

// SetRepair sets whether packets are repaired.
func (t *Tracker) SetRepair(v bool) {
	t.repair.Store(v)
}

// Process reads the KLV packets of a unit and returns the unit to distribute,
// which is the same one unless repair is enabled and something had to be fixed.
func (t *Tracker) Process(buf []byte) []byte {
	pkts, splitErr := klv.Split(buf)
	repair := t.repair.Load()
	now := time.Now()

	var out []byte
	changed := splitErr != nil

	for _, pkt := range pkts {
		if !klv.IsUASDatalink(pkt) {
			// other kinds of KLV are passed through untouched
			if repair {
				out = append(out, pkt...)
			}
			continue
		}

		dec, err := klv.Decode(pkt)

		t.mutex.Lock()
		t.packets++
		if err != nil || !dec.ChecksumValid {
			t.packetsInvalid++
		}
		if dec != nil && len(dec.Items) != 0 {
			t.update(dec.Telemetry(), now)
		}
		t.mutex.Unlock()

		if !repair {
			continue
		}

		res, err := klv.RepairPacket(pkt, now)
		if err != nil {
			// nothing can be recovered: the packet is dropped
			changed = true
			t.mutex.Lock()
			t.packetsFixed++
			t.mutex.Unlock()
			continue
		}

		if len(res.Fixes) != 0 {
			changed = true
			t.mutex.Lock()
			t.packetsFixed++
			t.mutex.Unlock()
		}

		out = append(out, res.Packet...)
	}

	if !repair || !changed {
		return buf
	}

	return out
}

// update merges a packet into the last known state. Packets often carry a subset
// of the fields, so fields that are absent keep their previous value.
func (t *Tracker) update(in *klv.Telemetry, now time.Time) {
	t.lastPacket = now

	if t.last == nil {
		t.last = &klv.Telemetry{}
	}
	l := t.last

	str := func(dst *string, src string) {
		if src != "" {
			*dst = src
		}
	}
	num := func(dst **float64, src *float64) {
		if src != nil {
			*dst = src
		}
	}

	if in.Time != nil {
		l.Time = in.Time
	}
	if in.Version != nil {
		l.Version = in.Version
	}
	str(&l.MissionID, in.MissionID)
	str(&l.TailNumber, in.TailNumber)
	str(&l.PlatformDesignation, in.PlatformDesignation)
	str(&l.CallSign, in.CallSign)
	str(&l.Sensor, in.Sensor)
	num(&l.PlatformHeading, in.PlatformHeading)
	num(&l.PlatformPitch, in.PlatformPitch)
	num(&l.PlatformRoll, in.PlatformRoll)
	num(&l.GroundSpeed, in.GroundSpeed)
	num(&l.SensorLat, in.SensorLat)
	num(&l.SensorLon, in.SensorLon)
	num(&l.SensorAlt, in.SensorAlt)
	num(&l.HFOV, in.HFOV)
	num(&l.VFOV, in.VFOV)
	num(&l.RelAzimuth, in.RelAzimuth)
	num(&l.RelElevation, in.RelElevation)
	num(&l.RelRoll, in.RelRoll)
	num(&l.SlantRange, in.SlantRange)
	num(&l.TargetWidth, in.TargetWidth)
	num(&l.FrameCenterLat, in.FrameCenterLat)
	num(&l.FrameCenterLon, in.FrameCenterLon)
	num(&l.FrameCenterElev, in.FrameCenterElev)

	// corners belong to a frame center: a new center without corners invalidates the old ones
	if len(in.Corners) == 4 {
		l.Corners = in.Corners
	} else if in.FrameCenterLat != nil {
		l.Corners = nil
	}

	if in.SensorLat != nil && in.SensorLon != nil && now.Sub(t.lastTrackPoint) >= trackInterval {
		pt := klv.Point{Lat: *in.SensorLat, Lon: *in.SensorLon}

		// a jump that no aircraft can make means that the source has changed:
		// the track starts again
		if n := len(t.track); n != 0 &&
			(math.Abs(t.track[n-1].Lat-pt.Lat) > maxTrackJump || math.Abs(t.track[n-1].Lon-pt.Lon) > maxTrackJump) {
			t.track = nil
		}

		t.lastTrackPoint = now
		t.track = append(t.track, pt)
		if len(t.track) > maxTrackPoints {
			t.track = t.track[len(t.track)-maxTrackPoints:]
		}
	}
}

// Latest returns the last known state, or nil when no metadata has been received.
func (t *Tracker) Latest() (*klv.Telemetry, time.Time) {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if t.last == nil {
		return nil, time.Time{}
	}

	cp := *t.last
	return &cp, t.lastPacket
}

// APIItem returns the state for the API, or nil when no metadata has been received.
func (t *Tracker) APIItem() *defs.APIPathTelemetry {
	t.mutex.Lock()
	defer t.mutex.Unlock()

	if t.last == nil {
		return nil
	}

	l := t.last

	item := &defs.APIPathTelemetry{
		Time:                l.Time,
		LastPacket:          t.lastPacket,
		MissionID:           l.MissionID,
		TailNumber:          l.TailNumber,
		PlatformDesignation: l.PlatformDesignation,
		CallSign:            l.CallSign,
		Sensor:              l.Sensor,
		Version:             l.Version,
		PlatformHeading:     l.PlatformHeading,
		PlatformPitch:       l.PlatformPitch,
		PlatformRoll:        l.PlatformRoll,
		GroundSpeed:         l.GroundSpeed,
		SensorLat:           l.SensorLat,
		SensorLon:           l.SensorLon,
		SensorAlt:           l.SensorAlt,
		HFOV:                l.HFOV,
		VFOV:                l.VFOV,
		RelAzimuth:          l.RelAzimuth,
		RelElevation:        l.RelElevation,
		SlantRange:          l.SlantRange,
		FrameCenterLat:      l.FrameCenterLat,
		FrameCenterLon:      l.FrameCenterLon,
		FrameCenterElev:     l.FrameCenterElev,
		Corners:             make([]defs.APIPathTelemetryPoint, len(l.Corners)),
		Track:               make([]defs.APIPathTelemetryPoint, len(t.track)),
		Packets:             t.packets,
		PacketsInvalid:      t.packetsInvalid,
		PacketsRepaired:     t.packetsFixed,
		Repair:              t.repair.Load(),
	}

	for i, c := range l.Corners {
		item.Corners[i] = defs.APIPathTelemetryPoint{Lat: c.Lat, Lon: c.Lon}
	}
	for i, c := range t.track {
		item.Track[i] = defs.APIPathTelemetryPoint{Lat: c.Lat, Lon: c.Lon}
	}

	return item
}
