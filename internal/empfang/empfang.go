// Package empfang is the receiving half. It writes incoming data into a partial file next
// to a journal entry, so an interrupted transfer can be picked up at the exact byte it
// stopped at — after a dropped connection, a killed process or a power cut.
package empfang

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Dschonas04/quicksend/internal/tagebuch"
)

// How much data is written before the file and its journal entry are flushed to disk.
// Smaller means less to redo after a crash, larger means fewer disk syncs.
const SyncAlle = 8 << 20

// ErrVersatz is returned when the sender starts at a different byte than the one that is
// actually on disk. The current offset travels with the error, so the sender can adjust.
type ErrVersatz struct {
	Erwartet int64
}

func (e ErrVersatz) Error() string {
	return fmt.Sprintf("Versatz passt nicht, erwartet %d", e.Erwartet)
}

type Anmeldung struct {
	Name       string `json:"name"`
	Groesse    int64  `json:"groesse"`
	Sha256     string `json:"sha256"`
	Gegenseite string `json:"gegenseite"`
}

type Antwort struct {
	Id      string `json:"id"`
	Versatz int64  `json:"versatz"`
	Zustand string `json:"zustand"`
}

type Empfang struct {
	zielordner string
	arbeit     string // hidden folder holding partial data and journal entries
	autoJa     bool

	sperre sync.Mutex
	offen  map[string]*vorgang
}

type vorgang struct {
	eintrag tagebuch.Eintrag
	frei    bool
}

// Neu prepares the folders and picks up whatever an earlier run left behind.
func Neu(zielordner string, autoAnnehmen bool) (*Empfang, error) {
	arbeit := filepath.Join(zielordner, ".quicksend")
	if err := os.MkdirAll(arbeit, 0o700); err != nil {
		return nil, err
	}
	e := &Empfang{zielordner: zielordner, arbeit: arbeit, autoJa: autoAnnehmen, offen: map[string]*vorgang{}}
	return e, e.aufraeumen()
}

// AutoAnnehmen switches automatic acceptance on or off at runtime.
func (x *Empfang) AutoAnnehmen(an bool) {
	x.sperre.Lock()
	x.autoJa = an
	x.sperre.Unlock()
}

// aufraeumen trusts the partial file on disk, not the number in the journal: after a crash
// the file may hold fewer bytes than the entry claims.
func (x *Empfang) aufraeumen() error {
	eintraege, err := tagebuch.Alle(x.arbeit)
	if err != nil {
		return err
	}
	for _, e := range eintraege {
		if e.Zustand == tagebuch.Fertig {
			continue
		}
		info, fehler := os.Stat(x.teilPfad(e.Id))
		if fehler != nil {
			// Data gone, entry worthless.
			_ = tagebuch.Loeschen(x.arbeit, e.Id)
			continue
		}
		e.Uebertragen = info.Size()
		if e.Zustand == tagebuch.Laeuft {
			e.Zustand = tagebuch.Wartet
			e.Meldung = "nach Neustart fortsetzbar"
		}
		if err := tagebuch.Schreiben(x.arbeit, e); err != nil {
			return err
		}
		x.offen[e.Id] = &vorgang{eintrag: e, frei: e.Zustand != tagebuch.Freigabe || x.autoJa}
	}
	return nil
}

func (x *Empfang) teilPfad(id string) string {
	return filepath.Join(x.arbeit, id+".teil")
}

// Anmelden registers a file. An earlier attempt at the same file is continued instead of
// started over, which is what makes a resume possible at all.
func (x *Empfang) Anmelden(a Anmeldung) (Antwort, error) {
	if a.Name == "" || a.Groesse < 0 || len(a.Sha256) != 64 {
		return Antwort{}, errors.New("Anmeldung unvollständig")
	}
	x.sperre.Lock()
	defer x.sperre.Unlock()

	for id, v := range x.offen {
		if v.eintrag.Sha256 == a.Sha256 && v.eintrag.Name == a.Name && v.eintrag.Zustand != tagebuch.Fertig {
			if info, err := os.Stat(x.teilPfad(id)); err == nil {
				v.eintrag.Uebertragen = info.Size()
			}
			return Antwort{Id: id, Versatz: v.eintrag.Uebertragen, Zustand: v.eintrag.Zustand}, nil
		}
	}

	id := neueId(a.Sha256, a.Name)
	e := tagebuch.Eintrag{
		Id:         id,
		Name:       filepath.Base(a.Name),
		Groesse:    a.Groesse,
		Sha256:     strings.ToLower(a.Sha256),
		Gegenseite: a.Gegenseite,
		Begonnen:   time.Now(),
		Zustand:    tagebuch.Freigabe,
	}
	if x.autoJa {
		e.Zustand = tagebuch.Laeuft
	}
	datei, err := os.OpenFile(x.teilPfad(id), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return Antwort{}, err
	}
	_ = datei.Close()
	if err := tagebuch.Schreiben(x.arbeit, e); err != nil {
		return Antwort{}, err
	}
	x.offen[id] = &vorgang{eintrag: e, frei: x.autoJa}
	return Antwort{Id: id, Versatz: 0, Zustand: e.Zustand}, nil
}

// Freigeben accepts or rejects a waiting transfer.
func (x *Empfang) Freigeben(id string, ja bool) error {
	x.sperre.Lock()
	v, da := x.offen[id]
	if !da {
		x.sperre.Unlock()
		return errors.New("unbekannter Vorgang")
	}
	if ja {
		v.frei = true
		v.eintrag.Zustand = tagebuch.Laeuft
		v.eintrag.Meldung = ""
	} else {
		v.eintrag.Zustand = tagebuch.Fehler
		v.eintrag.Meldung = "abgelehnt"
		delete(x.offen, id)
		_ = os.Remove(x.teilPfad(id))
	}
	e := v.eintrag
	x.sperre.Unlock()
	return tagebuch.Schreiben(x.arbeit, e)
}

// Schreiben appends the body at the given offset and reports how far the file now reaches.
func (x *Empfang) Schreiben(id string, versatz int64, koerper io.Reader) (int64, error) {
	x.sperre.Lock()
	v, da := x.offen[id]
	if !da {
		x.sperre.Unlock()
		return 0, errors.New("unbekannter Vorgang")
	}
	if !v.frei {
		x.sperre.Unlock()
		return 0, errors.New("noch nicht freigegeben")
	}
	x.sperre.Unlock()

	datei, err := os.OpenFile(x.teilPfad(id), os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer datei.Close()

	info, err := datei.Stat()
	if err != nil {
		return 0, err
	}
	auf := info.Size()
	switch {
	case versatz > auf:
		return auf, ErrVersatz{Erwartet: auf}
	case versatz < auf:
		// The sender wants to resend a piece; cut the file back so nothing is duplicated.
		if err := datei.Truncate(versatz); err != nil {
			return auf, err
		}
		auf = versatz
	}
	if _, err := datei.Seek(auf, io.SeekStart); err != nil {
		return auf, err
	}

	puffer := make([]byte, 256<<10)
	seitSync := int64(0)
	for {
		gelesen, leseFehler := koerper.Read(puffer)
		if gelesen > 0 {
			geschrieben, schreibFehler := datei.Write(puffer[:gelesen])
			auf += int64(geschrieben)
			seitSync += int64(geschrieben)
			if schreibFehler != nil {
				x.merken(id, auf, tagebuch.Laeuft, schreibFehler.Error())
				return auf, schreibFehler
			}
			if seitSync >= SyncAlle {
				if err := datei.Sync(); err != nil {
					return auf, err
				}
				x.merken(id, auf, tagebuch.Laeuft, "")
				seitSync = 0
			}
		}
		if leseFehler == io.EOF {
			break
		}
		if leseFehler != nil {
			_ = datei.Sync()
			x.merken(id, auf, tagebuch.Laeuft, leseFehler.Error())
			return auf, leseFehler
		}
	}
	if err := datei.Sync(); err != nil {
		return auf, err
	}
	x.merken(id, auf, tagebuch.Laeuft, "")
	return auf, nil
}

// Abschluss checks the hash before the file is moved into place. A file that does not
// match is kept as a partial, so the sender can fill in the missing or damaged part.
func (x *Empfang) Abschluss(id string) (tagebuch.Eintrag, error) {
	x.sperre.Lock()
	v, da := x.offen[id]
	if !da {
		x.sperre.Unlock()
		return tagebuch.Eintrag{}, errors.New("unbekannter Vorgang")
	}
	e := v.eintrag
	x.sperre.Unlock()

	summe, err := dateiSumme(x.teilPfad(id))
	if err != nil {
		return e, err
	}
	if summe != e.Sha256 {
		x.merken(id, e.Uebertragen, tagebuch.Laeuft, "Prüfsumme passt nicht, bitte erneut senden")
		return e, fmt.Errorf("Prüfsumme passt nicht: %s statt %s", summe, e.Sha256)
	}
	ziel, err := freierName(filepath.Join(x.zielordner, e.Name))
	if err != nil {
		return e, err
	}
	if err := os.Rename(x.teilPfad(id), ziel); err != nil {
		return e, err
	}
	if err := ordnerSync(x.zielordner); err != nil {
		return e, err
	}

	x.sperre.Lock()
	v.eintrag.Zustand = tagebuch.Fertig
	v.eintrag.Uebertragen = v.eintrag.Groesse
	v.eintrag.Ziel = ziel
	v.eintrag.Meldung = ""
	e = v.eintrag
	delete(x.offen, id)
	x.sperre.Unlock()
	return e, tagebuch.Schreiben(x.arbeit, e)
}

// Stand reports where a transfer currently stands.
func (x *Empfang) Stand(id string) (tagebuch.Eintrag, error) {
	x.sperre.Lock()
	if v, da := x.offen[id]; da {
		e := v.eintrag
		x.sperre.Unlock()
		return e, nil
	}
	x.sperre.Unlock()
	return tagebuch.Lesen(x.arbeit, id)
}

// Verlauf lists everything the receiving side knows about, newest first.
func (x *Empfang) Verlauf() []tagebuch.Eintrag {
	eintraege, err := tagebuch.Alle(x.arbeit)
	if err != nil {
		return nil
	}
	return eintraege
}

// Vergessen drops a finished or failed entry from the list.
func (x *Empfang) Vergessen(id string) error {
	x.sperre.Lock()
	delete(x.offen, id)
	x.sperre.Unlock()
	_ = os.Remove(x.teilPfad(id))
	return tagebuch.Loeschen(x.arbeit, id)
}

func (x *Empfang) merken(id string, auf int64, zustand, meldung string) {
	x.sperre.Lock()
	v, da := x.offen[id]
	if !da {
		x.sperre.Unlock()
		return
	}
	v.eintrag.Uebertragen = auf
	v.eintrag.Zustand = zustand
	v.eintrag.Meldung = meldung
	e := v.eintrag
	x.sperre.Unlock()
	_ = tagebuch.Schreiben(x.arbeit, e)
}

func neueId(sha, name string) string {
	summe := sha256.Sum256([]byte(sha + "|" + name + "|" + time.Now().Format(time.RFC3339Nano)))
	return hex.EncodeToString(summe[:8])
}

func dateiSumme(pfad string) (string, error) {
	datei, err := os.Open(pfad)
	if err != nil {
		return "", err
	}
	defer datei.Close()
	huelle := sha256.New()
	if _, err := io.Copy(huelle, datei); err != nil {
		return "", err
	}
	return hex.EncodeToString(huelle.Sum(nil)), nil
}

// freierName never overwrites: "bild.png" becomes "bild (2).png" if it is taken.
func freierName(pfad string) (string, error) {
	if _, err := os.Stat(pfad); os.IsNotExist(err) {
		return pfad, nil
	}
	endung := filepath.Ext(pfad)
	stamm := strings.TrimSuffix(pfad, endung)
	for n := 2; n < 10000; n++ {
		kandidat := fmt.Sprintf("%s (%d)%s", stamm, n, endung)
		if _, err := os.Stat(kandidat); os.IsNotExist(err) {
			return kandidat, nil
		}
	}
	return "", errors.New("kein freier Name gefunden")
}

func ordnerSync(pfad string) error {
	ordner, err := os.Open(pfad)
	if err != nil {
		return err
	}
	defer ordner.Close()
	if err := ordner.Sync(); err != nil && !errors.Is(err, os.ErrInvalid) {
		return err
	}
	return nil
}
