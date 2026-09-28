// Package tagebuch keeps one small file per transfer next to the partial data. It is what
// makes a transfer survive a crash: after a restart the app reads these entries and knows
// how many bytes of which file already arrived.
package tagebuch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The states a transfer moves through.
const (
	Wartet   = "wartet"   // queued, or resumable after an interruption
	Freigabe = "freigabe" // the receiving side has not accepted it yet
	Laeuft   = "laeuft"
	Fertig   = "fertig"
	Fehler   = "fehler"
)

type Eintrag struct {
	Id          string    `json:"id"`
	Name        string    `json:"name"`
	Groesse     int64     `json:"groesse"`
	Sha256      string    `json:"sha256"`
	Uebertragen int64     `json:"uebertragen"`
	Gegenseite  string    `json:"gegenseite"`
	Begonnen    time.Time `json:"begonnen"`
	Geaendert   time.Time `json:"geaendert"`
	Zustand     string    `json:"zustand"`
	Meldung     string    `json:"meldung,omitempty"`
	Versuche    int       `json:"versuche,omitempty"`
	Ziel        string    `json:"ziel,omitempty"`   // where the finished file ended up
	Quelle      string    `json:"quelle,omitempty"` // staging file on the sending side
	Adresse     string    `json:"adresse,omitempty"`
	Finger      string    `json:"finger,omitempty"`
}

// Anteil is how far along the transfer is, between 0 and 1.
func (e Eintrag) Anteil() float64 {
	if e.Groesse <= 0 {
		return 0
	}
	return float64(e.Uebertragen) / float64(e.Groesse)
}

// Schreiben stores an entry as <ordner>/<id>.json, written to a temporary file and
// renamed into place, so a crash never leaves a truncated entry behind.
func Schreiben(ordner string, e Eintrag) error {
	if e.Id == "" {
		return errors.New("Eintrag ohne Id")
	}
	if err := os.MkdirAll(ordner, 0o700); err != nil {
		return err
	}
	e.Geaendert = time.Now()
	rohdaten, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return err
	}
	pfad := filepath.Join(ordner, e.Id+".json")
	vorlaeufig := pfad + ".neu"
	datei, err := os.OpenFile(vorlaeufig, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := datei.Write(rohdaten); err != nil {
		datei.Close()
		return err
	}
	if err := datei.Sync(); err != nil {
		datei.Close()
		return err
	}
	if err := datei.Close(); err != nil {
		return err
	}
	return os.Rename(vorlaeufig, pfad)
}

// Lesen returns a single entry.
func Lesen(ordner, id string) (Eintrag, error) {
	var e Eintrag
	rohdaten, err := os.ReadFile(filepath.Join(ordner, id+".json"))
	if err != nil {
		return e, err
	}
	return e, json.Unmarshal(rohdaten, &e)
}

// Alle returns every entry in the folder, newest first.
func Alle(ordner string) ([]Eintrag, error) {
	dateien, err := os.ReadDir(ordner)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var raus []Eintrag
	for _, d := range dateien {
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".json") {
			continue
		}
		e, fehler := Lesen(ordner, strings.TrimSuffix(d.Name(), ".json"))
		if fehler != nil {
			continue // a single unreadable entry must not hide the rest
		}
		raus = append(raus, e)
	}
	sort.Slice(raus, func(i, j int) bool { return raus[i].Geaendert.After(raus[j].Geaendert) })
	return raus, nil
}

// Loeschen removes an entry.
func Loeschen(ordner, id string) error {
	err := os.Remove(filepath.Join(ordner, id+".json"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
