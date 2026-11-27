package defs

import "time"

// APIDiscoveredSource is a video source found on the network.
type APIDiscoveredSource struct {
	ID string `json:"id"`
	// how the source was found: sap, multicast, onvif, tak.
	Method string `json:"method"`
	Name   string `json:"name"`
	// value to use as "source" of a path in order to read the source.
	Source string `json:"source"`
	// additional path settings needed to read the source.
	Conf    map[string]string `json:"conf"`
	Address string            `json:"address"`
	Info    string            `json:"info"`
	// whether a path that reads the source exists already.
	Added     bool      `json:"added"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
}

// APIDiscoveredSourceList is a list of discovered sources.
type APIDiscoveredSourceList struct {
	// whether discovery is enabled.
	Enabled bool                  `json:"enabled"`
	Items   []APIDiscoveredSource `json:"items"`
}
