// Package tak announces streams to TAK clients (ATAK, WinTAK, iTAK) and TAK servers
// with Cursor-on-Target messages.
package tak

import (
	"encoding/xml"
	"math"
	"strconv"
	"time"
)

// types of Cursor-on-Target events.
const (
	// friendly unmanned aircraft.
	TypeUAS = "a-f-A-M-F-Q"
	// friendly ground sensor.
	TypeGroundSensor = "a-f-G-E-S"
	// sensor point of interest: what a sensor is looking at.
	TypeSPI = "b-m-p-s-p-i"
)

// value used by TAK when an error is unknown.
const unknownError = "9999999.0"

type cotPoint struct {
	Lat string `xml:"lat,attr"`
	Lon string `xml:"lon,attr"`
	Hae string `xml:"hae,attr"`
	Ce  string `xml:"ce,attr"`
	Le  string `xml:"le,attr"`
}

type cotContact struct {
	Callsign string `xml:"callsign,attr"`
}

type cotTrack struct {
	Course string `xml:"course,attr"`
	Speed  string `xml:"speed,attr"`
}

type cotSensor struct {
	Azimuth   string `xml:"azimuth,attr"`
	Elevation string `xml:"elevation,attr,omitempty"`
	FOV       string `xml:"fov,attr"`
	VFOV      string `xml:"vfov,attr,omitempty"`
	Range     string `xml:"range,attr"`
	Model     string `xml:"model,attr,omitempty"`
	Type      string `xml:"type,attr"`
	North     string `xml:"north,attr"`
	Roll      string `xml:"roll,attr"`
	FOVAlpha  string `xml:"fovAlpha,attr"`
	FOVRed    string `xml:"fovRed,attr"`
	FOVGreen  string `xml:"fovGreen,attr"`
	FOVBlue   string `xml:"fovBlue,attr"`
	Display   string `xml:"displayMagneticReference,attr"`
	HideFOV   string `xml:"hideFov,attr"`
}

type cotConnectionEntry struct {
	NetworkTimeout    string `xml:"networkTimeout,attr"`
	UID               string `xml:"uid,attr"`
	Path              string `xml:"path,attr"`
	Protocol          string `xml:"protocol,attr"`
	BufferTime        string `xml:"bufferTime,attr"`
	Address           string `xml:"address,attr"`
	Port              string `xml:"port,attr"`
	RoverPort         string `xml:"roverPort,attr"`
	RTSPReliable      string `xml:"rtspReliable,attr"`
	IgnoreEmbeddedKLV string `xml:"ignoreEmbeddedKLV,attr"`
	Alias             string `xml:"alias,attr"`
}

type cotVideo struct {
	URL string `xml:"url,attr"`
	UID string `xml:"uid,attr"`
	// marker of what the sensor is looking at
	SPI             string              `xml:"spi,attr,omitempty"`
	ConnectionEntry *cotConnectionEntry `xml:"ConnectionEntry"`
}

type cotLink struct {
	UID      string `xml:"uid,attr"`
	Type     string `xml:"type,attr"`
	Relation string `xml:"relation,attr"`
}

type cotRemarks struct {
	Text string `xml:",chardata"`
}

type cotDetail struct {
	Contact *cotContact `xml:"contact"`
	Track   *cotTrack   `xml:"track"`
	Sensor  *cotSensor  `xml:"sensor"`
	Video   *cotVideo   `xml:"__video"`
	Link    *cotLink    `xml:"link"`
	Remarks *cotRemarks `xml:"remarks"`
}

type cotEvent struct {
	XMLName xml.Name  `xml:"event"`
	Version string    `xml:"version,attr"`
	UID     string    `xml:"uid,attr"`
	Type    string    `xml:"type,attr"`
	Time    string    `xml:"time,attr"`
	Start   string    `xml:"start,attr"`
	Stale   string    `xml:"stale,attr"`
	How     string    `xml:"how,attr"`
	Point   cotPoint  `xml:"point"`
	Detail  cotDetail `xml:"detail"`
}

// Video is a stream that TAK clients can open.
type Video struct {
	// rtsp, rtsps, rtmp, srt, ...
	Protocol string
	Host     string
	Port     int
	// path of the stream, without the leading slash.
	Path string
}

// URL returns the address of the stream.
func (v *Video) URL() string {
	return v.Protocol + "://" + v.Host + ":" + strconv.Itoa(v.Port) + "/" + v.Path
}

// Feed is what is announced about a stream.
type Feed struct {
	UID      string
	Callsign string
	// TypeUAS or TypeGroundSensor.
	Type string

	Lat float64
	Lon float64
	// height above the ellipsoid in meters, when known.
	Alt *float64

	// degrees and meters per second, when known.
	Heading *float64
	Speed   *float64

	// position of what the sensor is looking at, when known.
	TargetLat *float64
	TargetLon *float64
	TargetAlt *float64

	// horizontal and vertical field of view in degrees, when known.
	HFOV *float64
	VFOV *float64

	Model string
	Video *Video
}

func ftoa(v float64, decimals int) string {
	return strconv.FormatFloat(v, 'f', decimals, 64)
}

func cotTime(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

// bearing and distance from a position to another, in degrees and meters.
func bearingDistance(lat1, lon1, lat2, lon2 float64) (float64, float64) {
	const r = 6371000.0
	p1 := lat1 * math.Pi / 180
	p2 := lat2 * math.Pi / 180
	dp := p2 - p1
	dl := (lon2 - lon1) * math.Pi / 180

	y := math.Sin(dl) * math.Cos(p2)
	x := math.Cos(p1)*math.Sin(p2) - math.Sin(p1)*math.Cos(p2)*math.Cos(dl)
	brg := math.Mod(math.Atan2(y, x)*180/math.Pi+360, 360)

	h := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return brg, 2 * r * math.Asin(math.Sqrt(h))
}

func newEvent(
	uid string,
	typ string,
	lat float64,
	lon float64,
	alt *float64,
	now time.Time,
	stale time.Duration,
) *cotEvent {
	hae := unknownError
	if alt != nil {
		hae = ftoa(*alt, 1)
	}

	return &cotEvent{
		Version: "2.0",
		UID:     uid,
		Type:    typ,
		Time:    cotTime(now),
		Start:   cotTime(now),
		Stale:   cotTime(now.Add(stale)),
		How:     "m-g",
		Point: cotPoint{
			Lat: ftoa(lat, 7),
			Lon: ftoa(lon, 7),
			Hae: hae,
			Ce:  unknownError,
			Le:  unknownError,
		},
	}
}

// Events returns the Cursor-on-Target messages that describe a feed: a marker for the
// sensor, that carries the address of the video and the field of view, and, when the sensor
// reports what it is looking at, a second marker for that point.
func Events(f *Feed, now time.Time, stale time.Duration) [][]byte {
	ev := newEvent(f.UID, f.Type, f.Lat, f.Lon, f.Alt, now, stale)
	ev.Detail.Contact = &cotContact{Callsign: f.Callsign}
	ev.Detail.Remarks = &cotRemarks{Text: "MediaMTX"}

	if f.Heading != nil || f.Speed != nil {
		tr := &cotTrack{Course: "0.0", Speed: "0.0"}
		if f.Heading != nil {
			tr.Course = ftoa(*f.Heading, 1)
		}
		if f.Speed != nil {
			tr.Speed = ftoa(*f.Speed, 1)
		}
		ev.Detail.Track = tr
	}

	hasTarget := f.TargetLat != nil && f.TargetLon != nil

	if hasTarget && f.HFOV != nil {
		az, dist := bearingDistance(f.Lat, f.Lon, *f.TargetLat, *f.TargetLon)

		s := &cotSensor{
			Azimuth:  ftoa(az, 1),
			FOV:      ftoa(*f.HFOV, 2),
			Range:    ftoa(dist, 0),
			Model:    f.Model,
			Type:     "r-e",
			North:    "0",
			Roll:     "0",
			FOVAlpha: "0.3",
			FOVRed:   "1.0",
			FOVGreen: "1.0",
			FOVBlue:  "1.0",
			Display:  "0",
			HideFOV:  "false",
		}
		if f.VFOV != nil {
			s.VFOV = ftoa(*f.VFOV, 2)
		}
		if f.Alt != nil {
			targetAlt := 0.0
			if f.TargetAlt != nil {
				targetAlt = *f.TargetAlt
			}
			s.Elevation = ftoa(math.Atan2(targetAlt-*f.Alt, dist)*180/math.Pi, 1)
		}
		ev.Detail.Sensor = s
	}

	if f.Video != nil {
		ev.Detail.Video = &cotVideo{
			URL: f.Video.URL(),
			UID: f.UID + ".video",
			ConnectionEntry: &cotConnectionEntry{
				NetworkTimeout:    "12000",
				UID:               f.UID + ".video",
				Path:              "/" + f.Video.Path,
				Protocol:          f.Video.Protocol,
				BufferTime:        "-1",
				Address:           f.Video.Host,
				Port:              strconv.Itoa(f.Video.Port),
				RoverPort:         "-1",
				RTSPReliable:      "1",
				IgnoreEmbeddedKLV: "false",
				Alias:             f.Callsign,
			},
		}
	}

	if hasTarget && ev.Detail.Video != nil {
		ev.Detail.Video.SPI = f.UID + ".SPI"
	}

	out := [][]byte{marshal(ev)}

	if hasTarget {
		spi := newEvent(f.UID+".SPI", TypeSPI, *f.TargetLat, *f.TargetLon, f.TargetAlt, now, stale)
		spi.Detail.Contact = &cotContact{Callsign: f.Callsign + " SPI"}
		spi.Detail.Link = &cotLink{UID: f.UID, Type: f.Type, Relation: "p-p"}
		out = append(out, marshal(spi))
	}

	return out
}

func marshal(ev *cotEvent) []byte {
	byts, _ := xml.Marshal(ev)
	return append([]byte(xml.Header[:len(xml.Header)-1]), byts...)
}
