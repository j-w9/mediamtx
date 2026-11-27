package defs

// APIPeerFeed is a stream of a peer.
type APIPeerFeed struct {
	Name      string   `json:"name"`
	Available bool     `json:"available"`
	Tracks    []string `json:"tracks"`
	// whether the peer makes a compressed copy of the stream.
	Compressed bool `json:"compressed"`
	// name of the local path that pulls the stream, if any, what it has been asked
	// to pull (original, compressed, auto) and what it is pulling at the moment.
	PulledBy      string `json:"pulledBy"`
	PulledMode    string `json:"pulledMode"`
	PulledVariant string `json:"pulledVariant"`
}

// APIPeer is the state of a peer.
type APIPeer struct {
	Name      string `json:"name"`
	URL       string `json:"url"`
	Reachable bool   `json:"reachable"`
	Error     string `json:"error"`
	Version   string `json:"version"`
	// time taken by the peer to answer, in milliseconds.
	LatencyMS float64 `json:"latencyMs"`
	// address to which streams can be pushed, to be followed by the name of the stream.
	// It does not contain credentials.
	PushBase string        `json:"pushBase"`
	Feeds    []APIPeerFeed `json:"feeds"`
}

// APIPeerList is a list of peers.
type APIPeerList struct {
	Items []APIPeer `json:"items"`
}

// APIPeerPull is a request to pull a stream from a peer.
type APIPeerPull struct {
	Peer string `json:"peer"`
	Feed string `json:"feed"`
	// original, compressed or auto.
	Mode string `json:"mode"`
}
