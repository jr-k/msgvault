package store_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

func TestRecordingReferenceTeamsPointer(t *testing.T) {
	assert, require := assert.New(t), require.New(t)
	f := storetest.New(t)
	id := f.CreateMessage("recording")
	refs := []store.AttachmentRef{{SourceAttachmentID: "teams:recording:abc", StoragePath: "https://teams.example.test/play?token=one"}}
	_, err := f.Store.DB().Exec(f.Store.Rebind(`UPDATE messages SET content_changed_at=? WHERE id=?`), "2000-01-01 00:00:00.000", id)
	require.NoError(err)
	require.NoError(f.Store.ReplaceMessageLinkAttachments(id, refs))
	var before, after string
	require.NoError(f.Store.DB().QueryRow(f.Store.Rebind(`SELECT content_changed_at FROM messages WHERE id=?`), id).Scan(&before))
	assert.NotContains(before, "2000-01-01")
	require.NoError(f.Store.ReplaceMessageLinkAttachments(id, refs))
	require.NoError(f.Store.DB().QueryRow(f.Store.Rebind(`SELECT content_changed_at FROM messages WHERE id=?`), id).Scan(&after))
	assert.Equal(before, after)
	_, err = f.Store.DB().Exec(f.Store.Rebind(`UPDATE messages SET content_changed_at=? WHERE id=?`), "2000-01-01 00:00:00.000", id)
	require.NoError(err)
	refs[0].StoragePath = "https://teams.example.test/play?token=two"
	require.NoError(f.Store.ReplaceMessageLinkAttachments(id, refs))
	require.NoError(f.Store.DB().QueryRow(f.Store.Rebind(`SELECT content_changed_at FROM messages WHERE id=?`), id).Scan(&after))
	assert.NotContains(after, "2000-01-01")
}

func TestRecordingReferenceState(t *testing.T) {
	assert, require := assert.New(t), require.New(t)
	f := storetest.New(t)
	id := f.CreateMessage("recording")
	input := store.RecordingReferenceInput{RouteKey: "route", Kind: "loom", Origin: "https://loom.com", RefSHA256: "hash", OccurrenceJSON: `{"ref":"ref","revision":"revision"}`}
	require.NoError(f.Store.ReconcileRecordingReferences(t.Context(), "destination", id, true, []store.RecordingReferenceInput{input}))
	claims, err := f.Store.ClaimRecordingReferences(t.Context(), "destination", time.Now().Add(time.Hour), 20)
	require.NoError(err)
	require.Len(claims, 1)
	first := claims[0]
	now := time.Now().UTC()
	ok, err := f.Store.FinishRecordingReference(t.Context(), first, store.RecordingReferenceResult{State: "uncertain", LastSendAt: &now, NextActionAt: now})
	require.NoError(err)
	assert.True(ok)
	require.NoError(f.Store.ReconcileRecordingReferences(t.Context(), "destination", id, false, nil))
	claims, err = f.Store.ClaimRecordingReferences(t.Context(), "destination", now.Add(time.Hour), 20)
	require.NoError(err)
	require.Len(claims, 1)
	assert.Equal("uncertain", claims[0].State)
	ok, err = f.Store.FinishRecordingReference(t.Context(), first, store.RecordingReferenceResult{State: "withdrawn", NextActionAt: now})
	require.NoError(err)
	assert.True(ok)
	input.RefSHA256 = "new-hash"
	require.NoError(f.Store.ReconcileRecordingReferences(t.Context(), "destination", id, true, []store.RecordingReferenceInput{input}))
	claims, err = f.Store.ClaimRecordingReferences(t.Context(), "destination", now.Add(time.Hour), 20)
	require.NoError(err)
	require.Len(claims, 1)
	assert.NotEqual(first.OperationID, claims[0].OperationID)
	ok, err = f.Store.FinishRecordingReference(t.Context(), first, store.RecordingReferenceResult{State: "retained", NextActionAt: now})
	require.NoError(err)
	assert.False(ok)
	ok, err = f.Store.MarkRecordingReferenceSending(t.Context(), first, now)
	require.NoError(err)
	assert.False(ok)
	before, err := f.Store.LoadRecordingReferenceCursor(t.Context(), "destination")
	require.NoError(err)
	after := store.RecordingReferenceCursor{At: now, AfterID: id, AfterRow: true, Policy: "policy"}
	ok, err = f.Store.AdvanceRecordingReferenceCursor(t.Context(), "destination", before, after)
	require.NoError(err)
	assert.True(ok)
	ok, err = f.Store.AdvanceRecordingReferenceCursor(t.Context(), "destination", before, after)
	require.NoError(err)
	assert.False(ok)
}
