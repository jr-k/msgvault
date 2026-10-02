package cmd

import (
	"context"
	"log/slog"
	"time"

	"go.kenn.io/msgvault/internal/telemetry"
)

const telemetryHeartbeatInterval = 24 * time.Hour

func closedTelemetryDone() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

// startTelemetryHeartbeat sends daemon_active now and on every tick until ctx ends; the returned channel closes once it stops.
func startTelemetryHeartbeat(ctx context.Context, reporter telemetry.Client, ticks <-chan time.Time, logger *slog.Logger) <-chan struct{} {
	if reporter == nil || !reporter.Enabled() {
		return closedTelemetryDone()
	}
	done := make(chan struct{})
	capture := func() {
		if err := reporter.Capture(telemetry.EventDaemonActive, nil); err != nil && ctx.Err() == nil {
			logger.Warn("capture telemetry event", "event", telemetry.EventDaemonActive, "error", err)
		}
	}
	go func() {
		defer close(done)
		capture()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticks:
				capture()
			}
		}
	}()
	return done
}
