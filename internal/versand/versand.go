// Package versand is the sending half. A file to send is first copied into a staging
// folder and written into the journal, so the queue survives a crash of the app or of the
// machine. The worker then pushes each entry across, asks the receiver where to continue
// after every hiccup, and backs off between attempts instead of hammering a dead peer.
package versand

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Dschonas04/quicksend/internal/kennung"
	"github.com/Dschonas04/quicksend/internal/tagebuch"
)

// How often an entry is retried before it is parked as failed. The user can start it
// again by hand, and nothing is lost in the meantime.
const MaxVersuche = 40

// Ziel is the device a file goes to.
type Ziel struct {
	Id      string `json:"id"`
	Name    string `json:"name"`
	Adresse string `json:"adresse"` // host:port
	Finger  string `json:"finger"`  // expected certificate fingerprint
	Code    string `json:"code"`
}

type Versand struct {
	ordner    string // staging files and journal entries
	eigenName string
	codeFuer  func(geraeteId string) string

	sperre    sync.Mutex
	auftraege map[string]*tagebuch.Eintrag
	wecken    chan struct{}
}

// Neu picks up a queue an earlier run left behind.
func Neu(ordner, eigenName string, codeFuer func(string) string) (*Versand, error) {
	if err := os.MkdirAll(ordner, 0o700); err != nil {
		return nil, err
	}
	v := &Versand{
		ordner:    ordner,
		eigenName: eigenName,
		codeFuer:  codeFuer,
		auftraege: map[string]*tagebuch.Eintrag{},
		wecken:    make(chan struct{}, 1),
	}
	eintraege, err := tagebuch.Alle(ordner)
	if err != nil {
		return nil, err
	}
	for i := range eintraege {
		e := eintraege[i]
		if e.Zustand == tagebuch.Fertig {
			continue
		}
		if _, fehler := os.Stat(e.Quelle); fehler != nil {
			e.Zustand = tagebuch.Fehler
			e.Meldung = "Zwischendatei fehlt nach Neustart"
			_ = tagebuch.Schreiben(ordner, e)
			continue
		}
		if e.Zustand == tagebuch.Laeuft {
			e.Meldung = "nach Neustart fortgesetzt"
		}
		v.auftraege[e.Id] = &e
	}
	return v, nil
}

// Einreihen copies the data into the staging folder, notes its hash and queues it.
func (v *Versand) Einreihen(z Ziel, quelle io.Reader, name string) (tagebuch.Eintrag, error) {
	if z.Adresse == "" {
		return tagebuch.Eintrag{}, errors.New("Ziel ohne Adresse")
	}
	id := hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))[:16]
	zwischen := filepath.Join(v.ordner, id+".daten")
	datei, err := os.OpenFile(zwischen, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return tagebuch.Eintrag{}, err
	}
	huelle := sha256.New()
	groesse, err := io.Copy(io.MultiWriter(datei, huelle), quelle)
	if err != nil {
		datei.Close()
		os.Remove(zwischen)
		return tagebuch.Eintrag{}, err
	}
	if err := datei.Sync(); err != nil {
		datei.Close()
		return tagebuch.Eintrag{}, err
	}
	if err := datei.Close(); err != nil {
		return tagebuch.Eintrag{}, err
	}

	e := tagebuch.Eintrag{
		Id:         id,
		Name:       filepath.Base(name),
		Groesse:    groesse,
		Sha256:     hex.EncodeToString(huelle.Sum(nil)),
		Gegenseite: z.Name,
		Begonnen:   time.Now(),
		Zustand:    tagebuch.Wartet,
		Quelle:     zwischen,
		Adresse:    z.Adresse,
		Finger:     z.Finger,
		Ziel:       z.Id,
	}
	if err := tagebuch.Schreiben(v.ordner, e); err != nil {
		return tagebuch.Eintrag{}, err
	}
	// The queue gets its own copy: the worker changes the entry while it runs, and
	// the value handed back to the caller must not move under them.
	lauf := e
	v.sperre.Lock()
	v.auftraege[id] = &lauf
	v.sperre.Unlock()
	v.anstossen()
	return e, nil
}

// Schleife runs until the context ends, working through the queue.
func (v *Versand) Schleife(ctx context.Context) {
	takt := time.NewTicker(2 * time.Second)
	defer takt.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-takt.C:
		case <-v.wecken:
		}
		for _, id := range v.faellig() {
			select {
			case <-ctx.Done():
				return
			default:
			}
			v.versuchen(ctx, id)
		}
	}
}

// faellig collects the entries whose backoff has elapsed.
func (v *Versand) faellig() []string {
	v.sperre.Lock()
	defer v.sperre.Unlock()
	var raus []string
	for id, e := range v.auftraege {
		if e.Zustand == tagebuch.Fertig || e.Zustand == tagebuch.Fehler {
			continue
		}
		if time.Since(e.Geaendert) < wartezeit(e.Versuche) {
			continue
		}
		raus = append(raus, id)
	}
	return raus
}

// wartezeit doubles with every failed attempt, up to a minute.
func wartezeit(versuche int) time.Duration {
	if versuche <= 0 {
		return 0
	}
	sekunden := math.Pow(2, math.Min(float64(versuche), 6))
	return time.Duration(math.Min(sekunden, 60)) * time.Second
}

func (v *Versand) versuchen(ctx context.Context, id string) {
	v.sperre.Lock()
	e, da := v.auftraege[id]
	if !da {
		v.sperre.Unlock()
		return
	}
	kopie := *e
	v.sperre.Unlock()

	fehler := v.uebertragen(ctx, &kopie)
	v.sperre.Lock()
	lauf, da := v.auftraege[id]
	if !da {
		v.sperre.Unlock()
		return
	}
	lauf.Uebertragen = kopie.Uebertragen
	lauf.Zustand = kopie.Zustand
	lauf.Meldung = kopie.Meldung
	if fehler != nil {
		lauf.Versuche++
		lauf.Meldung = fehler.Error()
		if lauf.Versuche >= MaxVersuche {
			lauf.Zustand = tagebuch.Fehler
		}
	} else if lauf.Zustand == tagebuch.Fertig {
		lauf.Versuche = 0
		_ = os.Remove(lauf.Quelle)
	}
	fertig := *lauf
	v.sperre.Unlock()
	_ = tagebuch.Schreiben(v.ordner, fertig)
}

// uebertragen does one full attempt: ask where to continue, push the rest, finish.
func (v *Versand) uebertragen(ctx context.Context, e *tagebuch.Eintrag) error {
	kunde := v.kunde(e.Finger)
	code := ""
	if v.codeFuer != nil {
		code = v.codeFuer(e.Ziel)
	}

	anmeldung, err := json.Marshal(map[string]any{
		"name":       e.Name,
		"groesse":    e.Groesse,
		"sha256":     e.Sha256,
		"gegenseite": v.eigenName,
	})
	if err != nil {
		return err
	}
	var antwort struct {
		Id      string `json:"id"`
		Versatz int64  `json:"versatz"`
		Zustand string `json:"zustand"`
	}
	if err := v.ruf(ctx, kunde, http.MethodPost, e.Adresse, "/gegen/anmelden", code,
		bytes.NewReader(anmeldung), &antwort); err != nil {
		return err
	}
	if antwort.Zustand == tagebuch.Freigabe {
		e.Zustand = tagebuch.Freigabe
		e.Meldung = "warte auf Freigabe am Zielgerät"
		return nil
	}
	e.Zustand = tagebuch.Laeuft
	e.Uebertragen = antwort.Versatz

	if antwort.Versatz < e.Groesse {
		datei, fehler := os.Open(e.Quelle)
		if fehler != nil {
			return fehler
		}
		defer datei.Close()
		if _, fehler := datei.Seek(antwort.Versatz, io.SeekStart); fehler != nil {
			return fehler
		}
		var stand struct {
			Versatz int64 `json:"versatz"`
		}
		adresse := fmt.Sprintf("/gegen/daten?id=%s&versatz=%d", antwort.Id, antwort.Versatz)
		if fehler := v.ruf(ctx, kunde, http.MethodPut, e.Adresse, adresse, code, datei, &stand); fehler != nil {
			// Even a failed push usually moved data; remember how far it got.
			if stand.Versatz > e.Uebertragen {
				e.Uebertragen = stand.Versatz
			}
			return fehler
		}
		e.Uebertragen = stand.Versatz
	}

	var fertig struct {
		Ziel string `json:"ziel"`
	}
	if err := v.ruf(ctx, kunde, http.MethodPost, e.Adresse,
		"/gegen/abschluss?id="+antwort.Id, code, nil, &fertig); err != nil {
		return err
	}
	e.Zustand = tagebuch.Fertig
	e.Uebertragen = e.Groesse
	e.Meldung = ""
	return nil
}

func (v *Versand) ruf(ctx context.Context, kunde *http.Client, art, adresse, weg, code string,
	koerper io.Reader, ziel any) error {
	anfrage, err := http.NewRequestWithContext(ctx, art, "https://"+adresse+weg, koerper)
	if err != nil {
		return err
	}
	anfrage.Header.Set("X-Quicksend-Code", code)
	anfrage.Header.Set("Content-Type", "application/json")
	antwort, err := kunde.Do(anfrage)
	if err != nil {
		return err
	}
	defer antwort.Body.Close()
	rohdaten, err := io.ReadAll(io.LimitReader(antwort.Body, 1<<20))
	if err != nil {
		return err
	}
	if antwort.StatusCode/100 != 2 {
		if ziel != nil {
			_ = json.Unmarshal(rohdaten, ziel) // may carry the offset to continue at
		}
		return fmt.Errorf("%s antwortete %s: %s", weg, antwort.Status, kurz(rohdaten))
	}
	if ziel == nil {
		return nil
	}
	return json.Unmarshal(rohdaten, ziel)
}

// kunde builds a client that accepts exactly the certificate the device announced.
func (v *Versand) kunde(finger string) *http.Client {
	return &http.Client{
		Timeout: 0, // large files must not run into a deadline
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true, // self-signed on purpose, pinned below
				VerifyPeerCertificate: func(rohketten [][]byte, _ [][]*x509.Certificate) error {
					if len(rohketten) == 0 {
						return errors.New("Gegenseite hat kein Zertifikat gezeigt")
					}
					if finger == "" {
						return nil
					}
					if gesehen := kennung.Fingerabdruck(rohketten[0]); gesehen != finger {
						return fmt.Errorf("Fingerabdruck passt nicht: %s statt %s", gesehen, finger)
					}
					return nil
				},
			},
			ResponseHeaderTimeout: 30 * time.Second,
			IdleConnTimeout:       30 * time.Second,
		},
	}
}

// Liste returns the queue plus the finished entries, newest first.
func (v *Versand) Liste() []tagebuch.Eintrag {
	eintraege, err := tagebuch.Alle(v.ordner)
	if err != nil {
		return nil
	}
	return eintraege
}

// Erneut puts a parked entry back into the queue.
func (v *Versand) Erneut(id string) error {
	v.sperre.Lock()
	e, da := v.auftraege[id]
	if !da {
		gelesen, err := tagebuch.Lesen(v.ordner, id)
		if err != nil {
			v.sperre.Unlock()
			return err
		}
		e = &gelesen
		v.auftraege[id] = e
	}
	if _, err := os.Stat(e.Quelle); err != nil {
		v.sperre.Unlock()
		return errors.New("Zwischendatei ist weg, bitte neu auswählen")
	}
	e.Zustand = tagebuch.Wartet
	e.Versuche = 0
	e.Meldung = ""
	kopie := *e
	v.sperre.Unlock()
	if err := tagebuch.Schreiben(v.ordner, kopie); err != nil {
		return err
	}
	v.anstossen()
	return nil
}

// Vergessen removes an entry and its staging file.
func (v *Versand) Vergessen(id string) error {
	v.sperre.Lock()
	if e, da := v.auftraege[id]; da {
		_ = os.Remove(e.Quelle)
		delete(v.auftraege, id)
	} else if e, err := tagebuch.Lesen(v.ordner, id); err == nil {
		_ = os.Remove(e.Quelle)
	}
	v.sperre.Unlock()
	return tagebuch.Loeschen(v.ordner, id)
}

func (v *Versand) anstossen() {
	select {
	case v.wecken <- struct{}{}:
	default:
	}
}

func kurz(rohdaten []byte) string {
	if len(rohdaten) > 200 {
		return string(rohdaten[:200])
	}
	return string(rohdaten)
}
