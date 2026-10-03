package importer

import (
	"context"
	"log/slog"

	"go.kenn.io/msgvault/internal/rederive"
	"go.kenn.io/msgvault/internal/store"
)

// healDerived re-derives archived rows that an older msgvault derived
// differently, once per source and pass version, before an import adds more.
// A failed pass records no ledger entry and runs again on the next import, so
// it never stops this import; cancellation reaches the import loop below.
func healDerived(ctx context.Context, st *store.Store, src *store.Source) {
	sum, ran, err := rederive.RunIfStale(ctx, st, src.SourceType, src.Identifier, src.ID, nil)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("re-derive archived messages failed; retrying on the next import",
				"source_id", src.ID, "error", err)
		}
		return
	}
	if ran && sum != nil && sum.MessagesScanned > 0 {
		slog.Info("re-derived archived messages",
			"source_id", src.ID, "messages", sum.MessagesScanned, "undecodable", sum.Undecodable)
	}
}
