package conf

import (
	"fmt"
	"net/url"
	"regexp"
)

var rePeerName = regexp.MustCompile(`^[0-9a-zA-Z_\-\.]+$`)

// Peer is another server whose streams can be listed and pulled.
type Peer struct {
	Name string `json:"name"`
	// address of the API of the peer.
	URL string `json:"url"`
	// credentials used to call the API of the peer and to read its streams.
	User        string `json:"user"`
	Pass        string `json:"pass"`
	Fingerprint string `json:"fingerprint"`
}

// Peers is a list of peers.
type Peers []Peer

// Validate validates the peers.
func (ps Peers) Validate() error {
	names := make(map[string]struct{})

	for _, p := range ps {
		if !rePeerName.MatchString(p.Name) {
			return fmt.Errorf("invalid peer name '%s': it can contain only alphanumeric characters, "+
				"underscore, dot, minus", p.Name)
		}

		if _, ok := names[p.Name]; ok {
			return fmt.Errorf("peer '%s' is defined twice", p.Name)
		}
		names[p.Name] = struct{}{}

		u, err := url.Parse(p.URL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("invalid URL of peer '%s': it must be the address of its API, "+
				"for instance http://192.168.1.10:9997", p.Name)
		}
	}

	return nil
}
