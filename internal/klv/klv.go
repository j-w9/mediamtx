// Package klv contains a decoder, an encoder and a repair tool for
// MISB ST 0601 (UAS Datalink Local Set) metadata, which describes where
// an airborne sensor is and what it is looking at.
package klv

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"time"
)

// UASDatalinkKey is the universal label that opens a ST 0601 packet.
var UASDatalinkKey = []byte{
	0x06, 0x0E, 0x2B, 0x34, 0x02, 0x0B, 0x01, 0x01,
	0x0E, 0x01, 0x03, 0x01, 0x01, 0x00, 0x00, 0x00,
}

// tags of ST 0601.
const (
	TagChecksum            = 1
	TagTimestamp           = 2
	TagMissionID           = 3
	TagTailNumber          = 4
	TagPlatformHeading     = 5
	TagPlatformPitch       = 6
	TagPlatformRoll        = 7
	TagPlatformDesignation = 10
	TagImageSourceSensor   = 11
	TagSensorLatitude      = 13
	TagSensorLongitude     = 14
	TagSensorAltitude      = 15
	TagSensorHFOV          = 16
	TagSensorVFOV          = 17
	TagSensorRelAzimuth    = 18
	TagSensorRelElevation  = 19
	TagSensorRelRoll       = 20
	TagSlantRange          = 21
	TagTargetWidth         = 22
	TagFrameCenterLat      = 23
	TagFrameCenterLon      = 24
	TagFrameCenterElev     = 25
	TagOffsetCornerLat1    = 26 // 26-33: latitude and longitude offsets of the 4 corners
	TagPlatformGroundSpeed = 56
	TagPlatformCallSign    = 59
	TagVersion             = 65
	TagCornerLat1          = 82 // 82-89: latitude and longitude of the 4 corners
)

// version written in packets that lack one.
const defaultVersion = 8

// Item is an element of a local set.
type Item struct {
	Tag   int
	Value []byte
}

// Packet is a ST 0601 packet.
type Packet struct {
	Items []Item

	// whether the checksum of the decoded packet was present and correct.
	ChecksumValid bool
}

// Checksum computes the checksum of a packet.
// buf must contain the packet from the first byte of the key
// up to and including the length byte of the checksum item.
func Checksum(buf []byte) uint16 {
	var sum uint16
	for i, b := range buf {
		if i%2 == 0 {
			sum += uint16(b) << 8
		} else {
			sum += uint16(b)
		}
	}
	return sum
}

// readBERLength reads a BER length and returns it with the number of bytes read.
func readBERLength(buf []byte) (int, int, error) {
	if len(buf) == 0 {
		return 0, 0, fmt.Errorf("missing length")
	}

	if buf[0] < 0x80 {
		return int(buf[0]), 1, nil
	}

	n := int(buf[0] & 0x7F)
	if n == 0 || n > 4 || len(buf) < 1+n {
		return 0, 0, fmt.Errorf("invalid length")
	}

	v := 0
	for _, b := range buf[1 : 1+n] {
		v = v<<8 | int(b)
	}

	return v, 1 + n, nil
}

func appendBERLength(buf []byte, v int) []byte {
	switch {
	case v < 0x80:
		return append(buf, byte(v))
	case v <= 0xFF:
		return append(buf, 0x81, byte(v))
	default:
		return append(buf, 0x82, byte(v>>8), byte(v))
	}
}

// readBEROID reads a tag, encoded as BER-OID.
func readBEROID(buf []byte) (int, int, error) {
	v := 0
	for i, b := range buf {
		if i == 3 {
			break
		}
		v = v<<7 | int(b&0x7F)
		if b&0x80 == 0 {
			return v, i + 1, nil
		}
	}
	return 0, 0, fmt.Errorf("invalid tag")
}

func appendBEROID(buf []byte, v int) []byte {
	if v < 0x80 {
		return append(buf, byte(v))
	}
	if v < 0x4000 {
		return append(buf, byte(v>>7)|0x80, byte(v&0x7F))
	}
	return append(buf, byte(v>>14)|0x80, byte(v>>7)|0x80, byte(v&0x7F))
}

// Split splits a buffer that contains one or more KLV packets.
// Packets of any kind are returned, each one with its key and length.
func Split(buf []byte) ([][]byte, error) {
	var ret [][]byte

	for len(buf) > 0 {
		if len(buf) < 17 {
			return ret, fmt.Errorf("truncated packet")
		}

		l, n, err := readBERLength(buf[16:])
		if err != nil {
			return ret, err
		}

		end := 16 + n + l
		if end > len(buf) {
			return ret, fmt.Errorf("truncated packet")
		}

		ret = append(ret, buf[:end])
		buf = buf[end:]
	}

	return ret, nil
}

// IsUASDatalink returns whether a packet is a ST 0601 packet.
func IsUASDatalink(buf []byte) bool {
	return len(buf) >= 16 && bytes.Equal(buf[:16], UASDatalinkKey)
}

// Decode decodes a ST 0601 packet.
// Items that are damaged beyond the point where they start are discarded
// and reported with an error, together with the items decoded up to that point.
func Decode(buf []byte) (*Packet, error) {
	if !IsUASDatalink(buf) {
		return nil, fmt.Errorf("not a ST 0601 packet")
	}

	l, n, err := readBERLength(buf[16:])
	if err != nil {
		return nil, err
	}

	start := 16 + n
	if start+l > len(buf) {
		return nil, fmt.Errorf("truncated packet")
	}

	p := &Packet{}
	body := buf[start : start+l]
	pos := 0

	for pos < len(body) {
		tag, tn, err := readBEROID(body[pos:])
		if err != nil {
			return p, err
		}

		il, ln, err := readBERLength(body[pos+tn:])
		if err != nil {
			return p, err
		}

		vstart := pos + tn + ln
		if vstart+il > len(body) {
			return p, fmt.Errorf("item %d exceeds the packet", tag)
		}

		if tag == TagChecksum {
			if il == 2 {
				expected := Checksum(buf[:start+vstart])
				p.ChecksumValid = binary.BigEndian.Uint16(body[vstart:]) == expected
			}
		} else {
			p.Items = append(p.Items, Item{Tag: tag, Value: body[vstart : vstart+il]})
		}

		pos = vstart + il
	}

	return p, nil
}

// Get returns the value of an item.
func (p *Packet) Get(tag int) []byte {
	for _, it := range p.Items {
		if it.Tag == tag {
			return it.Value
		}
	}
	return nil
}

// Set sets or replaces the value of an item.
func (p *Packet) Set(tag int, value []byte) {
	for i, it := range p.Items {
		if it.Tag == tag {
			p.Items[i].Value = value
			return
		}
	}
	p.Items = append(p.Items, Item{Tag: tag, Value: value})
}

// Encode encodes the packet. The timestamp is written first and the checksum,
// which is computed here, is written last, as required by the standard.
func (p *Packet) Encode() []byte {
	var body []byte

	writeItem := func(it Item) {
		body = appendBEROID(body, it.Tag)
		body = appendBERLength(body, len(it.Value))
		body = append(body, it.Value...)
	}

	for _, it := range p.Items {
		if it.Tag == TagTimestamp {
			writeItem(it)
		}
	}

	for _, it := range p.Items {
		if it.Tag != TagTimestamp && it.Tag != TagChecksum {
			writeItem(it)
		}
	}

	// the length includes the 4 bytes of the checksum item
	buf := append([]byte{}, UASDatalinkKey...)
	buf = appendBERLength(buf, len(body)+4)
	buf = append(buf, body...)
	buf = append(buf, TagChecksum, 2)
	buf = binary.BigEndian.AppendUint16(buf, Checksum(buf))

	return buf
}

func unsignedValue(v []byte) (uint64, bool) {
	if len(v) == 0 || len(v) > 8 {
		return 0, false
	}
	var u uint64
	for _, b := range v {
		u = u<<8 | uint64(b)
	}
	return u, true
}

// mapUnsigned converts an unsigned integer into a value between min and max.
func mapUnsigned(v []byte, minV float64, maxV float64) *float64 {
	u, ok := unsignedValue(v)
	if !ok {
		return nil
	}
	full := math.Pow(2, float64(8*len(v))) - 1
	out := minV + float64(u)*(maxV-minV)/full
	return &out
}

// mapSigned converts a signed integer into a value between -limit and limit.
// The most negative integer means that the value is not available.
func mapSigned(v []byte, limit float64) *float64 {
	u, ok := unsignedValue(v)
	if !ok {
		return nil
	}
	bits := uint(8 * len(v))
	if u == 1<<(bits-1) {
		return nil
	}
	s := int64(u)
	if u&(1<<(bits-1)) != 0 {
		s = int64(u) - (1 << bits)
	}
	out := float64(s) * limit / (math.Pow(2, float64(bits-1)) - 1)
	return &out
}

func signedBytes(value float64, limit float64, size int) []byte {
	if value > limit {
		value = limit
	} else if value < -limit {
		value = -limit
	}
	s := int64(math.Round(value / limit * (math.Pow(2, float64(8*size-1)) - 1)))
	buf := make([]byte, size)
	for i := size - 1; i >= 0; i-- {
		buf[i] = byte(s)
		s >>= 8
	}
	return buf
}

func unsignedBytes(value float64, minV float64, maxV float64, size int) []byte {
	if value > maxV {
		value = maxV
	} else if value < minV {
		value = minV
	}
	u := uint64(math.Round((value - minV) / (maxV - minV) * (math.Pow(2, float64(8*size)) - 1)))
	buf := make([]byte, size)
	for i := size - 1; i >= 0; i-- {
		buf[i] = byte(u)
		u >>= 8
	}
	return buf
}

// Point is a position on the ground.
type Point struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// Telemetry is the content of a ST 0601 packet, in engineering units
// (degrees, meters, meters per second). Fields that are absent are nil.
type Telemetry struct {
	Time                *time.Time
	MissionID           string
	TailNumber          string
	PlatformDesignation string
	CallSign            string
	Sensor              string
	Version             *int

	PlatformHeading *float64
	PlatformPitch   *float64
	PlatformRoll    *float64
	GroundSpeed     *float64

	SensorLat *float64
	SensorLon *float64
	SensorAlt *float64

	HFOV         *float64
	VFOV         *float64
	RelAzimuth   *float64
	RelElevation *float64
	RelRoll      *float64

	SlantRange  *float64
	TargetWidth *float64

	FrameCenterLat  *float64
	FrameCenterLon  *float64
	FrameCenterElev *float64

	// corners of the picture on the ground: upper left, upper right, lower right, lower left.
	Corners []Point
}

// Telemetry converts the packet into engineering units.
func (p *Packet) Telemetry() *Telemetry {
	t := &Telemetry{}

	for _, it := range p.Items {
		v := it.Value

		switch it.Tag {
		case TagTimestamp:
			if len(v) == 8 {
				us := binary.BigEndian.Uint64(v)
				if us != 0 && us < math.MaxInt64 {
					tm := time.UnixMicro(int64(us)).UTC()
					t.Time = &tm
				}
			}
		case TagMissionID:
			t.MissionID = string(v)
		case TagTailNumber:
			t.TailNumber = string(v)
		case TagPlatformDesignation:
			t.PlatformDesignation = string(v)
		case TagPlatformCallSign:
			t.CallSign = string(v)
		case TagImageSourceSensor:
			t.Sensor = string(v)
		case TagVersion:
			if len(v) == 1 {
				ver := int(v[0])
				t.Version = &ver
			}
		case TagPlatformHeading:
			t.PlatformHeading = mapUnsigned(v, 0, 360)
		case TagPlatformPitch:
			t.PlatformPitch = mapSigned(v, 20)
		case TagPlatformRoll:
			t.PlatformRoll = mapSigned(v, 50)
		case TagPlatformGroundSpeed:
			t.GroundSpeed = mapUnsigned(v, 0, 255)
		case TagSensorLatitude:
			t.SensorLat = mapSigned(v, 90)
		case TagSensorLongitude:
			t.SensorLon = mapSigned(v, 180)
		case TagSensorAltitude:
			t.SensorAlt = mapUnsigned(v, -900, 19000)
		case TagSensorHFOV:
			t.HFOV = mapUnsigned(v, 0, 180)
		case TagSensorVFOV:
			t.VFOV = mapUnsigned(v, 0, 180)
		case TagSensorRelAzimuth:
			t.RelAzimuth = mapUnsigned(v, 0, 360)
		case TagSensorRelElevation:
			t.RelElevation = mapSigned(v, 180)
		case TagSensorRelRoll:
			t.RelRoll = mapUnsigned(v, 0, 360)
		case TagSlantRange:
			t.SlantRange = mapUnsigned(v, 0, 5000000)
		case TagTargetWidth:
			t.TargetWidth = mapUnsigned(v, 0, 10000)
		case TagFrameCenterLat:
			t.FrameCenterLat = mapSigned(v, 90)
		case TagFrameCenterLon:
			t.FrameCenterLon = mapSigned(v, 180)
		case TagFrameCenterElev:
			t.FrameCenterElev = mapUnsigned(v, -900, 19000)
		}
	}

	t.Corners = p.corners(t)

	return t
}

// corners returns the corners of the picture. Full coordinates (tags 82-89) are preferred
// to offsets from the frame center (tags 26-33).
func (p *Packet) corners(t *Telemetry) []Point {
	full := make([]Point, 0, 4)
	for i := range 4 {
		lat := mapSigned(p.Get(TagCornerLat1+2*i), 90)
		lon := mapSigned(p.Get(TagCornerLat1+2*i+1), 180)
		if lat == nil || lon == nil {
			break
		}
		full = append(full, Point{Lat: *lat, Lon: *lon})
	}
	if len(full) == 4 {
		return full
	}

	if t.FrameCenterLat == nil || t.FrameCenterLon == nil {
		return nil
	}

	offs := make([]Point, 0, 4)
	for i := range 4 {
		lat := mapSigned(p.Get(TagOffsetCornerLat1+2*i), 0.075)
		lon := mapSigned(p.Get(TagOffsetCornerLat1+2*i+1), 0.075)
		if lat == nil || lon == nil {
			return nil
		}
		offs = append(offs, Point{Lat: *t.FrameCenterLat + *lat, Lon: *t.FrameCenterLon + *lon})
	}

	return offs
}

// FromTelemetry builds a packet. Only the fields that are set are written.
func FromTelemetry(t *Telemetry) *Packet {
	p := &Packet{}

	if t.Time != nil {
		p.Set(TagTimestamp, binary.BigEndian.AppendUint64(nil, uint64(t.Time.UnixMicro())))
	}

	str := func(tag int, v string) {
		if v != "" {
			p.Set(tag, []byte(v))
		}
	}
	signed := func(tag int, v *float64, limit float64, size int) {
		if v != nil {
			p.Set(tag, signedBytes(*v, limit, size))
		}
	}
	unsigned := func(tag int, v *float64, minV float64, maxV float64, size int) {
		if v != nil {
			p.Set(tag, unsignedBytes(*v, minV, maxV, size))
		}
	}

	str(TagMissionID, t.MissionID)
	str(TagTailNumber, t.TailNumber)
	unsigned(TagPlatformHeading, t.PlatformHeading, 0, 360, 2)
	signed(TagPlatformPitch, t.PlatformPitch, 20, 2)
	signed(TagPlatformRoll, t.PlatformRoll, 50, 2)
	str(TagPlatformDesignation, t.PlatformDesignation)
	str(TagImageSourceSensor, t.Sensor)
	signed(TagSensorLatitude, t.SensorLat, 90, 4)
	signed(TagSensorLongitude, t.SensorLon, 180, 4)
	unsigned(TagSensorAltitude, t.SensorAlt, -900, 19000, 2)
	unsigned(TagSensorHFOV, t.HFOV, 0, 180, 2)
	unsigned(TagSensorVFOV, t.VFOV, 0, 180, 2)
	unsigned(TagSensorRelAzimuth, t.RelAzimuth, 0, 360, 4)
	signed(TagSensorRelElevation, t.RelElevation, 180, 4)
	unsigned(TagSensorRelRoll, t.RelRoll, 0, 360, 4)
	unsigned(TagSlantRange, t.SlantRange, 0, 5000000, 4)
	unsigned(TagTargetWidth, t.TargetWidth, 0, 10000, 2)
	signed(TagFrameCenterLat, t.FrameCenterLat, 90, 4)
	signed(TagFrameCenterLon, t.FrameCenterLon, 180, 4)
	unsigned(TagFrameCenterElev, t.FrameCenterElev, -900, 19000, 2)
	unsigned(TagPlatformGroundSpeed, t.GroundSpeed, 0, 255, 1)
	str(TagPlatformCallSign, t.CallSign)

	if len(t.Corners) == 4 {
		for i, c := range t.Corners {
			p.Set(TagCornerLat1+2*i, signedBytes(c.Lat, 90, 4))
			p.Set(TagCornerLat1+2*i+1, signedBytes(c.Lon, 180, 4))
		}
	}

	version := defaultVersion
	if t.Version != nil {
		version = *t.Version
	}
	p.Set(TagVersion, []byte{byte(version)})

	return p
}

// Repair is the outcome of repairing a packet.
type Repair struct {
	// the repaired packet, which is the original one when nothing had to be fixed.
	Packet []byte
	// what has been fixed.
	Fixes []string
}

// earliest timestamp considered plausible.
var minTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// RepairPacket makes a ST 0601 packet conformant, so that strict receivers accept it:
//   - a missing or wrong checksum is recomputed
//   - the timestamp is moved first and the checksum last
//   - a missing or implausible timestamp is replaced with the current time
//   - a missing version number is added
//   - items damaged beyond repair, at the end of the packet, are dropped
//
// It returns an error when nothing can be recovered.
func RepairPacket(buf []byte, now time.Time) (*Repair, error) {
	p, err := Decode(buf)
	if p == nil {
		return nil, err
	}

	var fixes []string

	if err != nil {
		if len(p.Items) == 0 {
			return nil, err
		}
		fixes = append(fixes, "dropped damaged items")
	}

	if !p.ChecksumValid {
		fixes = append(fixes, "checksum")
	}

	ts := p.Get(TagTimestamp)
	valid := false
	if len(ts) == 8 {
		us := binary.BigEndian.Uint64(ts)
		if us < math.MaxInt64 {
			tm := time.UnixMicro(int64(us))
			valid = tm.After(minTime) && tm.Before(now.Add(24*time.Hour))
		}
	}
	if !valid {
		p.Set(TagTimestamp, binary.BigEndian.AppendUint64(nil, uint64(now.UnixMicro())))
		fixes = append(fixes, "timestamp")
	} else if p.Items[0].Tag != TagTimestamp {
		fixes = append(fixes, "item order")
	}

	if p.Get(TagVersion) == nil {
		p.Set(TagVersion, []byte{defaultVersion})
		fixes = append(fixes, "version")
	}

	if len(fixes) == 0 {
		// the checksum must also be the last item
		reencoded := p.Encode()
		if bytes.Equal(reencoded, buf) {
			return &Repair{Packet: buf}, nil
		}
		return &Repair{Packet: reencoded, Fixes: []string{"item order"}}, nil
	}

	return &Repair{Packet: p.Encode(), Fixes: fixes}, nil
}
