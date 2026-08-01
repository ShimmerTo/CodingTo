package app

import (
	"fmt"

	"codingto/internal/browserworkflow"
)

// ListBrowserProfiles returns the global, non-secret profile metadata shared by
// every agent. An optional target URL limits the result to profiles whose
// allowed origins include that origin.
func (a *App) ListBrowserProfiles(targetURL string) ([]browserworkflow.Profile, error) {
	base, err := browserworkflow.ProfileBaseDir()
	if err != nil {
		return nil, err
	}
	return browserworkflow.List(base, targetURL)
}

// SaveBrowserProfile stores metadata in the global browser profile directory
// and, when requested, protects the credential with the operating system
// credential store. The response contains metadata only.
func (a *App) SaveBrowserProfile(req SaveBrowserProfileRequest) (browserworkflow.Profile, error) {
	base, err := browserworkflow.ProfileBaseDir()
	if err != nil {
		return browserworkflow.Profile{}, err
	}
	return browserworkflow.Save(base, req.SaveRequest)
}

// DeleteBrowserProfile removes one global persistent browser session, including
// its protected credential file.
func (a *App) DeleteBrowserProfile(profileID string) error {
	if profileID == "" {
		return fmt.Errorf("profile id is required")
	}
	base, err := browserworkflow.ProfileBaseDir()
	if err != nil {
		return err
	}
	return browserworkflow.Delete(base, profileID)
}

// RenameBrowserProfile updates the display name of one persistent browser
// session in the global profile store. The immutable key and stored credentials
// are untouched.
func (a *App) RenameBrowserProfile(profileID, newName string) (browserworkflow.Profile, error) {
	if profileID == "" {
		return browserworkflow.Profile{}, fmt.Errorf("profile id is required")
	}
	base, err := browserworkflow.ProfileBaseDir()
	if err != nil {
		return browserworkflow.Profile{}, err
	}
	return browserworkflow.Rename(base, profileID, newName)
}
