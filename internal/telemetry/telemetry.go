// Package telemetry sends anonymous, opt-out daemon and web UI usage events.
package telemetry

import (
	"fmt"
	"log/slog"
	"net/http"
	"testing"

	kittelemetry "go.kenn.io/kit/telemetry"
)

const (
	// EnabledEnv turns telemetry off when set to 0.
	EnabledEnv = "MSGVAULT_TELEMETRY_ENABLED"
	// GenericEnabledEnv is the unprefixed opt-out variable kit also honors.
	GenericEnabledEnv = kittelemetry.GenericTelemetryEnabledEnv
	// EventDaemonActive is the daemon heartbeat sent at start and every 24 hours.
	EventDaemonActive = "daemon_active"
	// EventAppOpened is reported by the web UI through the daemon.
	EventAppOpened = "app_opened"
	application    = "msgvault"
	envPrefix      = "MSGVAULT"
	// PostHog project API keys are public ingest identifiers, not credentials.
	postHogAPIKey = "phc_AzHd9YvuHR7M5poKzC6eW654d3SgKyBdoQPuwkWhimUf" // #nosec G101
)

// Client is the reporter contract the daemon heartbeat uses.
type Client = kittelemetry.PostHogClient

// Reporter sanitizes and sends allowlisted events.
type Reporter = kittelemetry.PostHogReporter

// Options configures the daemon reporter.
type Options struct {
	DataDir string
	Version string
	Commit  string
}

// EnabledFromEnv reports whether the environment leaves telemetry on.
func EnabledFromEnv() bool {
	return kittelemetry.PostHogTelemetryEnabledFromEnv(envPrefix)
}

// NewReporter builds an enabled reporter, or an opted-out one that keeps the allowlist.
func NewReporter(opts Options) (*Reporter, error) {
	if testing.Testing() {
		// go test never sends; kit's opted-out reporter still admits allowlisted events.
		kittelemetry.DisablePostHogTelemetry()
	}
	return buildReporter(opts, "")
}

// NewReporterOrDisabled builds the reporter, or logs why it could not and
// returns kit's disabled reporter so startup continues.
func NewReporterOrDisabled(opts Options, logger *slog.Logger) *Reporter {
	reporter, err := NewReporter(opts)
	if err != nil {
		if logger != nil {
			logger.Warn("telemetry disabled", "error", err)
		}
		return DisabledReporter()
	}
	return reporter
}

// DisabledReporter returns a reporter that admits and sends nothing.
func DisabledReporter() *Reporter {
	return kittelemetry.DisabledPostHogReporter()
}

// CaptureHandler serves the web UI's event posts through reporter. A nil reporter admits no event.
func CaptureHandler(reporter *Reporter) http.Handler {
	return kittelemetry.NewPostHogCaptureHandler(reporter)
}

// buildReporter takes the endpoint so the helper-process test can point the enabled path at a stub; production passes "".
func buildReporter(opts Options, endpoint string) (*Reporter, error) {
	if !EnabledFromEnv() {
		reporter, err := kittelemetry.NewPostHogReporter(kittelemetry.PostHogOptions{EnvPrefix: envPrefix}, allowedEventOptions()...)
		if err != nil {
			return nil, fmt.Errorf("build opted-out telemetry reporter: %w", err)
		}
		return reporter, nil
	}
	id, installedAt, err := loadOrCreateInstall(opts.DataDir)
	if err != nil {
		return nil, err
	}
	reporter, err := kittelemetry.NewPostHogReporter(kittelemetry.PostHogOptions{
		APIKey: postHogAPIKey, Endpoint: endpoint, Application: application, EnvPrefix: envPrefix,
		DistinctID: id, InstalledAt: installedAt, Version: opts.Version, Commit: opts.Commit, Source: "daemon",
	}, allowedEventOptions()...)
	if err != nil {
		return nil, fmt.Errorf("build telemetry reporter: %w", err)
	}
	return reporter, nil
}

func allowedEventOptions() []kittelemetry.PostHogOption {
	return []kittelemetry.PostHogOption{
		kittelemetry.WithAllowedEvent(EventDaemonActive),
		kittelemetry.WithAllowedEvent(EventAppOpened),
	}
}
