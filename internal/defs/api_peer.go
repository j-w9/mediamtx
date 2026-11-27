package defs

// APIPeerFeed is a stream of a peer.
type APIPeerFeed struct {
	Name      string   `json:"name"`
	Available bool     `json:"available"`
	Tracks    []string `json:"tracks"`
	// whether the peer makes a compressed copy of the stream.
	Compressed bool `json:"compressed"`
	// values to use as "source" of a path in order to pull the stream and its compressed copy.
	Source           string `json:"source"`
	SourceCompressed string `json:"sourceCompressed"`
	// name of the local path that pulls the stream, if any.
	PulledBy string `json:"pulledBy"`
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
	// address to which streams can be pushed, followed by the name of the stream.
	PushBase string        `json:"pushBase"`
	Feeds    []APIPeerFeed `json:"feeds"`
}

// APIPeerList is a list of peers.
type APIPeerList struct {
	Items []APIPeer `json:"items"`
}
