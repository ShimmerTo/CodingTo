package browserworkflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	// workflowDirName is the directory that hosts browser profile metadata and
	// persistent sessions. It lives under the global CodingTo config directory
	// and is shared by every agent.
	workflowDirName = "browser-profile"
	// profilesDirName holds one subdirectory per stored profile key.
	profilesDirName = "profiles"
	// profileFileName is the JSON document describing a single profile.
	profileFileName = "profile.json"
	// secretFileName is the encrypted credential blob stored inside a profile dir.
	secretFileName = "credential.bin"
)

// ProfileBaseDir returns the directory where browser profiles are stored as a
// single global resource shared by every agent: <home>/.codingto/browser-profile.
// All agents read from and write to this one location, so a profile saved by one
// agent is available to any other agent.
func ProfileBaseDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to resolve home directory: %w", err)
	}
	return filepath.Join(home, ".codingto", workflowDirName), nil
}

// ProfileDir returns the global directory where all browser profiles are stored.
func ProfileDir() (string, error) {
	base, err := ProfileBaseDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, profilesDirName), nil
}

var profileKeyPattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]{0,62}[A-Za-z0-9])?$`)

var reservedProfileKeys = regexp.MustCompile(`(?i)^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\..*)?$`)

// LoginRecipe describes the form fields agent-browser should use for one
// persistent browser session. It intentionally contains no credential values.
type LoginRecipe struct {
	LoginURL         string `json:"loginUrl"`
	UsernameSelector string `json:"usernameSelector,omitempty"`
	PasswordSelector string `json:"passwordSelector,omitempty"`
	SubmitSelector   string `json:"submitSelector,omitempty"`
}

// BrowserState points at the persistent browser directory owned by a profile.
type BrowserState struct {
	Kind string `json:"kind"`
	Path string `json:"path"`
}

// Profile is the non-secret metadata stored in the global browser profile
// directory. CredentialRef is an opaque lookup key; the username and password
// live only in the platform credential store payload.
type Profile struct {
	ID                   string       `json:"id"`
	Name                 string       `json:"name"`
	Origins              []string     `json:"origins"`
	BrowserState         BrowserState `json:"browserState"`
	CredentialRef        string       `json:"credentialRef,omitempty"`
	CredentialConfigured bool         `json:"credentialConfigured"`
	LoginRecipe          LoginRecipe  `json:"loginRecipe"`
	CreatedAt            time.Time    `json:"createdAt"`
	UpdatedAt            time.Time    `json:"updatedAt"`
	LastUsedAt           *time.Time   `json:"lastUsedAt,omitempty"`
}

// SaveRequest is submitted by CodingTo's dedicated credential dialog. The
// password is consumed immediately and is never copied into Profile metadata.
type SaveRequest struct {
	ProfileID        string `json:"profileId,omitempty"`
	Key              string `json:"key,omitempty"`
	Name             string `json:"name,omitempty"`
	TargetURL        string `json:"targetUrl"`
	LoginURL         string `json:"loginUrl"`
	AuthMode         string `json:"authMode"`
	Username         string `json:"username,omitempty"`
	Password         string `json:"password,omitempty"`
	UsernameSelector string `json:"usernameSelector,omitempty"`
	PasswordSelector string `json:"passwordSelector,omitempty"`
	SubmitSelector   string `json:"submitSelector,omitempty"`
}

type storedCredential struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// CredentialStoreName reports the platform-backed secret store available to
// this build. An empty value means persistent sessions still work, but saved
// credentials must be disabled and login stays interactive.
func CredentialStoreName() string { return credentialStoreName() }

// Save creates or updates a persistent browser session below baseDir.
func Save(baseDir string, req SaveRequest) (Profile, error) {
	key := strings.TrimSpace(req.Key)
	if key == "" {
		// Name was used by releases that predate persistent-session keys. Keep
		// accepting it at the API boundary so an older frontend can still create
		// a session after the backend has been upgraded.
		key = strings.TrimSpace(req.Name)
	}
	if err := validateProfileKey(key); err != nil {
		return Profile{}, err
	}
	targetOrigin, err := normalizedOrigin(req.TargetURL)
	if err != nil {
		return Profile{}, fmt.Errorf("invalid target URL: %w", err)
	}
	loginURL := strings.TrimSpace(req.LoginURL)
	if loginURL == "" {
		loginURL = strings.TrimSpace(req.TargetURL)
	}
	loginOrigin, err := normalizedOrigin(loginURL)
	if err != nil {
		return Profile{}, fmt.Errorf("invalid login URL: %w", err)
	}

	if req.ProfileID == "" {
		if err := ensureProfileKeyAvailable(baseDir, key); err != nil {
			return Profile{}, err
		}
	}

	now := time.Now().UTC()
	profile := Profile{}
	if req.ProfileID != "" {
		profile, err = Load(baseDir, req.ProfileID)
		if err != nil {
			return Profile{}, err
		}
		if !strings.EqualFold(profile.ID, key) {
			return Profile{}, errors.New("Browser Profile Key 创建后不能修改")
		}
	} else {
		profile.ID = key
		profile.CreatedAt = now
	}
	profile.Name = key
	profile.Origins = uniqueStrings([]string{targetOrigin, loginOrigin})
	profile.BrowserState = BrowserState{Kind: "persistent-profile", Path: "user-data"}
	profile.LoginRecipe = LoginRecipe{
		LoginURL:         loginURL,
		UsernameSelector: strings.TrimSpace(req.UsernameSelector),
		PasswordSelector: strings.TrimSpace(req.PasswordSelector),
		SubmitSelector:   strings.TrimSpace(req.SubmitSelector),
	}
	profile.UpdatedAt = now

	profileDir, err := ensureProfileDir(baseDir, profile.ID)
	if err != nil {
		return Profile{}, err
	}
	if err := ensurePrivateDir(filepath.Join(profileDir, profile.BrowserState.Path)); err != nil {
		return Profile{}, fmt.Errorf("create browser state directory: %w", err)
	}

	switch req.AuthMode {
	case "saved-credential":
		if CredentialStoreName() == "" {
			return Profile{}, errors.New("saved browser credentials are not supported on this operating system")
		}
		if strings.TrimSpace(req.Username) == "" || req.Password == "" {
			return Profile{}, errors.New("username and password are required for automatic login")
		}
		plain, err := json.Marshal(storedCredential{Username: strings.TrimSpace(req.Username), Password: req.Password})
		if err != nil {
			return Profile{}, fmt.Errorf("encode credential: %w", err)
		}
		protected, protectErr := protectCredential(plain)
		clearBytes(plain)
		if protectErr != nil {
			return Profile{}, fmt.Errorf("protect credential: %w", protectErr)
		}
		if err := writePrivateFile(filepath.Join(profileDir, secretFileName), protected); err != nil {
			clearBytes(protected)
			return Profile{}, fmt.Errorf("write protected credential: %w", err)
		}
		clearBytes(protected)
		profile.CredentialRef = profile.ID
		profile.CredentialConfigured = true
	case "manual", "":
		if err := os.Remove(filepath.Join(profileDir, secretFileName)); err != nil && !os.IsNotExist(err) {
			return Profile{}, fmt.Errorf("remove saved credential: %w", err)
		}
		profile.CredentialRef = ""
		profile.CredentialConfigured = false
	default:
		return Profile{}, fmt.Errorf("unsupported auth mode %q", req.AuthMode)
	}

	if err := writeProfile(profileDir, profile); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

// List returns profile metadata, optionally limited to profiles whose allowed
// origins include targetURL's origin. No secret is opened by this operation.
func List(baseDir, targetURL string) ([]Profile, error) {
	root := filepath.Join(baseDir, profilesDirName)
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return []Profile{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list browser profiles: %w", err)
	}
	filterOrigin := ""
	if strings.TrimSpace(targetURL) != "" {
		filterOrigin, err = normalizedOrigin(targetURL)
		if err != nil {
			return nil, fmt.Errorf("invalid target URL: %w", err)
		}
	}
	profiles := make([]Profile, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || validateProfileKey(entry.Name()) != nil {
			continue
		}
		profile, loadErr := Load(baseDir, entry.Name())
		if loadErr != nil {
			continue
		}
		if filterOrigin == "" || contains(profile.Origins, filterOrigin) {
			profiles = append(profiles, profile)
		}
	}
	sort.Slice(profiles, func(i, j int) bool {
		left, right := profiles[i].UpdatedAt, profiles[j].UpdatedAt
		if profiles[i].LastUsedAt != nil {
			left = *profiles[i].LastUsedAt
		}
		if profiles[j].LastUsedAt != nil {
			right = *profiles[j].LastUsedAt
		}
		return left.After(right)
	})
	return profiles, nil
}

// Delete removes a persistent browser session directory for an agent, including
// its protected credential file. The profile ID is validated before any removal
// so a malformed value can never delete an unrelated directory.
func Delete(baseDir, profileID string) error {
	dir, err := profileDir(baseDir, profileID)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("delete browser profile: %w", err)
	}
	return nil
}

// Rename changes the display name of a persistent browser session without
// touching its immutable key or stored credentials. The updated profile is
// persisted and returned.
func Rename(baseDir, profileID, newName string) (Profile, error) {
	if err := validateProfileKey(profileID); err != nil {
		return Profile{}, err
	}
	trimmed := strings.TrimSpace(newName)
	if trimmed == "" {
		return Profile{}, errors.New("browser profile name must not be empty")
	}
	profile, err := Load(baseDir, profileID)
	if err != nil {
		return Profile{}, err
	}
	profile.Name = trimmed
	profile.UpdatedAt = time.Now().UTC()
	dir, err := profileDir(baseDir, profileID)
	if err != nil {
		return Profile{}, err
	}
	if err := writeProfile(dir, profile); err != nil {
		return Profile{}, err
	}
	return profile, nil
}

// Load reads one non-secret profile after validating its opaque ID.
func Load(baseDir, profileID string) (Profile, error) {
	profileDir, err := profileDir(baseDir, profileID)
	if err != nil {
		return Profile{}, err
	}
	raw, err := os.ReadFile(filepath.Join(profileDir, profileFileName))
	if err != nil {
		return Profile{}, fmt.Errorf("read browser profile: %w", err)
	}
	var profile Profile
	if err := json.Unmarshal(raw, &profile); err != nil {
		return Profile{}, fmt.Errorf("decode browser profile: %w", err)
	}
	if profile.ID != profileID {
		return Profile{}, errors.New("browser profile ID mismatch")
	}
	return profile, nil
}

// HasVaultCredential reports whether profileKey has a usable saved credential
// that the codingto-vault provider can serve to agent-browser. It mirrors the
// precondition checks performed by RunCredentialProvider but returns only a
// boolean and never reads or returns secret bytes.
func HasVaultCredential(baseDir, profileKey string) bool {
	profile, err := Load(baseDir, profileKey)
	if err != nil || !profile.CredentialConfigured || profile.CredentialRef != profile.ID {
		return false
	}
	dir, err := profileDir(baseDir, profile.ID)
	if err != nil {
		return false
	}
	protected, err := os.ReadFile(filepath.Join(dir, secretFileName))
	if err != nil || len(protected) == 0 {
		return false
	}
	return true
}

// PersistentProfilePath returns the absolute browser user-data directory.
func PersistentProfilePath(baseDir string, profile Profile) (string, error) {
	dir, err := profileDir(baseDir, profile.ID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, profile.BrowserState.Path), nil
}

// RunCredentialProvider implements agent-browser.plugin.v1 credential.read.
// It writes exactly one JSON response and never includes secret values in
// errors. The provider process is intended to exit immediately afterwards.
func RunCredentialProvider(in io.Reader, out io.Writer, baseDir string) error {
	var envelope struct {
		Protocol   string `json:"protocol"`
		Type       string `json:"type"`
		Capability string `json:"capability"`
		Request    struct {
			ItemRef     string `json:"itemRef"`
			URL         string `json:"url"`
			ProfileName string `json:"profileName"`
		} `json:"request"`
	}
	if err := json.NewDecoder(io.LimitReader(in, 64*1024)).Decode(&envelope); err != nil {
		return writeProviderFailure(out)
	}
	if envelope.Protocol != "agent-browser.plugin.v1" || envelope.Type != "credential.resolve" || envelope.Capability != "credential.read" {
		return writeProviderFailure(out)
	}
	profile, err := Load(baseDir, envelope.Request.ItemRef)
	if err != nil || !profile.CredentialConfigured || profile.CredentialRef != profile.ID {
		return writeProviderFailure(out)
	}
	requestedURL := strings.TrimSpace(envelope.Request.URL)
	if requestedURL == "" {
		requestedURL = profile.LoginRecipe.LoginURL
	}
	origin, err := normalizedOrigin(requestedURL)
	if err != nil || !contains(profile.Origins, origin) {
		return writeProviderFailure(out)
	}
	dir, _ := profileDir(baseDir, profile.ID)
	protected, err := os.ReadFile(filepath.Join(dir, secretFileName))
	if err != nil {
		return writeProviderFailure(out)
	}
	plain, err := unprotectCredential(protected)
	clearBytes(protected)
	if err != nil {
		return writeProviderFailure(out)
	}
	defer clearBytes(plain)
	var credential storedCredential
	if err := json.Unmarshal(plain, &credential); err != nil || credential.Username == "" || credential.Password == "" {
		return writeProviderFailure(out)
	}
	credentialResponse := map[string]string{
		"username": credential.Username,
		"password": credential.Password,
		"url":      profile.LoginRecipe.LoginURL,
	}
	if profile.LoginRecipe.UsernameSelector != "" {
		credentialResponse["usernameSelector"] = profile.LoginRecipe.UsernameSelector
	}
	if profile.LoginRecipe.PasswordSelector != "" {
		credentialResponse["passwordSelector"] = profile.LoginRecipe.PasswordSelector
	}
	if profile.LoginRecipe.SubmitSelector != "" {
		credentialResponse["submitSelector"] = profile.LoginRecipe.SubmitSelector
	}
	response := map[string]any{
		"protocol":   "agent-browser.plugin.v1",
		"success":    true,
		"credential": credentialResponse,
	}
	return json.NewEncoder(out).Encode(response)
}

func writeProviderFailure(out io.Writer) error {
	return json.NewEncoder(out).Encode(map[string]any{
		"protocol": "agent-browser.plugin.v1",
		"success":  false,
		"error":    "credential unavailable",
	})
}

func normalizedOrigin(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return "", errors.New("only http(s) URLs without embedded credentials are allowed")
	}
	host := strings.ToLower(parsed.Hostname())
	port := parsed.Port()
	if (parsed.Scheme == "https" && port == "443") || (parsed.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return strings.ToLower(parsed.Scheme + "://" + host), nil
}

func ensureProfileDir(baseDir, profileID string) (string, error) {
	dir, err := profileDir(baseDir, profileID)
	if err != nil {
		return "", err
	}
	if err := ensurePrivateDir(dir); err != nil {
		return "", fmt.Errorf("create browser profile directory: %w", err)
	}
	return dir, nil
}

// ensureProfileKeyAvailable checks directory names instead of only loading
// valid profile.json files. A partially-created or damaged profile directory
// still owns its key and must never be reused or written into. EqualFold keeps
// the rule consistent with Windows' case-insensitive filesystem semantics.
func ensureProfileKeyAvailable(baseDir, key string) error {
	root := filepath.Join(baseDir, profilesDirName)
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("list browser profile directories: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.EqualFold(entry.Name(), key) {
			return fmt.Errorf("Browser Profile Key %q 已存在，请使用其他 Key", key)
		}
	}
	return nil
}

func profileDir(baseDir, profileID string) (string, error) {
	if strings.TrimSpace(baseDir) == "" {
		return "", errors.New("browser profile directory is required")
	}
	if err := validateProfileKey(profileID); err != nil {
		return "", err
	}
	return filepath.Join(baseDir, profilesDirName, profileID), nil
}

func validateProfileKey(key string) error {
	if !profileKeyPattern.MatchString(key) || reservedProfileKeys.MatchString(key) {
		return errors.New("Browser Profile Key 必须为 1 到 64 位，只能包含字母、数字、点、下划线或连字符，并且必须以字母或数字开头和结尾")
	}
	return nil
}

func writeProfile(dir string, profile Profile) error {
	raw, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return fmt.Errorf("encode browser profile: %w", err)
	}
	raw = append(raw, '\n')
	if err := writePrivateFile(filepath.Join(dir, profileFileName), raw); err != nil {
		return fmt.Errorf("write browser profile: %w", err)
	}
	return nil
}

func writePrivateFile(name string, content []byte) error {
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, content, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, name); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Chmod(name, 0o600)
}

func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.Chmod(dir, 0o700)
}

func uniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != "" && !contains(result, value) {
			result = append(result, value)
		}
	}
	return result
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if strings.EqualFold(value, wanted) {
			return true
		}
	}
	return false
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
