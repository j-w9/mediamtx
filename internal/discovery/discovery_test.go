package discovery

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/bluenviron/mediamtx/internal/defs"
	"github.com/bluenviron/mediamtx/internal/test"
)

const testSDP = "v=0\r\n" +
	"o=- 1 1 IN IP4 10.0.0.7\r\n" +
	"s=Sensor 1\r\n" +
	"c=IN IP4 239.1.1.1/32\r\n" +
	"t=0 0\r\n" +
	"m=video 5000 RTP/AVP 33\r\n"

func sapPacketBytes(deletion bool, sdp string) []byte {
	flags := byte(0x20)
	if deletion {
		flags |= 0x04
	}
	buf := []byte{flags, 0, 0x12, 0x34, 10, 0, 0, 7}
	buf = append(buf, []byte("application/sdp\x00")...)
	return append(buf, []byte(sdp)...)
}

func TestParseSAP(t *testing.T) {
	p, err := parseSAP(sapPacketBytes(false, testSDP))
	require.NoError(t, err)
	require.False(t, p.deletion)
	require.Equal(t, "10.0.0.7", p.origin.String())
	require.Equal(t, testSDP, p.sdp)

	// the payload type is optional
	p, err = parseSAP(append([]byte{0x20, 0, 0, 1, 10, 0, 0, 7}, []byte(testSDP)...))
	require.NoError(t, err)
	require.Equal(t, testSDP, p.sdp)

	_, err = parseSAP([]byte{0x20, 0})
	require.Error(t, err)

	// encrypted
	_, err = parseSAP(append([]byte{0x22, 0, 0, 1, 10, 0, 0, 7}, []byte(testSDP)...))
	require.Error(t, err)
}

func TestParseSDPVideo(t *testing.T) {
	v, err := parseSDPVideo(testSDP)
	require.NoError(t, err)
	require.Equal(t, &sdpVideo{name: "Sensor 1", address: "239.1.1.1", port: 5000}, v)

	// MPEG-TS directly over UDP, address at media level
	v, err = parseSDPVideo("v=0\ns=X\nm=audio 4000 RTP/AVP 0\nc=IN IP4 239.9.9.9\n" +
		"m=video 6000 udp 33\nc=IN IP4 239.2.2.2/16\n")
	require.NoError(t, err)
	require.Equal(t, &sdpVideo{name: "X", address: "239.2.2.2", port: 6000, rawMPEGTS: true}, v)

	_, err = parseSDPVideo("v=0\ns=X\nm=audio 4000 RTP/AVP 0\n")
	require.Error(t, err)
}

func TestIsMPEGTS(t *testing.T) {
	pkt := make([]byte, 188*7)
	for i := 0; i < len(pkt); i += 188 {
		pkt[i] = 0x47
	}
	require.True(t, isMPEGTS(pkt))
	require.False(t, isMPEGTS(pkt[:200]))
	pkt[188] = 0
	require.False(t, isMPEGTS(pkt))
}

func TestParseONVIF(t *testing.T) {
	reply := `<?xml version="1.0"?><SOAP-ENV:Envelope><SOAP-ENV:Body><d:ProbeMatches><d:ProbeMatch>` +
		`<d:Types>dn:NetworkVideoTransmitter</d:Types>` +
		`<d:Scopes>onvif://www.onvif.org/type/video_encoder onvif://www.onvif.org/name/Front%20Gate ` +
		`onvif://www.onvif.org/hardware/DS-2CD2143</d:Scopes>` +
		`<d:XAddrs>http://[fe80::1]/onvif/device_service http://192.168.1.64/onvif/device_service</d:XAddrs>` +
		`</d:ProbeMatch></d:ProbeMatches></SOAP-ENV:Body></SOAP-ENV:Envelope>`

	d, err := parseONVIFProbeMatch([]byte(reply))
	require.NoError(t, err)
	require.Equal(t, &onvifDevice{host: "192.168.1.64", name: "Front Gate DS-2CD2143"}, d)

	_, err = parseONVIFProbeMatch([]byte("<x/>"))
	require.Error(t, err)
}

func TestParseCoTVideo(t *testing.T) {
	v, err := parseCoTVideo([]byte(`<event version="2.0" uid="UAS-1" type="a-f-A-M-F-Q"><point lat="1" lon="2"/>` +
		`<detail><contact callsign="RAVEN"/><__video url="rtsp://10.0.0.9:8554/live"/></detail></event>`))
	require.NoError(t, err)
	require.Equal(t, &cotVideo{uid: "UAS-1", callsign: "RAVEN", url: "rtsp://10.0.0.9:8554/live"}, v)

	// address given by the connection entry only
	v, err = parseCoTVideo([]byte(`<event uid="UAS-2"><detail><contact callsign="B"/><__video>` +
		`<ConnectionEntry protocol="rtsp" address="10.0.0.9" port="554" path="/cam"/></__video></detail></event>`))
	require.NoError(t, err)
	require.Equal(t, "rtsp://10.0.0.9:554/cam", v.url)

	// a position report without video
	_, err = parseCoTVideo([]byte(`<event uid="A"><detail><contact callsign="B"/></detail></event>`))
	require.Error(t, err)
}

func TestFinderSources(t *testing.T) {
	f := &Finder{OwnTAKPrefix: "MediaMTX-me-", Parent: test.NilLogger}
	f.sources = map[string]*defs.APIDiscoveredSource{}

	f.onSAP(sapPacketBytes(false, testSDP), nil)
	f.onSAP(sapPacketBytes(false, testSDP), nil)
	f.onMulticast("239.5.5.5:1234", make([]byte, 10))
	ts := make([]byte, 188)
	ts[0] = 0x47
	f.onMulticast("239.5.5.5:1234", ts)
	f.onTAK([]byte(`<event uid="UAS-1"><detail><contact callsign="RAVEN"/>`+
		`<__video url="rtsp://10.0.0.9:8554/live"/></detail></event>`), nil)
	// own announcements are ignored
	f.onTAK([]byte(`<event uid="MediaMTX-me-flight"><detail>`+
		`<__video url="rtsp://10.0.0.1:8554/flight"/></detail></event>`), nil)

	list := f.List()
	require.Len(t, list, 3)

	bySource := map[string]string{}
	for _, s := range list {
		bySource[s.Source] = s.Method + "|" + s.Name + "|" + s.Conf["rtpSDP"]
	}
	require.Equal(t, map[string]string{
		"udp+rtp://239.1.1.1:5000":    "sap|Sensor 1|" + testSDP,
		"udp+mpegts://239.5.5.5:1234": "multicast||",
		"rtsp://10.0.0.9:8554/live":   "tak|RAVEN|",
	}, bySource)

	// a SAP deletion removes the source
	f.onSAP(sapPacketBytes(true, testSDP), nil)
	require.Len(t, f.List(), 2)
}
