package telemetry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/bluenviron/mediamtx/internal/klv"
)

func packet(lat float64, lon float64) []byte {
	now := time.Now()
	return klv.FromTelemetry(&klv.Telemetry{
		Time:           &now,
		CallSign:       "HAWK21",
		SensorLat:      new(lat),
		SensorLon:      new(lon),
		FrameCenterLat: new(lat + 0.01),
		FrameCenterLon: new(lon + 0.01),
	}).Encode()
}

func TestTracker(t *testing.T) {
	tr := &Tracker{}
	require.Nil(t, tr.APIItem())

	good := packet(10, 20)

	// a unit can carry several packets, and other kinds of KLV
	other := append([]byte{0x06, 0x0E, 0x2B, 0x34, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1}, 2, 0xAA, 0xBB)
	unit := append(append(append([]byte{}, good...), other...), good...)

	// without repair, units are passed through untouched
	require.Equal(t, unit, tr.Process(unit))

	item := tr.APIItem()
	require.Equal(t, uint64(2), item.Packets)
	require.Equal(t, uint64(0), item.PacketsInvalid)
	require.Equal(t, "HAWK21", item.CallSign)
	require.InDelta(t, 10, *item.SensorLat, 0.0001)
	require.Len(t, item.Track, 1) // points are at least 2 seconds apart

	// a damaged packet is counted, and left alone
	bad := append([]byte{}, good...)
	bad[len(bad)-1] ^= 0xFF
	require.Equal(t, bad, tr.Process(bad))
	require.Equal(t, uint64(1), tr.APIItem().PacketsInvalid)

	// with repair, it is fixed, while the rest of the unit is preserved
	tr.SetRepair(true)
	unit = append(append(append([]byte{}, bad...), other...), good...)
	out := tr.Process(unit)
	require.Equal(t, append(append(append([]byte{}, good...), other...), good...), out)

	item = tr.APIItem()
	require.Equal(t, uint64(1), item.PacketsRepaired)
	require.True(t, item.Repair)

	// conformant units are not rewritten
	require.Equal(t, good, tr.Process(good))

	// fields that are missing from a packet keep their last value
	now := time.Now()
	tr.Process(klv.FromTelemetry(&klv.Telemetry{Time: &now, PlatformHeading: new(90.0)}).Encode())
	item = tr.APIItem()
	require.InDelta(t, 90, *item.PlatformHeading, 0.01)
	require.InDelta(t, 10, *item.SensorLat, 0.0001)
}

func TestTrackerTrackRestart(t *testing.T) {
	tr := &Tracker{}

	tr.Process(packet(10, 20))
	tr.lastTrackPoint = time.Time{}
	tr.Process(packet(10.001, 20.001))
	require.Len(t, tr.APIItem().Track, 2)

	// the source is replaced with one that is somewhere else
	tr.lastTrackPoint = time.Time{}
	tr.Process(packet(50, -100))
	item := tr.APIItem()
	require.Len(t, item.Track, 1)
	require.InDelta(t, 50, item.Track[0].Lat, 0.0001)
}
