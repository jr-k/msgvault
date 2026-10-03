package telemetry

import (
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	kittelemetry "go.kenn.io/kit/telemetry"
)

const (
	wireStubEnv = "MSGVAULT_TELEMETRY_WIRE_STUB"
	wireDirEnv  = "MSGVAULT_TELEMETRY_WIRE_DIR"
)

func TestEnabledEnvMatchesKitDerivation(t *testing.T) {
	assert.Equal(t, EnabledEnv, kittelemetry.PrefixedTelemetryEnabledEnv(envPrefix))
}

func TestNewReporterUnderGoTestKeepsAllowlist(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	t.Setenv(EnabledEnv, "1")
	t.Setenv(GenericEnabledEnv, "1")
	dir := t.TempDir()

	reporter, err := NewReporter(Options{DataDir: dir})
	require.NoError(err)
	assert.False(reporter.Enabled(), "go test must never send")
	assert.True(reporter.EventAllowed(EventAppOpened))
	assert.True(reporter.EventAllowed(EventDaemonActive))
	for _, event := range []string{"daemon_started", "App_Opened", "search_run"} {
		assert.False(reporter.EventAllowed(event), event)
	}
	assert.NoFileExists(filepath.Join(dir, installIDFilename))
}

func TestCaptureHandlerAnswersDisabledUnderGoTest(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	reporter, err := NewReporter(Options{DataDir: t.TempDir()})
	require.NoError(err)

	post := func(handler http.Handler, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		return resp
	}

	handler := CaptureHandler(reporter)
	accepted := post(handler, `{"event":"app_opened"}`)
	require.Equal(http.StatusAccepted, accepted.Code)
	var status struct {
		Status string `json:"status"`
	}
	require.NoError(json.Unmarshal(accepted.Body.Bytes(), &status))
	assert.Equal("disabled", status.Status)
	assert.Equal(http.StatusBadRequest, post(handler, `{"event":"daemon_started"}`).Code)
	assert.Equal(http.StatusBadRequest, post(handler, `{"event":""}`).Code)
	assert.Equal(http.StatusBadRequest, post(CaptureHandler(nil), `{"event":"app_opened"}`).Code)
}

// TestEnabledReporterWireHelper runs only inside the helper process the wire tests start.
func TestEnabledReporterWireHelper(t *testing.T) {
	stub := os.Getenv(wireStubEnv)
	if stub == "" {
		return
	}
	require := require.New(t)
	reporter, err := buildReporter(Options{DataDir: os.Getenv(wireDirEnv), Version: "test-version", Commit: "test-commit"}, stub)
	require.NoError(err)
	if reporter.Enabled() {
		require.NoError(reporter.Capture(EventAppOpened, map[string]any{"query": "q", "account": "a"}))
		require.NoError(reporter.Capture(EventDaemonActive, nil))
	}
	require.NoError(reporter.Close())
}

type recordedRequest struct {
	path string
	body []byte
}

type wireStub struct {
	mu       sync.Mutex
	requests []recordedRequest
	server   *httptest.Server
}

func newWireStub(t *testing.T) *wireStub {
	t.Helper()
	stub := &wireStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		stub.mu.Lock()
		stub.requests = append(stub.requests, recordedRequest{path: r.URL.Path, body: body})
		stub.mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":1}`))
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func (s *wireStub) recorded() []recordedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.requests)
}

func runWireHelper(t *testing.T, stubURL, dir string, env ...string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestEnabledReporterWireHelper$", "-test.count=1") //nolint:gosec // os.Args[0] is the test binary; args are fixed test flags.
	cmd.Env = append(os.Environ(), wireStubEnv+"="+stubURL, wireDirEnv+"="+dir)
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
}

func TestEnabledReporterSendsOnlyAllowlistedFields(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	stub := newWireStub(t)
	dir := t.TempDir()
	id := strings.Repeat("ab", 16)
	created := time.Now().UTC().Add(-50*time.Hour - 30*time.Minute)
	seed, err := json.Marshal(installRecord{ID: id, CreatedAt: created})
	require.NoError(err)
	require.NoError(os.WriteFile(filepath.Join(dir, installIDFilename), seed, 0o600))

	runWireHelper(t, stub.server.URL, dir, EnabledEnv+"=1", GenericEnabledEnv+"=1")

	var messages []map[string]any
	for _, req := range stub.recorded() {
		if req.path != "/batch/" {
			continue
		}
		var batch struct {
			Messages []map[string]any `json:"batch"`
		}
		require.NoError(json.Unmarshal(req.body, &batch), string(req.body))
		messages = append(messages, batch.Messages...)
	}
	require.Len(messages, 2)

	kitKeys := []string{"$process_person_profile", "$geoip_disable", "application", "source", "version", "commit", "goos", "goarch", "install_age_hours"}
	sdkKeys := []string{"$lib", "$lib_version", "$os", "$go_version"}
	optionalSDKKeys := []string{"$os_version", "$os_distro"}
	events := make([]string, 0, len(messages))
	for _, message := range messages {
		events = append(events, fmt.Sprint(message["event"]))
		assert.Equal(id, message["distinct_id"])
		props, ok := message["properties"].(map[string]any)
		require.True(ok, "properties object")
		for _, key := range append(slices.Clone(kitKeys), sdkKeys...) {
			assert.Contains(props, key)
		}
		for key := range props {
			allowed := slices.Contains(kitKeys, key) || slices.Contains(sdkKeys, key) || slices.Contains(optionalSDKKeys, key)
			assert.True(allowed, "unexpected property %q", key)
		}
		assert.Equal("msgvault", props["application"])
		assert.Equal("daemon", props["source"])
		assert.Equal("test-version", props["version"])
		assert.Equal("test-commit", props["commit"])
		assert.InDelta(50, props["install_age_hours"], 0)
		assert.NotContains(props, "query")
		assert.NotContains(props, "account")
	}
	assert.ElementsMatch([]string{EventAppOpened, EventDaemonActive}, events)
}

func TestOptedOutReporterSendsNothing(t *testing.T) {
	cases := []struct {
		name string
		env  []string
	}{
		{"prefixed variable", []string{EnabledEnv + "=0", GenericEnabledEnv + "=1"}},
		{"generic variable", []string{EnabledEnv + "=1", GenericEnabledEnv + "=0"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := newWireStub(t)
			dir := t.TempDir()
			runWireHelper(t, stub.server.URL, dir, tc.env...)
			assert.Empty(t, stub.recorded(), "an opted-out daemon must send nothing")
			assert.NoFileExists(t, filepath.Join(dir, installIDFilename))
		})
	}
}
