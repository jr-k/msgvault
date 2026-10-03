package recordingref

import (
	"bytes"
	"database/sql"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/docbankmedia"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil/storetest"
)

func recordingBody(t *testing.T, f *storetest.Fixture, id int64, body string) {
	t.Helper()
	require.NoError(t, f.Store.UpsertMessageBody(id, sql.NullString{String: body, Valid: true}, sql.NullString{}))
}

func recordingState(t *testing.T, f *storetest.Fixture, id int64) (state, code, operation string) {
	t.Helper()
	require.NoError(t, f.Store.DB().QueryRow(f.Store.Rebind(`SELECT state,error_code,operation_id FROM recording_references WHERE message_id=? LIMIT 1`), id).Scan(&state, &code, &operation))
	return
}

func due(t *testing.T, f *storetest.Fixture) {
	t.Helper()
	_, err := f.Store.DB().Exec(`UPDATE recording_references SET next_action_at='2000-01-01 00:00:00.000'`)
	require.NoError(t, err)
}

func runDiscovery(t *testing.T, w *Worker, count int) {
	t.Helper()
	assert.Eventually(t, func() bool {
		_, err := w.RunBatch(t.Context())
		require.NoError(t, err)
		var got int
		require.NoError(t, w.st.DB().QueryRow(`SELECT COUNT(*) FROM recording_references`).Scan(&got))
		return got == count
	}, 5*time.Second, 10*time.Millisecond)
}

func recordingClient(t *testing.T, handler http.HandlerFunc) *docbankmedia.Client {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	c, err := docbankmedia.NewClient(s.URL, nil)
	require.NoError(t, err)
	return c
}

func writeReceipt(w http.ResponseWriter, op string) {
	data, _ := json.Marshal(docbankmedia.Receipt{VaultUID: "vault", SourceID: "source", OperationID: op, OccurrenceID: "occurrence", OperationState: "succeeded", CoverageState: "link_only", Outcome: "retained"})
	_, _ = w.Write(data)
}

func TestRecordingReferenceFeed(t *testing.T) {
	assert, require := assert.New(t), require.New(t)
	f := storetest.New(t)
	client := recordingClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	one := f.CreateMessage("one")
	recordingBody(t, f, one, "https://loom.com/share/one")
	w := NewWorker(f.Store, client, "destination", nil, nil)
	runDiscovery(t, w, 1)
	source, err := f.Store.GetOrCreateSource("slack", "alice")
	require.NoError(err)
	conv, err := f.Store.EnsureConversation(source.ID, "thread", "Thread")
	require.NoError(err)
	two, err := f.Store.UpsertMessage(&store.Message{SourceID: source.ID, ConversationID: conv, SourceMessageID: "two", MessageType: "slack"})
	require.NoError(err)
	recordingBody(t, f, two, "https://cap.example.test/s/two")
	_, err = w.RunBatch(t.Context())
	require.NoError(err)
	w = NewWorker(f.Store, client, "destination", []string{"https://cap.example.test"}, nil)
	runDiscovery(t, w, 2)
	recordingBody(t, f, one, "recording removed")
	assert.Eventually(func() bool {
		_, err := w.RunBatch(t.Context())
		require.NoError(err)
		state, _, _ := recordingState(t, f, one)
		return state == "withdrawn"
	}, 5*time.Second, 10*time.Millisecond)
}

func TestRecordingReferenceBackfill(t *testing.T) {
	assert, require := assert.New(t), require.New(t)
	f := storetest.New(t)
	for i := range 405 {
		id := f.CreateMessage(strconv.Itoa(i))
		recordingBody(t, f, id, "https://loom.com/share/"+strconv.Itoa(i))
	}
	_, err := f.Store.DB().Exec(`UPDATE messages SET content_changed_at='2000-01-01 00:00:00.000'`)
	require.NoError(err)
	client := recordingClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	result, err := NewWorker(f.Store, client, "destination", nil, nil).RunBatch(t.Context())
	require.NoError(err)
	assert.Equal(405, result.Examined)
	assert.Equal(3, result.Pages)
	var count int
	require.NoError(f.Store.DB().QueryRow(`SELECT COUNT(*) FROM recording_references`).Scan(&count))
	assert.Equal(405, count)
}

func TestRecordingReferenceTeamsPointer(t *testing.T) {
	f := storetest.New(t)
	id := f.CreateMessage("teams")
	require.NoError(t, f.Store.ReplaceMessageLinkAttachments(id, []store.AttachmentRef{{SourceAttachmentID: "teams:recording:abc", StoragePath: "https://teams.example.test/play?token=secret"}}))
	client := recordingClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	runDiscovery(t, NewWorker(f.Store, client, "destination", nil, nil), 1)
	state, _, _ := recordingState(t, f, id)
	assert.Equal(t, "pending", state)
}

func TestRecordingReferenceDelivery(t *testing.T) {
	t.Run("hidden until restored", func(t *testing.T) {
		assert, require := assert.New(t), require.New(t)
		f := storetest.New(t)
		id := f.CreateMessage("recording")
		recordingBody(t, f, id, "https://loom.com/share/abc")
		requests := 0
		client := recordingClient(t, func(w http.ResponseWriter, r *http.Request) {
			var req docbankmedia.ReferenceRequest
			if !assert.NoError(json.UnmarshalRead(r.Body, &req)) {
				return
			}
			requests++
			writeReceipt(w, req.OperationID)
		})
		m, exists, err := f.Store.ReadRecordingMessage(t.Context(), id)
		require.NoError(err)
		require.True(exists)
		inputs, err := referenceInputs(m, messageRefs(m, nil))
		require.NoError(err)
		require.NoError(f.Store.ReconcileRecordingReferences(t.Context(), "destination", id, true, inputs))
		_, err = f.Store.DB().Exec(f.Store.Rebind(`UPDATE messages SET deleted_at=? WHERE id=?`), "2000-01-01 00:00:00.000", id)
		require.NoError(err)
		w := NewWorker(f.Store, client, "destination", nil, nil)
		_, err = w.RunBatch(t.Context())
		require.NoError(err)
		assert.Zero(requests)
		state, _, _ := recordingState(t, f, id)
		assert.Equal("withdrawn", state)
		_, err = f.Store.DB().Exec(f.Store.Rebind(`UPDATE messages SET deleted_at=NULL WHERE id=?`), id)
		require.NoError(err)
		assert.Eventually(func() bool { _, err := w.RunBatch(t.Context()); require.NoError(err); return requests == 1 }, 5*time.Second, 10*time.Millisecond)
	})
	for _, tc := range []struct {
		name        string
		status      int
		code, state string
	}{
		{"retry", 503, "server_error", "pending"},
		{"blocked", 422, "validation", "blocked"},
		{"capability", 503, "capability_unavailable", "pending"},
		{"credential", 0, "credential_unavailable", "pending"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert, require := assert.New(t), require.New(t)
			f := storetest.New(t)
			id := f.CreateMessage("recording")
			recordingBody(t, f, id, "https://loom.com/share/abc?token=one")
			var requests []docbankmedia.ReferenceRequest
			status := tc.status
			client := recordingClient(t, func(w http.ResponseWriter, r *http.Request) {
				var req docbankmedia.ReferenceRequest
				if !assert.NoError(json.UnmarshalRead(r.Body, &req)) {
					return
				}
				requests = append(requests, req)
				if status != 200 {
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"code":"` + tc.code + `"}`))
					return
				}
				writeReceipt(w, req.OperationID)
			})
			if tc.name == "credential" {
				client, err := docbankmedia.NewClient("http://127.0.0.1:1", func() (string, error) { return "", docbankmedia.ErrCredentialUnavailable })
				require.NoError(err)
				w := NewWorker(f.Store, client, "destination", nil, nil)
				runDiscovery(t, w, 1)
				state, code, _ := recordingState(t, f, id)
				assert.Equal(tc.state, state)
				assert.Equal(tc.code, code)
				assert.Empty(requests)
				return
			}
			w := NewWorker(f.Store, client, "destination", nil, nil)
			runDiscovery(t, w, 1)
			state, code, operation := recordingState(t, f, id)
			assert.Equal(tc.state, state)
			assert.Equal(tc.code, code)
			if tc.name == "capability" {
				due(t, f)
				_, err := w.RunBatch(t.Context())
				require.NoError(err)
				claims, err := f.Store.ClaimRecordingReferences(t.Context(), "destination", time.Now().Add(time.Hour), 20)
				require.NoError(err)
				require.Len(claims, 1)
				assert.Equal(2, claims[0].RetryCount)
				assert.WithinDuration(time.Now().Add(10*time.Minute), claims[0].NextActionAt, 5*time.Second)
			}
			status = 200
			require.NoError(f.Store.ReconsiderBlockedRecordingReferences(t.Context(), "destination"))
			due(t, f)
			_, err := w.RunBatch(t.Context())
			require.NoError(err)
			state, code, after := recordingState(t, f, id)
			assert.Equal("retained", state)
			assert.Empty(code)
			assert.Equal(operation, after)
			assert.Equal(requests[0], requests[len(requests)-1])
			recordingBody(t, f, id, "https://loom.com/share/abc?token=two")
			assert.Eventually(func() bool {
				_, err := w.RunBatch(t.Context())
				require.NoError(err)
				_, _, after := recordingState(t, f, id)
				return after != operation
			}, 5*time.Second, 10*time.Millisecond)
		})
	}
}

func TestRecordingReferenceUncertain(t *testing.T) {
	t.Run("unsuccessful resend preserves receipt recovery", func(t *testing.T) {
		assert, require := assert.New(t), require.New(t)
		f := storetest.New(t)
		id := f.CreateMessage("recording")
		recordingBody(t, f, id, "https://loom.com/share/abc")
		m, exists, err := f.Store.ReadRecordingMessage(t.Context(), id)
		require.NoError(err)
		require.True(exists)
		inputs, err := referenceInputs(m, messageRefs(m, nil))
		require.NoError(err)
		require.NoError(f.Store.ReconcileRecordingReferences(t.Context(), "destination", id, true, inputs))
		claims, err := f.Store.ClaimRecordingReferences(t.Context(), "destination", time.Now().Add(time.Hour), 20)
		require.NoError(err)
		require.Len(claims, 1)
		now := time.Now().UTC()
		applied, err := f.Store.FinishRecordingReference(t.Context(), claims[0], store.RecordingReferenceResult{State: "uncertain", LastSendAt: &now, NextActionAt: now})
		require.NoError(err)
		require.True(applied)
		client := recordingClient(t, func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
		w := NewWorker(f.Store, client, "destination", nil, nil)
		_, err = w.RunBatch(t.Context())
		require.NoError(err)
		state, code, op := recordingState(t, f, id)
		assert.Equal("uncertain", state)
		assert.Equal("server_error", code)
		assert.Equal(claims[0].OperationID, op)
		missingKey, err := docbankmedia.NewClient("http://127.0.0.1:1", func() (string, error) { return "", docbankmedia.ErrCredentialUnavailable })
		require.NoError(err)
		w.client = missingKey
		due(t, f)
		_, err = w.RunBatch(t.Context())
		require.NoError(err)
		state, code, _ = recordingState(t, f, id)
		assert.Equal("uncertain", state)
		assert.Equal("credential_unavailable", code)
		recordingBody(t, f, id, "link removed")
		due(t, f)
		w.client = recordingClient(t, func(w http.ResponseWriter, r *http.Request) {
			assert.Equal(http.MethodGet, r.Method)
			writeReceipt(w, op)
		})
		_, err = w.RunBatch(t.Context())
		require.NoError(err)
		state, code, _ = recordingState(t, f, id)
		assert.Equal("withdrawn", state)
		assert.Empty(code)
	})
	for _, tc := range []struct {
		name       string
		live       bool
		status     int
		old        bool
		want, code string
	}{
		{"resend", true, 200, false, "retained", ""},
		{"receipt", false, 200, false, "withdrawn", ""},
		{"recent miss", false, 404, false, "uncertain", "not_found"},
		{"settled miss", false, 404, true, "withdrawn", "receipt_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert, require := assert.New(t), require.New(t)
			f := storetest.New(t)
			id := f.CreateMessage("recording")
			recordingBody(t, f, id, "https://loom.com/share/abc?token=one")
			lose := true
			var requests []docbankmedia.ReferenceRequest
			var get string
			client := recordingClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					get = r.URL.Path
					w.WriteHeader(tc.status)
					if tc.status == 200 {
						writeReceipt(w, strings.TrimPrefix(get, "/api/v1/media/operations/"))
					}
					return
				}
				var req docbankmedia.ReferenceRequest
				if !assert.NoError(json.UnmarshalRead(r.Body, &req)) {
					return
				}
				requests = append(requests, req)
				if lose {
					hijacker, ok := w.(http.Hijacker)
					if !assert.True(ok) {
						return
					}
					conn, _, err := hijacker.Hijack()
					if !assert.NoError(err) {
						return
					}
					_ = conn.Close()
					return
				}
				writeReceipt(w, req.OperationID)
			})
			w := NewWorker(f.Store, client, "destination", nil, nil)
			runDiscovery(t, w, 1)
			state, code, op := recordingState(t, f, id)
			assert.Equal("uncertain", state)
			assert.Equal("transport", code)
			if !tc.live {
				_, err := f.Store.DB().Exec(f.Store.Rebind(`UPDATE messages SET deleted_from_source_at=? WHERE id=?`), "2000-01-01 00:00:00.000", id)
				require.NoError(err)
			}
			if tc.old {
				_, err := f.Store.DB().Exec(`UPDATE recording_references SET last_send_at='2000-01-01 00:00:00.000'`)
				require.NoError(err)
			}
			lose = false
			due(t, f)
			_, err := w.RunBatch(t.Context())
			require.NoError(err)
			state, code, after := recordingState(t, f, id)
			assert.Equal(tc.want, state)
			assert.Equal(tc.code, code)
			assert.Equal(op, after)
			if tc.live {
				require.Len(requests, 2)
				assert.Equal(requests[0], requests[1])
			} else {
				assert.Equal("/api/v1/media/operations/"+op, get)
			}
		})
	}
}

func TestRecordingReferenceSecrets(t *testing.T) {
	assert, require := assert.New(t), require.New(t)
	f := storetest.New(t)
	id := f.CreateMessage("recording")
	recordingBody(t, f, id, "https://cap.example.test/s/path_secret?token=query_secret#fragment_secret")
	var logs bytes.Buffer
	client := recordingClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":"query_secret","detail":"path_secret"}`))
	})
	w := NewWorker(f.Store, client, "destination", []string{"https://cap.example.test"}, slog.New(slog.NewTextHandler(&logs, nil)))
	runDiscovery(t, w, 1)
	var row string
	require.NoError(f.Store.DB().QueryRow(`SELECT route_key || kind || origin || ref_sha256 || operation_id || occurrence_json || state || error_code || source_id || occurrence_id || outcome || coverage_state FROM recording_references`).Scan(&row))
	for _, secret := range []string{"path_secret", "query_secret", "fragment_secret"} {
		assert.NotContains(row, secret)
		assert.NotContains(logs.String(), secret)
	}
	_, err := client.SubmitReference(t.Context(), docbankmedia.ReferenceRequest{OperationID: "op", ReferenceURL: "https://cap.example.test/s/path_secret"})
	require.Error(err)
	assert.NotContains(err.Error(), "secret")
}
