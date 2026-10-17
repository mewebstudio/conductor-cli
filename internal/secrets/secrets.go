// Package secrets stores API keys in the operating system's credential store:
// macOS Keychain, Windows Credential Manager or the Linux Secret Service.
package secrets

import (
	"errors"

	"github.com/zalando/go-keyring"
)

const service = "conductor-cli"

// ErrNotFound means no key is stored for the provider.
var ErrNotFound = errors.New("no key stored")

// Get returns the stored key for a provider.
func Get(provider string) (string, error) {
	v, err := keyring.Get(service, provider)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

// Set stores the key for a provider.
func Set(provider, key string) error { return keyring.Set(service, provider, key) }

// Delete removes the key for a provider; a missing key is not an error.
func Delete(provider string) error {
	err := keyring.Delete(service, provider)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// Has reports whether a key is stored.
func Has(provider string) bool {
	_, err := Get(provider)
	return err == nil
}
