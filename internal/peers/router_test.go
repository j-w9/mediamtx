package peers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLink(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	tick := func(d time.Duration) time.Time {
		now = now.Add(d)
		return now
	}

	l := &Link{Variant: "original"}
	errs := uint64(0)

	// a clean link stays on the original
	for range 10 {
		require.Equal(t, "original", l.Update(Health{Available: true, FramesInError: errs}, tick(5*time.Second)))
	}

	// an isolated burst of errors is tolerated
	errs += 10
	require.Equal(t, "original", l.Update(Health{Available: true, FramesInError: errs}, tick(5*time.Second)))
	require.Equal(t, "original", l.Update(Health{Available: true, FramesInError: errs}, tick(5*time.Second)))

	// errors that persist make the link fall back to the compressed copy
	errs += 10
	require.Equal(t, "original", l.Update(Health{Available: true, FramesInError: errs}, tick(5*time.Second)))
	errs += 10
	require.Equal(t, "compressed", l.Update(Health{Available: true, FramesInError: errs}, tick(5*time.Second)))

	// it stays there for 2 minutes, then the original is tried again
	require.Equal(t, "compressed", l.Update(Health{Available: true}, tick(time.Minute)))
	require.Equal(t, "original", l.Update(Health{Available: true}, tick(61*time.Second)))

	// the attempt fails: back to the compressed copy, this time for 4 minutes
	errs = 0
	require.Equal(t, "original", l.Update(Health{Available: true, FramesInError: errs}, tick(5*time.Second)))
	errs += 5
	require.Equal(t, "original", l.Update(Health{Available: true, FramesInError: errs}, tick(5*time.Second)))
	errs += 5
	require.Equal(t, "compressed", l.Update(Health{Available: true, FramesInError: errs}, tick(5*time.Second)))
	require.Equal(t, "compressed", l.Update(Health{Available: true}, tick(3*time.Minute)))
	require.Equal(t, "original", l.Update(Health{Available: true}, tick(61*time.Second)))

	// this time the original holds, so the wait starts again from 2 minutes
	for range 15 {
		require.Equal(t, "original", l.Update(Health{Available: true}, tick(5*time.Second)))
	}
	require.Equal(t, initialHold, l.hold)

	// a stream that does not arrive at all is a reason to fall back too
	require.Equal(t, "original", l.Update(Health{Available: false}, tick(5*time.Second)))
	require.Equal(t, "original", l.Update(Health{Available: false}, tick(5*time.Second)))
	require.Equal(t, "original", l.Update(Health{Available: false}, tick(5*time.Second)))
	require.Equal(t, "compressed", l.Update(Health{Available: false}, tick(5*time.Second)))
}

func TestLinkPacketLoss(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l := &Link{Variant: "original"}

	recv, lost := uint64(0), uint64(0)
	step := func(r uint64, lo uint64) string {
		recv += r
		lost += lo
		now = now.Add(5 * time.Second)
		return l.Update(Health{Available: true, PacketsReceived: recv, PacketsLost: lost}, now)
	}

	// 2% of packets lost and recovered: fine
	for range 5 {
		require.Equal(t, "original", step(1000, 20))
	}

	// 30% lost: the link is spending a lot of its capacity in sending packets again
	require.Equal(t, "original", step(1000, 430))
	require.Equal(t, "compressed", step(1000, 430))
}

func TestVariant(t *testing.T) {
	s := &Sources{Original: "srt://a?streamid=read:x", Compressed: "srt://a?streamid=read:x_compressed"}
	require.Equal(t, "original", Variant(s, s.Original))
	require.Equal(t, "compressed", Variant(s, s.Compressed))
	require.Equal(t, "", Variant(s, "rtsp://other"))
	require.Equal(t, "", Variant(&Sources{Original: "a"}, ""))
}
