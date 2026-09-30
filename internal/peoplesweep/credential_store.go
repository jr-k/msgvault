package peoplesweep

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"go.kenn.io/msgvault/internal/providercredentials"
)

var (
	credentialProfileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	ErrCredentialNotFound        = errors.New("people provider credential not found")
)

const legacyCredentialNamespace = "people-providers"

// Credential carries an authentication scheme and an opaque secret. The
// pointer-backed secret prevents fmt's value-%p special case from inspecting
// the underlying string when it bypasses Formatter.
type Credential struct {
	Scheme AuthScheme
	secret *credentialSecret
}

type credentialSecret struct {
	value string
}

// NewCredential constructs an opaque provider credential.
func NewCredential(scheme AuthScheme, value string) Credential {
	return Credential{Scheme: scheme, secret: &credentialSecret{value: value}}
}

// Value returns the secret only for explicit use at the provider boundary.
func (c Credential) Value() string {
	if c.secret == nil {
		return ""
	}
	return c.secret.value
}

func (c Credential) hasValue() bool {
	return c.secret != nil && c.secret.value != ""
}

func (c Credential) String() string {
	return fmt.Sprintf("people provider credential (%s)", c.Scheme)
}

func (c Credential) GoString() string {
	return c.String()
}

// Format prevents supported fmt verbs from inspecting credential internals.
func (c Credential) Format(state fmt.State, _ rune) {
	_, _ = io.WriteString(state, c.String())
}

// CredentialStore maps people provider profiles onto the shared provider
// credential store. Revisions cover one credential, absent included.
type CredentialStore interface {
	Revision(profileName, endpoint string) (string, bool, error)
	Load(profileName, endpoint string) (string, error)
	SaveIfRevision(profileName, endpoint, value, expected string) (string, error)
	DeleteIfRevision(profileName, endpoint, expected string) (string, error)
}

var ErrCredentialRevisionConflict = providercredentials.ErrConflict

// CredentialResolver resolves only the source fingerprinted into a profile.
type CredentialResolver interface {
	Resolve(profileName string, profile ProviderProfile) (Credential, error)
}

// credentialFile is the format of legacy <tokens>/people-providers files.
type credentialFile struct {
	Scheme AuthScheme `json:"scheme"`
	Value  string     `json:"value"`
}

// StoredCredentials keeps people provider keys in provider-credentials.json
// under people.provider/<name>, bound to the profile endpoint's origin.
type StoredCredentials struct {
	tokensDir string
}

func NewStoredCredentials(tokensDir string) StoredCredentials {
	return StoredCredentials{tokensDir: tokensDir}
}

func (s StoredCredentials) read(profileName, endpoint string) (providercredentials.Snapshot, string, error) {
	if err := validateCredentialProfileName(profileName); err != nil {
		return providercredentials.Snapshot{}, "", err
	}
	if err := s.importLegacy(profileName, endpoint); err != nil {
		return providercredentials.Snapshot{}, "", err
	}
	snapshot, err := providercredentials.Read(s.tokensDir)
	return snapshot, providercredentials.PeopleProviderID(profileName), err
}

func (s StoredCredentials) Revision(profileName, endpoint string) (string, bool, error) {
	snapshot, id, err := s.read(profileName, endpoint)
	if err != nil {
		return "", false, err
	}
	revision, err := snapshot.Revision(id)
	return revision, snapshot.Stored(id), err
}

func (s StoredCredentials) Load(profileName, endpoint string) (string, error) {
	snapshot, id, err := s.read(profileName, endpoint)
	if err != nil {
		return "", err
	}
	if !snapshot.Stored(id) {
		return "", fmt.Errorf("%w for profile %q", ErrCredentialNotFound, profileName)
	}
	value, _, err := snapshot.Resolve(id, endpoint, "", nil)
	return value, err
}

func (s StoredCredentials) SaveIfRevision(profileName, endpoint, value, expected string) (string, error) {
	_, id, err := s.read(profileName, endpoint)
	if err != nil {
		return "", err
	}
	saved, err := providercredentials.PutIfRevision(s.tokensDir, expected, id, endpoint, value)
	if err != nil {
		return "", err
	}
	return saved.Revision(id)
}

func (s StoredCredentials) DeleteIfRevision(profileName, endpoint, expected string) (string, error) {
	snapshot, id, err := s.read(profileName, endpoint)
	if err != nil {
		return "", err
	}
	if !snapshot.Stored(id) {
		return "", fmt.Errorf("%w for profile %q", ErrCredentialNotFound, profileName)
	}
	deleted, err := providercredentials.DeleteIfRevision(s.tokensDir, expected, id)
	if err != nil {
		return "", err
	}
	return deleted.Revision(id)
}

// importLegacy moves a key written by an older release into the shared store,
// then removes the old file. A key already stored wins over the old file.
func (s StoredCredentials) importLegacy(profileName, endpoint string) error {
	path := filepath.Join(s.tokensDir, legacyCredentialNamespace, profileName+".json")
	file, err := os.Open(path) // #nosec G304 -- validated profile name under the tokens directory.
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("import legacy people provider credential %s (left in place): %w", path, err)
	}
	var stored credentialFile
	decoder := jsontext.NewDecoder(io.LimitReader(file, 16<<10), json.RejectUnknownMembers(true))
	decodeErr := json.UnmarshalDecode(decoder, &stored)
	if decodeErr == nil {
		decodeErr = requireCredentialJSONEnd(decoder)
	}
	_ = file.Close()
	if decodeErr != nil {
		return fmt.Errorf("import legacy people provider credential %s (left in place): malformed JSON", path)
	}
	id := providercredentials.PeopleProviderID(profileName)
	snapshot, err := providercredentials.Read(s.tokensDir)
	if err == nil && !snapshot.Stored(id) {
		var absent string
		if absent, err = snapshot.Revision(id); err == nil {
			_, err = providercredentials.PutIfRevision(s.tokensDir, absent, id, endpoint, stored.Value)
		}
		if errors.Is(err, providercredentials.ErrConflict) {
			err = nil // Another process imported it first.
		}
	}
	if err != nil {
		return fmt.Errorf("import legacy people provider credential %s (left in place): %w", path, err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove imported legacy people provider credential %s: %w", path, err)
	}
	return nil
}

// ValidateProviderProfileName applies the single grammar used by provider
// config, commands, and the private credential namespace.
func ValidateProviderProfileName(profileName string) error {
	if !credentialProfileNamePattern.MatchString(profileName) {
		return errors.New("invalid people provider profile name")
	}
	return nil
}

func validateCredentialProfileName(profileName string) error {
	if err := ValidateProviderProfileName(profileName); err != nil {
		return fmt.Errorf("invalid people provider credential profile name: %w", err)
	}
	return nil
}

func requireCredentialJSONEnd(decoder *jsontext.Decoder) error {
	var extra any
	if err := json.UnmarshalDecode(decoder, &extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("credential file contains multiple JSON values")
		}
		return err
	}
	return nil
}

type credentialResolver struct {
	store  CredentialStore
	lookup CredentialLookup
}

// NewCredentialResolver constructs the sole credential-source dispatcher.
func NewCredentialResolver(store CredentialStore, lookup CredentialLookup) CredentialResolver {
	return &credentialResolver{store: store, lookup: lookup}
}

func (r *credentialResolver) Resolve(profileName string, profile ProviderProfile) (Credential, error) {
	if err := profile.Validate(); err != nil {
		return Credential{}, fmt.Errorf("validate people provider profile before credential resolution: %w", err)
	}
	switch profile.Credential {
	case CredentialStored:
		if profileName != profile.CredentialRef {
			return Credential{}, errors.New("stored people provider credential name does not match the fingerprinted profile")
		}
		if r.store == nil {
			return Credential{}, errors.New("people provider credential store is unavailable")
		}
		value, err := r.store.Load(profileName, profile.Endpoint)
		if err != nil {
			return Credential{}, err
		}
		return NewCredential(profile.Auth, value), nil
	case CredentialEnv:
		if r.lookup == nil {
			return Credential{}, errors.New("people provider credential environment lookup is unavailable")
		}
		value, ok := r.lookup(profile.CredentialRef)
		if !ok || value == "" {
			return Credential{}, fmt.Errorf("people provider credential environment variable %s is not set", profile.CredentialRef)
		}
		return NewCredential(profile.Auth, value), nil
	case CredentialNone:
		return NewCredential(AuthNone, ""), nil
	default:
		return Credential{}, errors.New("people provider profile has an invalid credential source")
	}
}
