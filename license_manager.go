package main

import (
	"crypto/ed25519"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"huiju-bridge/internal/licensing"
)

var licensePublicKey string
var appVersion = "dev"

type LicenseManager struct {
	path        string
	publicKey   ed25519.PublicKey
	machineCode string
	mu          sync.RWMutex
}

func NewLicenseManager(appDir, encodedPublicKey string) (*LicenseManager, error) {
	publicKey, err := licensing.DecodePublicKey(encodedPublicKey)
	if err != nil {
		return nil, err
	}
	machineCode, err := licensing.MachineCode()
	if err != nil {
		return nil, err
	}
	return &LicenseManager{path: filepath.Join(appDir, "license.dat"), publicKey: publicKey, machineCode: machineCode}, nil
}

func (m *LicenseManager) MachineCode() string {
	return m.machineCode
}

func (m *LicenseManager) read() (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	data, err := os.ReadFile(m.path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("尚未激活")
		}
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func (m *LicenseManager) Current() (licensing.Claims, error) {
	code, err := m.read()
	if err != nil {
		return licensing.Claims{}, err
	}
	return licensing.Verify(code, m.publicKey, m.machineCode, time.Now())
}

func (m *LicenseManager) Check() error {
	_, err := m.Current()
	return err
}

func (m *LicenseManager) Activate(code string) (licensing.Claims, error) {
	claims, err := licensing.Verify(code, m.publicKey, m.machineCode, time.Now())
	if err != nil {
		return claims, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	tmp := m.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.TrimSpace(code)), 0600); err != nil {
		return claims, err
	}
	if err := os.Rename(tmp, m.path); err != nil {
		return claims, err
	}
	return claims, nil
}
