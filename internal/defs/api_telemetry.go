package defs

import "time"

// APIPathTelemetryPoint is a position on the ground.
type APIPathTelemetryPoint struct {
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// APIPathTelemetry is the position of the sensor of a path and of what it is looking at,
// read from KLV metadata (MISB ST 0601). Angles are in degrees, distances in meters,
// speeds in meters per second. Values that are not present in the metadata are null.
type APIPathTelemetry struct {
	// time written by the sensor.
	Time *time.Time `json:"time"`
	// time at which the last packet was received.
	LastPacket time.Time `json:"lastPacket"`

	MissionID           string `json:"missionID"`
	TailNumber          string `json:"tailNumber"`
	PlatformDesignation string `json:"platformDesignation"`
	CallSign            string `json:"callSign"`
	Sensor              string `json:"sensor"`
	Version             *int   `json:"version"`

	PlatformHeading *float64 `json:"platformHeading"`
	PlatformPitch   *float64 `json:"platformPitch"`
	PlatformRoll    *float64 `json:"platformRoll"`
	GroundSpeed     *float64 `json:"groundSpeed"`

	SensorLat *float64 `json:"sensorLat"`
	SensorLon *float64 `json:"sensorLon"`
	SensorAlt *float64 `json:"sensorAlt"`

	HFOV         *float64 `json:"hfov"`
	VFOV         *float64 `json:"vfov"`
	RelAzimuth   *float64 `json:"relAzimuth"`
	RelElevation *float64 `json:"relElevation"`
	SlantRange   *float64 `json:"slantRange"`

	FrameCenterLat  *float64 `json:"frameCenterLat"`
	FrameCenterLon  *float64 `json:"frameCenterLon"`
	FrameCenterElev *float64 `json:"frameCenterElev"`

	// corners of the picture on the ground: upper left, upper right, lower right, lower left.
	Corners []APIPathTelemetryPoint `json:"corners"`
	// recent positions of the sensor, oldest first.
	Track []APIPathTelemetryPoint `json:"track"`

	Packets         uint64 `json:"packets"`
	PacketsInvalid  uint64 `json:"packetsInvalid"`
	PacketsRepaired uint64 `json:"packetsRepaired"`
	Repair          bool   `json:"repair"`
}
