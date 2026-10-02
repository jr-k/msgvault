package cmd

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/telemetry"
)

type fakeTelemetryClient struct {
	enabled bool
	events  chan string
}

func (c *fakeTelemetryClient) Capture(event string, _ map[string]any) error {
	c.events <- event
	return nil
}

func (c *fakeTelemetryClient) Close() error { return nil }

func (c *fakeTelemetryClient) Enabled() bool { return c.enabled }

func receiveTelemetryEvent(t *testing.T, events <-chan string) string {
	t.Helper()
	select {
	case event := <-events:
		return event
	case <-t.Context().Done():
		require.FailNow(t, "telemetry event never arrived")
		return ""
	}
}

func TestTelemetryHeartbeatCapturesAtStartAndOnTick(t *testing.T) {
	assert := assert.New(t)
	client := &fakeTelemetryClient{enabled: true, events: make(chan string, 4)}
	ticks := make(chan time.Time)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := startTelemetryHeartbeat(ctx, client, ticks, slog.New(slog.DiscardHandler))
	assert.Equal(telemetry.EventDaemonActive, receiveTelemetryEvent(t, client.events))
	ticks <- time.Now()
	assert.Equal(telemetry.EventDaemonActive, receiveTelemetryEvent(t, client.events))

	cancel()
	<-done
	assert.Empty(client.events, "no capture after the heartbeat stops")
}

func TestTelemetryHeartbeatDisabledReporterStartsNothing(t *testing.T) {
	client := &fakeTelemetryClient{events: make(chan string, 1)}
	done := startTelemetryHeartbeat(t.Context(), client, make(chan time.Time), slog.New(slog.DiscardHandler))
	select {
	case <-done:
	default:
		require.FailNow(t, "a disabled reporter must return an already closed channel")
	}
	assert.Empty(t, client.events)
}

// TestServeKeepsServingWithCorruptTelemetryInstallFile runs a real built daemon, where the go-test disable never applies.
func TestServeKeepsServingWithCorruptTelemetryInstallFile(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(err)
	binaryName := "msgvault"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(t.TempDir(), binaryName)
	build := exec.Command("go", "build", "-tags", "fts5 sqlite_vec", "-o", binaryPath, "./cmd/msgvault")
	build.Dir = repoRoot
	buildOutput, err := build.CombinedOutput()
	require.NoError(err, "build real msgvault binary: %s", buildOutput)

	home := t.TempDir()
	const apiKey = "telemetry-fallback-key"
	require.NoError(os.WriteFile(filepath.Join(home, "config.toml"), []byte(
		"[server]\napi_port = 0\nbind_addr = \"127.0.0.1\"\napi_key = \""+apiKey+"\"\n"+
			"[analytics]\nengine = \"auto\"\nauto_build_cache = false\n[vector]\nenabled = false\n"), 0o600))
	corrupt := []byte("not json")
	installPath := filepath.Join(home, "telemetry-install-id")
	require.NoError(os.WriteFile(installPath, corrupt, 0o600))

	serve := exec.Command(binaryPath, "--home", home, "serve")
	serve.Env = append(os.Environ(), telemetry.EnabledEnv+"=1", telemetry.GenericEnabledEnv+"=1")
	stdout, err := serve.StdoutPipe()
	require.NoError(err)
	require.NoError(serve.Start())
	t.Cleanup(func() {
		_ = serve.Process.Kill()
		_ = serve.Wait()
	})

	origins := make(chan string, 1)
	go func() {
		addr := regexp.MustCompile(`API server: (http://127\.0\.0\.1:\d+)`)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if match := addr.FindStringSubmatch(scanner.Text()); match != nil {
				origins <- match[1]
				break
			}
		}
		_, _ = io.Copy(io.Discard, stdout)
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	var origin string
	select {
	case origin = <-origins:
	case <-ctx.Done():
		require.FailNow("daemon never reported its API server")
	}

	require.Eventually(func() bool {
		resp, err := http.Get(origin + "/health")
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 2*time.Minute, 50*time.Millisecond, "daemon health")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/api/v1/telemetry/events", strings.NewReader(`{"event":"app_opened"}`))
	require.NoError(err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", apiKey)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(err)
	_ = resp.Body.Close()
	assert.Equal(http.StatusBadRequest, resp.StatusCode, "the disabled fallback admits no event")

	after, err := os.ReadFile(installPath)
	require.NoError(err)
	assert.Equal(corrupt, after)
}
