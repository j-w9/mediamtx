package tak

import (
	"encoding/xml"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bluenviron/mediamtx/internal/conf"
	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/test"
)

func TestEvents(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

	evs := Events(&Feed{
		UID:       "MediaMTX-host-flight",
		Callsign:  "HAWK21",
		Type:      TypeUAS,
		Lat:       36.2,
		Lon:       -115.03,
		Alt:       new(2400.0),
		Heading:   new(272.0),
		Speed:     new(52.0),
		TargetLat: new(36.235),
		TargetLon: new(-115.034),
		TargetAlt: new(580.0),
		HFOV:      new(3.2),
		VFOV:      new(1.8),
		Model:     "EO",
		Video:     &Video{Protocol: "rtsp", Host: "10.0.0.5", Port: 8554, Path: "flight_compressed"},
	}, now, 10*time.Second)
	require.Len(t, evs, 2)

	// messages are well-formed XML and carry what TAK clients look for
	var ev cotEvent
	require.NoError(t, xml.Unmarshal(evs[0], &ev))
	require.Equal(t, "2.0", ev.Version)
	require.Equal(t, TypeUAS, ev.Type)
	require.Equal(t, "2026-01-02T03:04:05.000Z", ev.Time)
	require.Equal(t, "2026-01-02T03:04:15.000Z", ev.Stale)
	require.Equal(t, "36.2000000", ev.Point.Lat)
	require.Equal(t, "2400.0", ev.Point.Hae)
	require.Equal(t, "HAWK21", ev.Detail.Contact.Callsign)
	require.Equal(t, "272.0", ev.Detail.Track.Course)
	require.Equal(t, "rtsp://10.0.0.5:8554/flight_compressed", ev.Detail.Video.URL)
	require.Equal(t, "MediaMTX-host-flight.video", ev.Detail.Video.UID)
	require.Equal(t, "MediaMTX-host-flight.SPI", ev.Detail.Video.SPI)
	require.Equal(t, "/flight_compressed", ev.Detail.Video.ConnectionEntry.Path)
	require.Equal(t, "10.0.0.5", ev.Detail.Video.ConnectionEntry.Address)
	require.Equal(t, "8554", ev.Detail.Video.ConnectionEntry.Port)
	require.Equal(t, "3.20", ev.Detail.Sensor.FOV)
	// the target is almost exactly north of the sensor, about 3.9 km away and below it
	require.Equal(t, "354.7", ev.Detail.Sensor.Azimuth)
	require.Equal(t, "3908", ev.Detail.Sensor.Range)
	require.True(t, strings.HasPrefix(ev.Detail.Sensor.Elevation, "-"))

	var spi cotEvent
	require.NoError(t, xml.Unmarshal(evs[1], &spi))
	require.Equal(t, TypeSPI, spi.Type)
	require.Equal(t, "MediaMTX-host-flight.SPI", spi.UID)
	require.Equal(t, "36.2350000", spi.Point.Lat)
	require.Equal(t, "MediaMTX-host-flight", spi.Detail.Link.UID)

	// a fixed camera: one marker, no sensor cone
	evs = Events(&Feed{UID: "u", Callsign: "gate <1>", Type: TypeGroundSensor, Lat: 1, Lon: 2}, now, time.Second)
	require.Len(t, evs, 1)
	require.NoError(t, xml.Unmarshal(evs[0], &ev))
	require.Equal(t, "gate <1>", ev.Detail.Contact.Callsign)
	require.Equal(t, unknownError, ev.Point.Hae)
}

func TestFeeds(t *testing.T) {
	base := func() *conf.Path {
		return &conf.Path{TAKShare: true, CompressPathSuffix: "_compressed"}
	}

	camera := base()
	camera.Name = "camera"
	camera.Latitude = 45
	camera.Longitude = 9

	hidden := base()
	hidden.Name = "hidden"
	hidden.Latitude = 45
	hidden.TAKShare = false

	flight := base()
	flight.Name = "flight"
	flight.Compress = true

	flightOut := base()
	flightOut.Name = "flight_compressed"
	flightOut.Compress = true

	nowhere := base()
	nowhere.Name = "nowhere"

	pathConfs := map[string]*conf.Path{
		"camera": camera, "hidden": hidden, "flight": flight, "flight_compressed": flightOut, "nowhere": nowhere,
	}

	tel := &defs.APIPathTelemetry{SensorLat: new(36.2), SensorLon: new(-115.0), CallSign: "HAWK21", HFOV: new(3.0)}

	feeds := Feeds([]defs.APIPath{
		{Name: "camera", Available: true},
		{Name: "hidden", Available: true},
		{Name: "flight", Available: true, Telemetry: tel},
		{Name: "flight_compressed", Available: true, Telemetry: tel},
		{Name: "nowhere", Available: true},
		{Name: "offline", Available: false},
	}, pathConfs, "MediaMTX-h-", "10.0.0.5", 8554)

	require.Len(t, feeds, 2)

	require.Equal(t, "camera", feeds[0].Callsign)
	require.Equal(t, TypeGroundSensor, feeds[0].Type)
	require.Equal(t, "rtsp://10.0.0.5:8554/camera", feeds[0].Video.URL())

	require.Equal(t, "HAWK21", feeds[1].Callsign)
	require.Equal(t, TypeUAS, feeds[1].Type)
	require.Equal(t, "MediaMTX-h-flight", feeds[1].UID)
	// the compressed copy is what clients are sent to
	require.Equal(t, "rtsp://10.0.0.5:8554/flight_compressed", feeds[1].Video.URL())
}

func TestSender(t *testing.T) {
	for _, ca := range []string{"udp", "tcp"} {
		t.Run(ca, func(t *testing.T) {
			recv := make(chan []byte, 4)
			var addr string

			if ca == "udp" {
				pc, err := net.ListenPacket("udp", "127.0.0.1:0")
				require.NoError(t, err)
				defer pc.Close()
				addr = "udp://" + pc.LocalAddr().String()

				go func() {
					buf := make([]byte, 65536)
					for {
						n, _, err2 := pc.ReadFrom(buf)
						if err2 != nil {
							return
						}
						recv <- append([]byte{}, buf[:n]...)
					}
				}()
			} else {
				ln, err := net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, err)
				defer ln.Close()
				addr = "tcp://" + ln.Addr().String()

				go func() {
					conn, err2 := ln.Accept()
					if err2 != nil {
						return
					}
					defer conn.Close()
					buf := make([]byte, 65536)
					for {
						n, err3 := conn.Read(buf)
						if err3 != nil {
							return
						}
						recv <- append([]byte{}, buf[:n]...)
					}
				}()
			}

			require.NoError(t, ValidateAddress(addr))

			p := &Publisher{
				Addresses:   []string{addr},
				Interval:    conf.Duration(500 * time.Millisecond),
				RTSPAddress: ":8554",
				PathManager: &testPathManager{},
				Conf: func() *conf.Conf {
					return &conf.Conf{Paths: map[string]*conf.Path{
						"camera": {Name: "camera", TAKShare: true, Latitude: 45, Longitude: 9},
					}}
				},
				Parent: test.NilLogger,
			}
			require.NoError(t, p.Initialize())
			defer p.Close()

			select {
			case msg := <-recv:
				var ev cotEvent
				require.NoError(t, xml.Unmarshal(msg, &ev))
				require.Equal(t, "camera", ev.Detail.Contact.Callsign)
				// the address of the video is the one of the interface that reaches the destination
				require.Equal(t, "rtsp://127.0.0.1:8554/camera", ev.Detail.Video.URL)
			case <-time.After(5 * time.Second):
				t.Fatal("no message received")
			}
		})
	}
}

type testPathManager struct{}

func (testPathManager) APIPathsList() (*defs.APIPathList, error) {
	return &defs.APIPathList{Items: []defs.APIPath{{Name: "camera", Available: true}}}, nil
}

func TestValidateAddress(t *testing.T) {
	require.NoError(t, ValidateAddress("udp://239.2.3.1:6969"))
	require.NoError(t, ValidateAddress("tls://tak.example.com:8089"))
	require.Error(t, ValidateAddress("http://tak.example.com:8089"))
	require.Error(t, ValidateAddress("tcp://tak.example.com"))
}
