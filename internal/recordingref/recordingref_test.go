package recordingref

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScan(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		origins    []string
		kind       Kind
		count      int
	}{
		{"loom", "See https://WWW.LOOM.COM:443/share/abc?token=secret#fragment.", nil, Loom, 1},
		{"cap", "https://cap.so/dev/abc", nil, CapCloud, 1},
		{"cap HTTP", "http://cap.so/s/abc http://www.cap.so/embed/abc", nil, "", 0},
		{"registered", "https://cap.example.test/s/abc", []string{"https://CAP.EXAMPLE.TEST:443"}, CapSelfHosted, 1},
		{"unlisted", "https://cap.example.test/s/abc", nil, "", 0},
		{"lookalike", "https://loom.com.example.test/share/abc", nil, "", 0},
		{"user info", "https://alice@loom.com/share/abc", nil, "", 0},
		{"port", "https://loom.com:444/share/abc", nil, "", 0},
		{"long", "https://loom.com/share/" + strings.Repeat("a", 8192), nil, "", 0},
		{"encoded path", "https://loom.com/share/%61bc", nil, "", 0},
		{"deduplicate", "https://loom.com/share/abc https://www.loom.com/share/abc", nil, Loom, 1},
		{"route identity", "https://loom.com/share/abc https://www.loom.com/embed/abc", nil, Loom, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs := Scan(tc.text, tc.origins)
			require.Len(t, refs, tc.count)
			if tc.count == 1 {
				assert.Equal(t, tc.kind, refs[0].Kind)
				assert.Len(t, refs[0].RouteKey, 64)
			}
		})
	}
	r := Scan("https://cap.example.test/s/abc?token=secret", []string{"https://cap.example.test"})
	require.Len(t, r, 1)
	assert.Empty(t, r[0].Canonical)
	assert.NotContains(t, r[0].Origin, "secret")
}

func TestTeamsPointer(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	r, ok := TeamsPointer("teams:recording:abc", "https://teams.example.test/play?id=secret#fragment")
	assert.True(ok)
	assert.Equal("https://teams.example.test/play?id=secret", r.Canonical)
	_, ok = TeamsPointer("teams:inline:abc", "https://teams.example.test/play")
	assert.False(ok)
	_, ok = TeamsPointer("teams:recording:abc", "https://alice@teams.example.test/play")
	assert.False(ok)
	_, err := CanonicalOrigin("https://example.test/s/abc")
	require.Error(err)
	_, err = CanonicalOrigin("https://example.test#token")
	require.Error(err)
	origin, err := CanonicalOrigin("HTTPS://EXAMPLE.TEST:443")
	require.NoError(err)
	assert.Equal("https://example.test", origin)
}
