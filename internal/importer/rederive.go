package importer

import (
	"context"
	"fmt"
	"log/slog"

	"go.kenn.io/msgvault/internal/rederive"
	"go.kenn.io/msgvault/internal/store"
)

// healDerived re-derives archived rows that an older msgvault derived
// differently, once per source and pass version, before an import adds more.
func healDerived(ctx context.Context, st *store.Store, src *store.Source) error {
	sum, ran, err := rederive.RunIfStale(ctx, st, src.SourceType, src.Identifier, src.ID, nil)
	if err != nil {
		return fmt.Errorf("re-derive archived messages: %w", err)
	}
	if ran && sum != nil && sum.MessagesScanned > 0 {
		slog.Info("re-derived archived messages",
			"source_id", src.ID, "messages", sum.MessagesScanned, "undecodable", sum.Undecodable)
	}
	return nil
}
