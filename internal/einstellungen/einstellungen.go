// Package einstellungen holds what the app remembers between starts: the device name,
// the port, where received files land, and the codes of devices already paired with.
package einstellungen

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

// StandardPort is the port the peer endpoint listens on. The user interface uses the
// next one up, bound to loopback only.
const StandardPort = 51765

type Einstellungen struct {
	GeraeteId    string            `json:"geraete_id"`
	GeraeteName  string            `json:"geraete_name"`
	Port         int               `json:"port"`
	Zielordner   string            `json:"zielordner"`
	AutoAnnehmen bool              `json:"auto_annehmen"`
	Freigabecode string            `json:"freigabecode"`
	Bekannt      map[string]string `json:"bekannt"` // device id -> its code

	pfad   string
	sperre sync.Mutex
}

// Ordner is where the config file, the device certificate and the send queue live.
func Ordner() (string, error) {
	basis, err := os.UserConfigDir()
	if err != nil {
		heim, fehler := os.UserHomeDir()
		if fehler != nil {
			return "", fehler
		}
		basis = filepath.Join(heim, ".config")
	}
	ordner := filepath.Join(basis, "nahfunk")
	if err := os.MkdirAll(ordner, 0o700); err != nil {
		return "", err
	}
	return ordner, nil
}

// Laden reads the config file, creating it with sensible defaults on first start.
func Laden() (*Einstellungen, error) {
	ordner, err := Ordner()
	if err != nil {
		return nil, err
	}
	pfad := filepath.Join(ordner, "einstellungen.json")
	e := &Einstellungen{pfad: pfad, Bekannt: map[string]string{}}
	rohdaten, err := os.ReadFile(pfad)
	switch {
	case err == nil:
		if fehler := json.Unmarshal(rohdaten, e); fehler != nil {
			return nil, fmt.Errorf("%s ist beschädigt: %w", pfad, fehler)
		}
	case !os.IsNotExist(err):
		return nil, err
	}
	e.pfad = pfad
	if e.Bekannt == nil {
		e.Bekannt = map[string]string{}
	}
	if e.GeraeteId == "" {
		e.GeraeteId = zufallsId()
	}
	if e.GeraeteName == "" {
		e.GeraeteName = standardName()
	}
	if e.Port == 0 {
		e.Port = StandardPort
	}
	if e.Freigabecode == "" {
		e.Freigabecode = neuerCode()
	}
	if e.Zielordner == "" {
		e.Zielordner = standardZiel()
	}
	if err := os.MkdirAll(e.Zielordner, 0o755); err != nil {
		return nil, err
	}
	return e, e.Speichern()
}

// Speichern writes the file through a temporary copy, so a crash mid-write cannot
// leave a half-written config behind.
func (e *Einstellungen) Speichern() error {
	e.sperre.Lock()
	defer e.sperre.Unlock()
	rohdaten, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	vorlaeufig := e.pfad + ".neu"
	if err := os.WriteFile(vorlaeufig, rohdaten, 0o600); err != nil {
		return err
	}
	return os.Rename(vorlaeufig, e.pfad)
}

// CodeFuer returns the stored code of a device, if it was paired with before.
func (e *Einstellungen) CodeFuer(id string) string {
	e.sperre.Lock()
	defer e.sperre.Unlock()
	return e.Bekannt[id]
}

// CodeMerken stores the code of a device so it only has to be typed once.
func (e *Einstellungen) CodeMerken(id, code string) error {
	e.sperre.Lock()
	if e.Bekannt == nil {
		e.Bekannt = map[string]string{}
	}
	e.Bekannt[id] = code
	e.sperre.Unlock()
	return e.Speichern()
}

// UiPort is the loopback port of the user interface.
func (e *Einstellungen) UiPort() int {
	return e.Port + 1
}

func zufallsId() string {
	rohdaten := make([]byte, 8)
	if _, err := rand.Read(rohdaten); err != nil {
		return "unbekannt"
	}
	return hex.EncodeToString(rohdaten)
}

func neuerCode() string {
	zahl, err := rand.Int(rand.Reader, big.NewInt(1000000))
	if err != nil {
		return "000000"
	}
	return fmt.Sprintf("%06d", zahl.Int64())
}

func standardName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "Nahfunk-" + runtime.GOOS
	}
	return name
}

func standardZiel() string {
	heim, err := os.UserHomeDir()
	if err != nil {
		return "nahfunk-empfang"
	}
	for _, kandidat := range []string{"Downloads", "Download"} {
		pfad := filepath.Join(heim, kandidat)
		if info, fehler := os.Stat(pfad); fehler == nil && info.IsDir() {
			return filepath.Join(pfad, "Nahfunk")
		}
	}
	return filepath.Join(heim, "Nahfunk")
}
