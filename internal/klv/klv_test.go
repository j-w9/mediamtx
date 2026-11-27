package klv

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEncodeDecode(t *testing.T) {
	tm := time.Date(2024, 5, 6, 7, 8, 9, 123000000, time.UTC)

	in := &Telemetry{
		Time:                &tm,
		MissionID:           "MISSION01",
		PlatformDesignation: "MQ-9",
		PlatformHeading:     new(159.97),
		PlatformPitch:       new(-0.43),
		SensorLat:           new(60.1768),
		SensorLon:           new(128.4267),
		SensorAlt:           new(14190.7),
		HFOV:                new(144.57),
		FrameCenterLat:      new(-10.5423),
		FrameCenterLon:      new(29.1578),
		Corners: []Point{
			{Lat: -10.54, Lon: 29.15},
			{Lat: -10.54, Lon: 29.16},
			{Lat: -10.55, Lon: 29.16},
			{Lat: -10.55, Lon: 29.15},
		},
	}

	buf := FromTelemetry(in).Encode()

	pkts, err := Split(append(append([]byte{}, buf...), buf...))
	require.NoError(t, err)
	require.Len(t, pkts, 2)

	p, err := Decode(buf)
	require.NoError(t, err)
	require.True(t, p.ChecksumValid)
	require.Equal(t, TagTimestamp, p.Items[0].Tag)

	out := p.Telemetry()
	require.Equal(t, tm, *out.Time)
	require.Equal(t, "MISSION01", out.MissionID)
	require.Equal(t, "MQ-9", out.PlatformDesignation)
	require.InDelta(t, 159.97, *out.PlatformHeading, 0.01)
	require.InDelta(t, -0.43, *out.PlatformPitch, 0.001)
	require.InDelta(t, 60.1768, *out.SensorLat, 0.00001)
	require.InDelta(t, 128.4267, *out.SensorLon, 0.00001)
	require.InDelta(t, 14190.7, *out.SensorAlt, 0.5)
	require.InDelta(t, -10.5423, *out.FrameCenterLat, 0.00001)
	require.Len(t, out.Corners, 4)
	require.InDelta(t, 29.16, out.Corners[2].Lon, 0.00001)
	require.Equal(t, 8, *out.Version)
}

func TestRepair(t *testing.T) {
	now := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	good := FromTelemetry(&Telemetry{Time: &now, SensorLat: new(10.0)}).Encode()

	// conformant packets are left untouched
	r, err := RepairPacket(good, now)
	require.NoError(t, err)
	require.Empty(t, r.Fixes)
	require.Equal(t, good, r.Packet)

	// wrong checksum
	bad := append([]byte{}, good...)
	bad[len(bad)-1] ^= 0xFF
	r, err = RepairPacket(bad, now)
	require.NoError(t, err)
	require.Equal(t, []string{"checksum"}, r.Fixes)
	require.Equal(t, good, r.Packet)

	// no timestamp, no version, no checksum
	p := &Packet{Items: []Item{{Tag: TagSensorLatitude, Value: signedBytes(10, 90, 4)}}}
	raw := append([]byte{}, UASDatalinkKey...)
	raw = append(raw, 6, TagSensorLatitude, 4)
	raw = append(raw, p.Items[0].Value...)
	r, err = RepairPacket(raw, now)
	require.NoError(t, err)
	require.Equal(t, []string{"checksum", "timestamp", "version"}, r.Fixes)

	fixed, err := Decode(r.Packet)
	require.NoError(t, err)
	require.True(t, fixed.ChecksumValid)
	require.Equal(t, now, *fixed.Telemetry().Time)

	// not KLV at all
	_, err = RepairPacket([]byte{1, 2, 3}, now)
	require.Error(t, err)
}
