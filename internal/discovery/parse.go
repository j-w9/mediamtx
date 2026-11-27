package discovery

import (
	"encoding/xml"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// sapPacket is a Session Announcement Protocol packet (RFC 2974).
type sapPacket struct {
	// the session is being deleted.
	deletion bool
	origin   net.IP
	sdp      string
}

func parseSAP(buf []byte) (*sapPacket, error) {
	if len(buf) < 8 {
		return nil, fmt.Errorf("packet too short")
	}

	flags := buf[0]
	if flags>>5 != 1 {
		return nil, fmt.Errorf("unsupported version")
	}
	if flags&0x02 != 0 {
		return nil, fmt.Errorf("encrypted announcements are not supported")
	}
	if flags&0x01 != 0 {
		return nil, fmt.Errorf("compressed announcements are not supported")
	}

	p := &sapPacket{deletion: flags&0x04 != 0}

	authLen := int(buf[1]) * 4
	pos := 4

	if flags&0x10 != 0 { // IPv6 origin
		if len(buf) < pos+16 {
			return nil, fmt.Errorf("packet too short")
		}
		p.origin = net.IP(append([]byte{}, buf[pos:pos+16]...))
		pos += 16
	} else {
		p.origin = net.IP(append([]byte{}, buf[pos:pos+4]...))
		pos += 4
	}

	pos += authLen
	if pos >= len(buf) {
		return nil, fmt.Errorf("packet too short")
	}

	payload := buf[pos:]

	// the payload type is optional, and absent when the payload starts with "v=0"
	if !strings.HasPrefix(string(payload), "v=0") {
		i := strings.IndexByte(string(payload), 0)
		if i < 0 {
			return nil, fmt.Errorf("invalid payload type")
		}
		if typ := string(payload[:i]); typ != "application/sdp" {
			return nil, fmt.Errorf("unsupported payload type '%s'", typ)
		}
		payload = payload[i+1:]
	}

	p.sdp = string(payload)
	return p, nil
}

// sdpVideo is the video stream described by a SDP.
type sdpVideo struct {
	name    string
	address string
	port    int
	// whether the stream is MPEG-TS sent directly over UDP, as opposed to RTP.
	rawMPEGTS bool
}

// parseSDPVideo extracts the first video stream from a SDP.
func parseSDPVideo(sdp string) (*sdpVideo, error) {
	v := &sdpVideo{}
	sessionAddr := ""
	inVideo := false
	found := false

	for line := range strings.SplitSeq(strings.ReplaceAll(sdp, "\r\n", "\n"), "\n") {
		if len(line) < 2 || line[1] != '=' {
			continue
		}
		val := line[2:]

		switch line[0] {
		case 's':
			v.name = strings.TrimSpace(val)

		case 'c':
			// c=IN IP4 239.1.1.1/32
			parts := strings.Fields(val)
			if len(parts) == 3 {
				addr, _, _ := strings.Cut(parts[2], "/")
				if !found {
					sessionAddr = addr
				} else if inVideo {
					v.address = addr
				}
			}

		case 'm':
			// m=video 5000 RTP/AVP 33
			parts := strings.Fields(val)
			inVideo = false
			if len(parts) >= 3 && parts[0] == "video" && !found {
				port, err := strconv.Atoi(strings.SplitN(parts[1], "/", 2)[0])
				if err != nil {
					return nil, fmt.Errorf("invalid port")
				}
				v.port = port
				proto := strings.ToUpper(parts[2])
				v.rawMPEGTS = proto == "UDP" || strings.HasPrefix(proto, "MP2T") || proto == "RAW/RAW/UDP"
				found = true
				inVideo = true
			}
		}
	}

	if !found {
		return nil, fmt.Errorf("no video stream")
	}
	if v.address == "" {
		v.address = sessionAddr
	}
	if net.ParseIP(v.address) == nil {
		return nil, fmt.Errorf("invalid address")
	}

	return v, nil
}

// isMPEGTS returns whether a datagram contains MPEG-TS packets.
func isMPEGTS(buf []byte) bool {
	if len(buf) < 188 || len(buf)%188 != 0 {
		return false
	}
	for i := 0; i < len(buf); i += 188 {
		if buf[i] != 0x47 {
			return false
		}
	}
	return true
}

// onvifProbe is the WS-Discovery message that asks ONVIF cameras to show up.
func onvifProbe(messageID string) []byte {
	return []byte(`<?xml version="1.0" encoding="UTF-8"?>` +
		`<e:Envelope xmlns:e="http://www.w3.org/2003/05/soap-envelope"` +
		` xmlns:w="http://schemas.xmlsoap.org/ws/2004/08/addressing"` +
		` xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery"` +
		` xmlns:dn="http://www.onvif.org/ver10/network/wsdl">` +
		`<e:Header><w:MessageID>uuid:` + messageID + `</w:MessageID>` +
		`<w:To e:mustUnderstand="true">urn:schemas-xmlsoap-org:ws:2005:04:discovery</w:To>` +
		`<w:Action e:mustUnderstand="true">http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</w:Action>` +
		`</e:Header><e:Body><d:Probe><d:Types>dn:NetworkVideoTransmitter</d:Types></d:Probe></e:Body></e:Envelope>`)
}

var (
	reXAddrs = regexp.MustCompile(`<[^>]*XAddrs[^>]*>([^<]*)<`)
	reScopes = regexp.MustCompile(`<[^>]*Scopes[^>]*>([^<]*)<`)
)

// onvifDevice is a camera that answered a probe.
type onvifDevice struct {
	host string
	name string
}

func parseONVIFProbeMatch(buf []byte) (*onvifDevice, error) {
	m := reXAddrs.FindSubmatch(buf)
	if m == nil {
		return nil, fmt.Errorf("no address found")
	}

	var host string
	for addr := range strings.FieldsSeq(string(m[1])) {
		u, err := url.Parse(addr)
		if err != nil || u.Hostname() == "" {
			continue
		}
		// prefer IPv4 addresses, which is what cameras are usually reached with
		if ip := net.ParseIP(u.Hostname()); ip != nil && ip.To4() == nil && host != "" {
			continue
		}
		host = u.Hostname()
		if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
			break
		}
	}
	if host == "" {
		return nil, fmt.Errorf("no address found")
	}

	d := &onvifDevice{host: host}

	if s := reScopes.FindSubmatch(buf); s != nil {
		var name, hardware string
		for scope := range strings.FieldsSeq(string(s[1])) {
			if v, ok := strings.CutPrefix(scope, "onvif://www.onvif.org/name/"); ok {
				name, _ = url.PathUnescape(v)
			} else if v2, ok2 := strings.CutPrefix(scope, "onvif://www.onvif.org/hardware/"); ok2 {
				hardware, _ = url.PathUnescape(v2)
			}
		}
		d.name = strings.TrimSpace(name + " " + hardware)
	}

	return d, nil
}

// cotVideo is a video stream announced by a TAK client or server.
type cotVideo struct {
	uid      string
	callsign string
	url      string
}

func parseCoTVideo(buf []byte) (*cotVideo, error) {
	var ev struct {
		UID    string `xml:"uid,attr"`
		Detail struct {
			Contact struct {
				Callsign string `xml:"callsign,attr"`
			} `xml:"contact"`
			Video struct {
				URL             string `xml:"url,attr"`
				ConnectionEntry struct {
					Protocol string `xml:"protocol,attr"`
					Address  string `xml:"address,attr"`
					Port     string `xml:"port,attr"`
					Path     string `xml:"path,attr"`
				} `xml:"ConnectionEntry"`
			} `xml:"__video"`
		} `xml:"detail"`
	}

	err := xml.Unmarshal(buf, &ev)
	if err != nil {
		return nil, err
	}

	v := &cotVideo{uid: ev.UID, callsign: ev.Detail.Contact.Callsign, url: ev.Detail.Video.URL}

	if ce := ev.Detail.Video.ConnectionEntry; v.url == "" && ce.Address != "" && ce.Protocol != "" {
		v.url = ce.Protocol + "://" + ce.Address
		if ce.Port != "" && ce.Port != "-1" {
			v.url += ":" + ce.Port
		}
		v.url += ce.Path
	}

	if v.url == "" {
		return nil, fmt.Errorf("no video")
	}

	u, err := url.Parse(v.url)
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("invalid video address")
	}

	return v, nil
}
