package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	emersionimap "github.com/emersion/go-imap/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/msgvault/internal/agentgrant"
	"go.kenn.io/msgvault/internal/api"
	"go.kenn.io/msgvault/internal/config"
	imaplib "go.kenn.io/msgvault/internal/imap"
	"go.kenn.io/msgvault/internal/store"
	"go.kenn.io/msgvault/internal/testutil"
)

func TestDraftComposeArgs(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	intent, err := parseDraftComposeArgs([]string{
		"draft-compose", "--source-id", "42", "--from", "owner@example.test",
		"--to", "to@example.test", "--cc=copy@example.test", "--bcc", "hidden@example.test",
		"--subject", "Subject", "--body=body", "--json",
	})
	requirements.NoError(err)
	assertions.Equal(int64(42), intent.SourceID)
	assertions.Equal([]string{"to@example.test"}, intent.To)
	assertions.Equal([]string{"copy@example.test"}, intent.Cc)
	assertions.Equal([]string{"hidden@example.test"}, intent.Bcc)
	assertions.True(intent.JSON)

	for _, args := range [][]string{
		{"draft-compose", "--source-id", "0", "--to", "to@example.test"},
		{"draft-compose", "--to", "to@example.test"},
		{"draft-compose", "--source-id", "42"},
		{"draft-compose", "--source-id", "42", "--account", "owner@example.test", "--to", "to@example.test"},
	} {
		_, err := parseDraftComposeArgs(args)
		assertions.Error(err)
	}
}

func TestDraftComposeEndToEnd(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	fixture := newDraftReplyFixture(t)
	adapter := fixture.grantedAdapter()
	events := make([]api.CLIRunEvent, 0, 1)
	err := adapter.runCLIComposeDraft(t.Context(), api.CLIRunRequest{Args: []string{
		"draft-compose", "--source-id", strconv.FormatInt(fixture.source.ID, 10),
		"--from", testutil.IMAPTestUsername,
		"--to", "to@example.test", "--cc", "copy@example.test", "--bcc", "hidden@example.test",
		"--subject", "Compose subject", "--body", "compose body", "--json",
	}}, func(event api.CLIRunEvent) error {
		events = append(events, event)
		return nil
	})
	requirements.NoError(err)
	requirements.Len(events, 1)
	var result draftReplyOutput
	requirements.NoError(json.Unmarshal([]byte(events[0].Data), &result))
	assertions.Equal(draftReplyStatusCreated, result.Status)
	assertions.Equal(int64(1), result.Revision)

	message, err := fixture.store.GetMessage(result.MessageID)
	requirements.NoError(err)
	assertions.Equal([]string{"to@example.test"}, message.To)
	assertions.Equal([]string{"copy@example.test"}, message.Cc)
	assertions.Equal([]string{"hidden@example.test"}, message.Bcc)
	raw, err := fixture.store.GetMessageRaw(result.MessageID)
	requirements.NoError(err)
	assertions.Contains(string(raw), "Bcc:")
	assertions.Contains(string(raw), "hidden@example.test")

	// The backends tokenize punctuation in full email queries differently.
	matches, total, err := fixture.store.SearchMessages("copy", 0, 10)
	requirements.NoError(err)
	requirements.Equal(int64(1), total)
	requirements.Len(matches, 1)
	assertions.Equal(result.MessageID, matches[0].ID)
}

func TestDraftComposeHTTPPublishesManagedDraft(t *testing.T) {
	assertions := assert.New(t)
	requirements := require.New(t)
	fixture := newDraftReplyFixture(t)
	adapter := fixture.grantedAdapter()
	server := httptest.NewServer(api.NewServerWithOptions(api.ServerOptions{
		Config: &config.Config{
			HomeDir: t.TempDir(),
			Server:  config.ServerConfig{APIKey: "owner-test-key"},
		},
		Store:  adapter,
		Logger: slog.New(slog.DiscardHandler),
	}).Router())
	t.Cleanup(server.Close)

	args := []string{
		"draft-compose", "--source-id", strconv.FormatInt(fixture.source.ID, 10),
		"--from", testutil.IMAPTestUsername, "--to", "to@example.test",
		"--cc", "copy@example.test", "--bcc", "hidden@example.test",
		"--subject", "Compose subject", "--body", "compose body", "--json",
	}
	body, err := json.Marshal(map[string]any{"args": args})
	requirements.NoError(err)
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/cli/run", bytes.NewReader(body))
	requirements.NoError(err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Api-Key", "owner-test-key")
	response, err := http.DefaultClient.Do(request)
	requirements.NoError(err)
	defer func() { _ = response.Body.Close() }()
	requirements.Equal(http.StatusOK, response.StatusCode)

	var events []api.CLIRunEvent
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		var event api.CLIRunEvent
		requirements.NoError(json.Unmarshal(scanner.Bytes(), &event))
		events = append(events, event)
	}
	requirements.NoError(scanner.Err())
	requirements.Len(events, 2)
	requirements.Equal(cliStreamStdout, events[0].Type)
	requirements.Equal("complete", events[1].Type)

	var result draftReplyOutput
	requirements.NoError(json.Unmarshal([]byte(events[0].Data), &result))
	assertions.Equal(draftReplyStatusCreated, result.Status)
	assertions.Equal(fixture.source.ID, result.SourceID)
	assertions.Equal("Drafts", result.Mailbox)
	assertions.NotZero(result.UID)
	assertions.NotZero(result.UIDValidity)
	assertions.Equal(int64(1), result.Revision)

	draft, err := fixture.store.GetIMAPDraft(result.DraftID)
	requirements.NoError(err)
	assertions.Equal(result.MessageID, draft.CurrentMessageID)
	assertions.Equal(result.UID, draft.CurrentReceipt.UID)
	assertions.Equal(result.UIDValidity, draft.CurrentReceipt.UIDValidity)
	message, err := fixture.store.GetMessage(result.MessageID)
	requirements.NoError(err)
	assertions.Equal([]string{"to@example.test"}, message.To)
	assertions.Equal([]string{"copy@example.test"}, message.Cc)
	assertions.Equal([]string{"hidden@example.test"}, message.Bcc)
	storedRaw, err := fixture.store.GetMessageRaw(result.MessageID)
	requirements.NoError(err)
	assertions.Contains(string(storedRaw), "Bcc:")
	assertions.Contains(string(storedRaw), "hidden@example.test")

	flags, fetchedRaw := fetchDraftMailboxMessage(t, fixture.config, draft.CurrentReceipt)
	assertions.Contains(flags, emersionimap.FlagDraft)
	assertions.Equal(storedRaw, fetchedRaw)
}

const personDraftSanitizedValue = "@carol\x1b[31m:example.org"

// personDraftFixture is one durable person whose cluster holds a column email,
// an identifier-only email, a phone number, and two chat identifiers, beside
// an unrelated participant and a curated-only contact point.
type personDraftFixture struct {
	draftReplyFixture

	adapter       *storeAPIAdapter
	providerCalls *int
	personID      int64
}

// countingDraftAdapter returns the granted adapter with a draft client
// factory that counts every provider connection.
func countingDraftAdapter(fixture draftReplyFixture) (*storeAPIAdapter, *int) {
	adapter := fixture.grantedAdapter()
	calls := new(int)
	factory := adapter.draftClientFactory
	adapter.draftClientFactory = func(ctx context.Context, source *store.Source) (*imaplib.Client, error) {
		*calls++
		return factory(ctx, source)
	}
	return adapter, calls
}

func newPersonDraftFixture(t *testing.T) personDraftFixture {
	t.Helper()
	require := require.New(t)
	fixture := newDraftReplyFixture(t)
	st := fixture.store
	columnID, err := st.EnsureParticipant("carol@example.com", "Carol", "example.com")
	require.NoError(err)
	identifierID, err := st.EnsureParticipantByIdentifier("email", "carol.alt@example.com", "")
	require.NoError(err)
	phoneID, err := st.EnsurePhoneParticipantContext(t.Context(), "+15555550100", "")
	require.NoError(err)
	chatID, err := st.EnsureParticipantByIdentifier("imessage", "carol-chat@example.org", "")
	require.NoError(err)
	matrixID, err := st.EnsureParticipantByIdentifier("matrix", personDraftSanitizedValue, "")
	require.NoError(err)
	for _, member := range []int64{identifierID, phoneID, chatID, matrixID} {
		_, err = st.LinkParticipants(columnID, member)
		require.NoError(err)
	}
	person, _, err := st.CreatePersonFromParticipantContext(t.Context(), columnID)
	require.NoError(err)
	require.ElementsMatch([]int64{columnID, identifierID, phoneID, chatID, matrixID}, person.ParticipantIDs)
	_, err = st.EnsureParticipant("unrelated@example.com", "Unrelated", "example.com")
	require.NoError(err)
	_, err = st.AddPersonContactPointContext(t.Context(), person.ID, store.PersonContactPointInput{
		AddressKind: store.ContactAddressEmail, OriginalValue: "curated-only@example.com",
		Envelope: store.ValueEnvelopeInput{Source: store.ProvenanceUser},
	})
	require.NoError(err)
	adapter, calls := countingDraftAdapter(fixture)
	return personDraftFixture{draftReplyFixture: fixture, adapter: adapter, providerCalls: calls, personID: person.ID}
}

func (f draftReplyFixture) runCompose(
	t *testing.T, adapter *storeAPIAdapter, grant *agentgrant.Grant, args ...string,
) ([]api.CLIRunEvent, error) {
	t.Helper()
	var events []api.CLIRunEvent
	err := adapter.runCLIComposeDraft(t.Context(), api.CLIRunRequest{
		Args: append([]string{api.CLIRunDraftComposeCommand}, args...), Grant: grant,
	}, func(event api.CLIRunEvent) error {
		events = append(events, event)
		return nil
	})
	return events, err
}

func (f draftReplyFixture) composeToPerson(
	t *testing.T, adapter *storeAPIAdapter, personID int64, to string,
) ([]api.CLIRunEvent, error) {
	t.Helper()
	return f.runCompose(t, adapter, nil,
		"--person-id", strconv.FormatInt(personID, 10),
		"--source-id", strconv.FormatInt(f.source.ID, 10), "--from", testutil.IMAPTestUsername,
		"--to", to, "--subject", "Hello", "--body", "person body", "--json")
}

func (f draftReplyFixture) listPerson(t *testing.T, adapter *storeAPIAdapter, personID int64) []personDraftAddress {
	t.Helper()
	events, err := f.runCompose(t, adapter, nil, "--person-id", strconv.FormatInt(personID, 10), "--json")
	require.NoError(t, err)
	require.Len(t, events, 1)
	var output personDraftAddressesOutput
	require.NoError(t, json.Unmarshal([]byte(events[0].Data), &output))
	require.Equal(t, personID, output.PersonID)
	return output.Addresses
}

func supportedPersonAddresses(rows []personDraftAddress) []string {
	addresses := make([]string, 0, len(rows))
	for _, row := range rows {
		if row.Supported {
			addresses = append(addresses, row.Value)
		}
	}
	return addresses
}

func TestDraftComposePersonArgs(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	intent, err := parseDraftComposeArgs([]string{"draft-compose", "--person-id", "7"})
	require.NoError(err)
	assert.Equal(int64(7), intent.PersonID)
	assert.False(intent.JSON)
	intent, err = parseDraftComposeArgs([]string{"draft-compose", "--person-id=7", "--json"})
	require.NoError(err)
	assert.Equal(int64(7), intent.PersonID)
	assert.True(intent.JSON)
	intent, err = parseDraftComposeArgs([]string{
		"draft-compose", "--person-id", "7", "--account", "owner@example.com",
		"--to", "Carol <carol@example.com>", "--cc", "copy@example.com", "--body", "x",
	})
	require.NoError(err)
	assert.Equal([]string{"Carol <carol@example.com>"}, intent.To)
	assert.Equal([]string{"copy@example.com"}, intent.Cc)
	intent, err = parseDraftComposeArgs([]string{"draft-compose", "--source-id", "42", "--cc", "copy@example.com"})
	require.NoError(err)
	assert.Zero(intent.PersonID)
	assert.Equal([]string{"copy@example.com"}, intent.Cc)

	for _, args := range [][]string{
		{"draft-compose", "--person-id", "0"},
		{"draft-compose", "--person-id", "-3"},
		{"draft-compose", "--person-id", "seven"},
		{"draft-compose", "--person-id", "7", "--person-id", "8"},
		{"draft-compose", "--person-id", "7", "--body", "x"},
		{"draft-compose", "--person-id", "7", "--cc", "copy@example.com"},
		{"draft-compose", "--person-id", "7", "--account", "owner@example.com"},
		{"draft-compose", "--person-id", "7", "--source-id", "42", "--to", "a@example.com", "--to", "b@example.com"},
		{"draft-compose", "--person-id", "7", "--conversation", "3", "--body", "x"},
		{"draft-compose", "--person-id", "7", "--to", "a@example.com"},
	} {
		_, err := parseDraftComposeArgs(args)
		require.Error(err, args)
		assert.Equal("invalid_args", err.Error(), args)
	}
}

func TestDraftComposePersonListsArchivedAddresses(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := newPersonDraftFixture(t)

	rows := f.listPerson(t, f.adapter, f.personID)
	assert.ElementsMatch([]personDraftAddress{
		{Kind: "email", Value: "carol@example.com", Supported: true},
		{Kind: "email", Value: "carol.alt@example.com", Supported: true},
		{Kind: "phone", Value: "+15555550100"},
		{Kind: "imessage", Value: "carol-chat@example.org"},
		{Kind: "matrix", Value: personDraftSanitizedValue},
	}, rows)

	events, err := f.runCompose(t, f.adapter, nil, "--person-id", strconv.FormatInt(f.personID, 10))
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal(cliStreamStdout, events[0].Type)
	text := events[0].Data
	assert.Contains(text, "email\tcarol@example.com\tsupported\n")
	assert.Contains(text, "email\tcarol.alt@example.com\tsupported\n")
	assert.Contains(text, "phone\t+15555550100\tunsupported\n")
	assert.Contains(text, "imessage\tcarol-chat@example.org\tunsupported\n")
	assert.Contains(text, "matrix\t@carol:example.org\tunsupported\n")
	assert.NotContains(text, "\x1b")
	assert.NotContains(text, "unrelated@example.com")
	assert.NotContains(text, "curated-only@example.com")
	assert.NotContains(text, "Carol")
	assert.Zero(*f.providerCalls)
}

func TestDraftComposePersonCreatesDraftToChosenAddress(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := newPersonDraftFixture(t)
	supported := supportedPersonAddresses(f.listPerson(t, f.adapter, f.personID))
	require.Len(supported, 2)
	chosen := supported[1]
	require.Equal("carol.alt@example.com", chosen)

	server := httptest.NewServer(api.NewServerWithOptions(api.ServerOptions{
		Config: &config.Config{
			HomeDir: t.TempDir(),
			Server:  config.ServerConfig{APIKey: "owner-test-key"},
		},
		Store:  f.adapter,
		Logger: slog.New(slog.DiscardHandler),
	}).Router())
	t.Cleanup(server.Close)
	body, err := json.Marshal(map[string]any{"args": []string{
		"draft-compose", "--person-id", strconv.FormatInt(f.personID, 10),
		"--source-id", strconv.FormatInt(f.source.ID, 10), "--from", testutil.IMAPTestUsername,
		"--to", chosen, "--cc", "unverified-copy@example.com",
		"--subject", "Person subject", "--body", "person body", "--json",
	}})
	require.NoError(err)
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/cli/run", bytes.NewReader(body))
	require.NoError(err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Api-Key", "owner-test-key")
	response, err := http.DefaultClient.Do(request)
	require.NoError(err)
	defer func() { _ = response.Body.Close() }()
	require.Equal(http.StatusOK, response.StatusCode)
	var events []api.CLIRunEvent
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		var event api.CLIRunEvent
		require.NoError(json.Unmarshal(scanner.Bytes(), &event))
		events = append(events, event)
	}
	require.NoError(scanner.Err())
	require.Len(events, 2)
	require.Equal(cliStreamStdout, events[0].Type)
	require.Equal("complete", events[1].Type)

	var result draftReplyOutput
	require.NoError(json.Unmarshal([]byte(events[0].Data), &result))
	assert.Equal(draftReplyStatusCreated, result.Status)
	assert.Equal(1, *f.providerCalls)
	message, err := f.store.GetMessage(result.MessageID)
	require.NoError(err)
	assert.Equal([]string{chosen}, message.To)
	assert.Equal([]string{"unverified-copy@example.com"}, message.Cc)
	storedRaw, err := f.store.GetMessageRaw(result.MessageID)
	require.NoError(err)
	assert.Contains(string(storedRaw), "To: <"+chosen+">")
	assert.Contains(string(storedRaw), "Cc: <unverified-copy@example.com>")
	assert.NotContains(string(storedRaw), "carol@example.com")
	draft, err := f.store.GetIMAPDraft(result.DraftID)
	require.NoError(err)
	_, fetchedRaw := fetchDraftMailboxMessage(t, f.config, draft.CurrentReceipt)
	assert.Equal(storedRaw, fetchedRaw)
}

func TestDraftComposePersonRejectsAddressOutsidePerson(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := newPersonDraftFixture(t)
	for _, to := range []string{
		"unrelated@example.com",
		"curated-only@example.com",
		"carol-chat@example.org",
		"carol.alt@example.com, other@example.com",
		"not an address",
	} {
		events, err := f.composeToPerson(t, f.adapter, f.personID, to)
		var coded *api.CLIRunCodedError
		require.ErrorAs(err, &coded, to)
		assert.Equal("invalid_compose_metadata", coded.Code, to)
		assert.NotContains(coded.Err.Error(), "@", to)
		assert.Empty(events, to)
	}
	assert.Zero(*f.providerCalls)
}

func TestDraftComposePersonBindings(t *testing.T) {
	type bindingsFixture struct {
		draftReplyFixture

		adapter              *storeAPIAdapter
		survivor, absorbed   *store.Person
		absorbedParticipants []int64
		merge                *store.PersonMergeResult
	}
	setup := func(t *testing.T) bindingsFixture {
		t.Helper()
		require := require.New(t)
		fixture := newDraftReplyFixture(t)
		st := fixture.store
		survivorParticipant, err := st.EnsureParticipant("survivor@example.com", "", "example.com")
		require.NoError(err)
		firstAbsorbed, err := st.EnsureParticipant("absorbed-1@example.com", "", "example.com")
		require.NoError(err)
		secondAbsorbed, err := st.EnsureParticipant("absorbed-2@example.com", "", "example.com")
		require.NoError(err)
		_, err = st.LinkParticipants(firstAbsorbed, secondAbsorbed)
		require.NoError(err)
		survivor, _, err := st.CreatePersonFromParticipantContext(t.Context(), survivorParticipant)
		require.NoError(err)
		absorbed, _, err := st.CreatePersonFromParticipantContext(t.Context(), firstAbsorbed)
		require.NoError(err)
		merged, err := st.MergePersonsContext(t.Context(), store.PersonMergeRequest{
			SurvivorID: survivor.ID, AbsorbedID: absorbed.ID,
			ExpectedSurvivorRevision: survivor.Revision, ExpectedAbsorbedRevision: absorbed.Revision,
			IdempotencyKey: "compose-person-merge", Actor: "test",
		})
		require.NoError(err)
		adapter, _ := countingDraftAdapter(fixture)
		return bindingsFixture{
			draftReplyFixture: fixture, adapter: adapter, survivor: &merged.Person, absorbed: absorbed,
			absorbedParticipants: []int64{firstAbsorbed, secondAbsorbed}, merge: merged,
		}
	}
	accepts := func(t *testing.T, f bindingsFixture, personID int64, to string) {
		t.Helper()
		events, err := f.composeToPerson(t, f.adapter, personID, to)
		require.NoError(t, err, to)
		require.Len(t, events, 1, to)
		var result draftReplyOutput
		require.NoError(t, json.Unmarshal([]byte(events[0].Data), &result))
		assert.Equal(t, draftReplyStatusCreated, result.Status, to)
	}
	rejects := func(t *testing.T, f bindingsFixture, personID int64, to string) {
		t.Helper()
		_, err := f.composeToPerson(t, f.adapter, personID, to)
		require.Error(t, err, to)
		assert.Equal(t, "invalid_compose_metadata", err.Error(), to)
	}
	split := func(t *testing.T, f bindingsFixture, participants []int64) *store.PersonSplitResult {
		t.Helper()
		result, err := f.store.SplitPersonMergeContext(t.Context(), store.PersonSplitRequest{
			SourcePersonID: f.survivor.ID, MergeID: f.merge.Merge.ID, ParticipantIDs: participants,
			ExpectedSourceRevision: f.survivor.Revision, IdempotencyKey: "compose-person-split", Actor: "test",
		})
		require.NoError(t, err)
		return result
	}

	t.Run("merge", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		f := setup(t)
		_, err := f.runCompose(t, f.adapter, nil, "--person-id", strconv.FormatInt(f.absorbed.ID, 10))
		require.Error(err)
		assert.Equal("invalid_args", err.Error())
		_, err = f.composeToPerson(t, f.adapter, f.absorbed.ID, "absorbed-1@example.com")
		require.Error(err)
		assert.Equal("invalid_args", err.Error())
		assert.ElementsMatch(
			[]string{"survivor@example.com", "absorbed-1@example.com", "absorbed-2@example.com"},
			supportedPersonAddresses(f.listPerson(t, f.adapter, f.survivor.ID)))
		accepts(t, f, f.survivor.ID, "survivor@example.com")
		accepts(t, f, f.survivor.ID, "absorbed-2@example.com")
	})

	t.Run("exact_split", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		f := setup(t)
		result := split(t, f, f.absorbedParticipants)
		require.True(result.ExactReversal)
		assert.ElementsMatch([]string{"absorbed-1@example.com", "absorbed-2@example.com"},
			supportedPersonAddresses(f.listPerson(t, f.adapter, result.NewPerson.ID)))
		rejects(t, f, result.SourcePerson.ID, "absorbed-1@example.com")
		accepts(t, f, result.NewPerson.ID, "absorbed-1@example.com")
		accepts(t, f, result.SourcePerson.ID, "survivor@example.com")
		rejects(t, f, result.NewPerson.ID, "survivor@example.com")
	})

	t.Run("partial_split", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		f := setup(t)
		result := split(t, f, []int64{f.absorbedParticipants[0]})
		require.False(result.ExactReversal)
		assert.ElementsMatch([]string{"absorbed-1@example.com"},
			supportedPersonAddresses(f.listPerson(t, f.adapter, result.NewPerson.ID)))
		assert.ElementsMatch([]string{"survivor@example.com", "absorbed-2@example.com"},
			supportedPersonAddresses(f.listPerson(t, f.adapter, result.SourcePerson.ID)))
		accepts(t, f, result.NewPerson.ID, "absorbed-1@example.com")
		rejects(t, f, result.NewPerson.ID, "absorbed-2@example.com")
		rejects(t, f, result.NewPerson.ID, "survivor@example.com")
		accepts(t, f, result.SourcePerson.ID, "absorbed-2@example.com")
		rejects(t, f, result.SourcePerson.ID, "absorbed-1@example.com")
	})
}

func TestDraftComposePersonIsOwnerOnly(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := newPersonDraftFixture(t)
	grant := &agentgrant.Grant{
		ID:          "compose-grant",
		Permissions: []agentgrant.Permission{agentgrant.PermissionDraftCreate},
		Sources:     []agentgrant.SourceRef{{ID: f.source.ID, Type: f.source.SourceType, Identifier: f.source.Identifier}},
	}
	for _, personID := range []int64{f.personID, f.personID + 1000} {
		person := strconv.FormatInt(personID, 10)
		for _, args := range [][]string{
			{"--person-id", person},
			{"--person-id", person, "--json"},
			{"--person-id", person, "--source-id", strconv.FormatInt(f.source.ID, 10),
				"--from", testutil.IMAPTestUsername, "--to", "carol@example.com", "--body", "x", "--json"},
		} {
			events, err := f.runCompose(t, f.adapter, grant, args...)
			require.Error(err, args)
			assert.Equal("not_permitted", err.Error(), args)
			assert.Empty(events, args)
		}
	}
	assert.Zero(*f.providerCalls)
}

func TestDraftComposePersonKeepsSourceChecks(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := newPersonDraftFixture(t)
	person := strconv.FormatInt(f.personID, 10)

	_, err := f.runCompose(t, f.adapter, nil, "--person-id", person, "--to", "carol@example.com", "--body", "x")
	require.Error(err)
	assert.Equal("invalid_args", err.Error())

	require.NoError(f.store.AddAccountIdentity(f.source.ID, "alias@example.com", "manual"))
	events, err := f.runCompose(t, f.adapter, nil, "--person-id", person,
		"--source-id", strconv.FormatInt(f.source.ID, 10), "--to", "carol@example.com", "--body", "x")
	require.Error(err)
	assert.Equal("from_ambiguous", err.Error())
	assert.Empty(events)

	f.adapter.draftPolicy = nil
	events, err = f.composeToPerson(t, f.adapter, f.personID, "carol@example.com")
	require.Error(err)
	assert.Equal("draft_disabled", err.Error())
	assert.Empty(events)
	assert.Zero(*f.providerCalls)
}

func TestDraftComposePersonListsCaseDistinctIdentifiers(t *testing.T) {
	require := require.New(t)
	f := newPersonDraftFixture(t)
	st := f.store
	person, err := st.GetPersonContext(t.Context(), f.personID)
	require.NoError(err)
	upperID, err := st.EnsureParticipantByIdentifier("matrix", "@Carol:example.org", "")
	require.NoError(err)
	lowerID, err := st.EnsureParticipantByIdentifier("matrix", "@carol:example.org", "")
	require.NoError(err)
	require.NotEqual(upperID, lowerID)
	for _, member := range []int64{upperID, lowerID} {
		_, err = st.LinkParticipants(person.ParticipantIDs[0], member)
		require.NoError(err)
	}
	person, err = st.GetPersonContext(t.Context(), f.personID)
	require.NoError(err)
	require.Subset(person.ParticipantIDs, []int64{upperID, lowerID})

	rows := f.listPerson(t, f.adapter, f.personID)
	assert.Subset(t, rows, []personDraftAddress{
		{Kind: "matrix", Value: "@Carol:example.org"},
		{Kind: "matrix", Value: "@carol:example.org"},
	})
}
