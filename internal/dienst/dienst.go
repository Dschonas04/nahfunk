// Package dienst wires everything together: one listener on the network for the other
// device, and one on loopback for the user interface.
package dienst

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/Dschonas04/quicksend/internal/einstellungen"
	"github.com/Dschonas04/quicksend/internal/empfang"
	"github.com/Dschonas04/quicksend/internal/kennung"
	"github.com/Dschonas04/quicksend/internal/suche"
	"github.com/Dschonas04/quicksend/internal/tagebuch"
	"github.com/Dschonas04/quicksend/internal/versand"
)

//go:embed alle/*
var seiten embed.FS

type Dienst struct {
	Einst   *einstellungen.Einstellungen
	Kennung *kennung.Kennung
	Empfang *empfang.Empfang
	Versand *versand.Versand
	Liste   *suche.Liste

	einmal sync.Once
	brems  *bremse
}

// Bremse throttles wrong codes per address. It is built on first use, so a Dienst still
// works as a plain struct literal.
func (d *Dienst) Bremse() *bremse {
	d.einmal.Do(func() { d.brems = neueBremse() })
	return d.brems
}

// GegenMux serves the other device. Every call needs the receiving side's code.
func (d *Dienst) GegenMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/gegen/hallo", d.mitCode(func(w http.ResponseWriter, r *http.Request) {
		antworten(w, map[string]any{
			"name":    d.Einst.GeraeteName,
			"id":      d.Einst.GeraeteId,
			"version": Version,
		})
	}))
	mux.HandleFunc("/gegen/anmelden", d.mitCode(func(w http.ResponseWriter, r *http.Request) {
		var a empfang.Anmeldung
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&a); err != nil {
			fehler(w, http.StatusBadRequest, err)
			return
		}
		antwort, err := d.Empfang.Anmelden(a)
		if err != nil {
			fehler(w, http.StatusBadRequest, err)
			return
		}
		antworten(w, antwort)
	}))
	mux.HandleFunc("/gegen/daten", d.mitCode(func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		versatz, _ := strconv.ParseInt(r.URL.Query().Get("versatz"), 10, 64)
		auf, err := d.Empfang.Schreiben(id, versatz, r.Body)
		if err != nil {
			// The offset travels along, so the sender knows where to pick up.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]any{"versatz": auf, "fehler": err.Error()})
			return
		}
		antworten(w, map[string]any{"versatz": auf})
	}))
	mux.HandleFunc("/gegen/abschluss", d.mitCode(func(w http.ResponseWriter, r *http.Request) {
		e, err := d.Empfang.Abschluss(r.URL.Query().Get("id"))
		if err != nil {
			fehler(w, http.StatusConflict, err)
			return
		}
		antworten(w, map[string]any{"ziel": e.Ziel, "name": e.Name})
	}))
	mux.HandleFunc("/gegen/stand", d.mitCode(func(w http.ResponseWriter, r *http.Request) {
		e, err := d.Empfang.Stand(r.URL.Query().Get("id"))
		if err != nil {
			fehler(w, http.StatusNotFound, err)
			return
		}
		antworten(w, e)
	}))
	return mux
}

// UiMux serves the local user interface.
func (d *Dienst) UiMux() *http.ServeMux {
	mux := http.NewServeMux()
	inhalt, err := fs.Sub(seiten, "alle")
	if err == nil {
		mux.Handle("/", http.FileServer(http.FS(inhalt)))
	}
	mux.HandleFunc("/api/zustand", func(w http.ResponseWriter, r *http.Request) {
		antworten(w, map[string]any{
			"eigen": map[string]any{
				"name":       d.Einst.GeraeteName,
				"id":         d.Einst.GeraeteId,
				"code":       d.Einst.Freigabecode,
				"port":       d.Einst.Port,
				"zielordner": d.Einst.Zielordner,
				"auto":       d.Einst.AutoAnnehmen,
				"version":    Version,
			},
			"geraete": d.Liste.Geraete(),
			"empfang": d.Empfang.Verlauf(),
			"versand": d.Versand.Liste(),
			"bekannt": d.Einst.Bekannt,
		})
	})
	mux.HandleFunc("/api/senden", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			fehler(w, http.StatusMethodNotAllowed, errors.New("nur POST"))
			return
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			fehler(w, http.StatusBadRequest, err)
			return
		}
		zielId := r.FormValue("geraet")
		code := r.FormValue("code")
		g, da := d.Liste.Finden(zielId)
		if !da {
			fehler(w, http.StatusNotFound, errors.New("Gerät nicht gefunden"))
			return
		}
		if code != "" {
			if err := d.Einst.CodeMerken(g.Id, code); err != nil {
				fehler(w, http.StatusInternalServerError, err)
				return
			}
		}
		dateien := r.MultipartForm.File["dateien"]
		if len(dateien) == 0 {
			fehler(w, http.StatusBadRequest, errors.New("keine Datei dabei"))
			return
		}
		var eingereiht []tagebuch.Eintrag
		for _, kopf := range dateien {
			offen, err := kopf.Open()
			if err != nil {
				fehler(w, http.StatusBadRequest, err)
				return
			}
			e, fehlerBeim := d.Versand.Einreihen(versand.Ziel{
				Id: g.Id, Name: g.Name, Adresse: g.Adresse, Finger: g.Finger,
			}, offen, kopf.Filename)
			offen.Close()
			if fehlerBeim != nil {
				fehler(w, http.StatusInternalServerError, fehlerBeim)
				return
			}
			eingereiht = append(eingereiht, e)
		}
		antworten(w, map[string]any{"eingereiht": eingereiht})
	})
	mux.HandleFunc("/api/freigeben", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		ja := r.URL.Query().Get("ja") != "nein"
		if err := d.Empfang.Freigeben(id, ja); err != nil {
			fehler(w, http.StatusBadRequest, err)
			return
		}
		antworten(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/erneut", func(w http.ResponseWriter, r *http.Request) {
		if err := d.Versand.Erneut(r.URL.Query().Get("id")); err != nil {
			fehler(w, http.StatusBadRequest, err)
			return
		}
		antworten(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/vergessen", func(w http.ResponseWriter, r *http.Request) {
		id := r.URL.Query().Get("id")
		if r.URL.Query().Get("seite") == "empfang" {
			if err := d.Empfang.Vergessen(id); err != nil {
				fehler(w, http.StatusBadRequest, err)
				return
			}
		} else if err := d.Versand.Vergessen(id); err != nil {
			fehler(w, http.StatusBadRequest, err)
			return
		}
		antworten(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/einstellungen", func(w http.ResponseWriter, r *http.Request) {
		var neu struct {
			Name       *string `json:"name"`
			Zielordner *string `json:"zielordner"`
			Auto       *bool   `json:"auto"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&neu); err != nil {
			fehler(w, http.StatusBadRequest, err)
			return
		}
		if neu.Name != nil && *neu.Name != "" {
			d.Einst.GeraeteName = *neu.Name
		}
		if neu.Zielordner != nil && *neu.Zielordner != "" {
			d.Einst.Zielordner = *neu.Zielordner
		}
		if neu.Auto != nil {
			d.Einst.AutoAnnehmen = *neu.Auto
			d.Empfang.AutoAnnehmen(*neu.Auto)
		}
		if err := d.Einst.Speichern(); err != nil {
			fehler(w, http.StatusInternalServerError, err)
			return
		}
		antworten(w, map[string]any{"ok": true})
	})
	mux.HandleFunc("/api/suchen", func(w http.ResponseWriter, r *http.Request) {
		zeit, abbrechen := context.WithTimeout(r.Context(), 4*time.Second)
		defer abbrechen()
		_ = d.Liste.EinmalSuchen(zeit, 3*time.Second)
		antworten(w, map[string]any{"geraete": d.Liste.Geraete()})
	})
	return mux
}

// mitCode rejects anything that does not carry the receiving side's code, and slows an
// address down that keeps guessing.
func (d *Dienst) mitCode(weiter http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		wer := absender(r)
		if frei, rest := d.Bremse().Darf(wer); !frei {
			w.Header().Set("Retry-After", strconv.Itoa(int(rest.Seconds())+1))
			fehler(w, http.StatusTooManyRequests,
				fmt.Errorf("zu viele falsche Codes, bitte %s warten", rest.Round(time.Second)))
			return
		}
		gegeben := r.Header.Get("X-Quicksend-Code")
		soll := d.Einst.Freigabecode
		if subtle.ConstantTimeCompare([]byte(gegeben), []byte(soll)) != 1 {
			d.Bremse().Fehlschlag(wer)
			fehler(w, http.StatusForbidden, errors.New("falscher Freigabecode"))
			return
		}
		d.Bremse().Erfolg(wer)
		weiter(w, r)
	}
}

// Starten brings both listeners up and returns once the context ends.
func (d *Dienst) Starten(ctx context.Context) error {
	gegen := &http.Server{
		Handler: mitKopfzeilen(d.GegenMux(), false),
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{d.Kennung.Zertifikat},
			MinVersion:   tls.VersionTLS12,
		},
		ReadHeaderTimeout: 20 * time.Second,
	}
	lauscher, err := net.Listen("tcp", fmt.Sprintf(":%d", d.Einst.Port))
	if err != nil {
		return fmt.Errorf("Port %d ist belegt: %w", d.Einst.Port, err)
	}
	ui := &http.Server{
		Handler:           NurOertlich(GleicherUrsprung(mitKopfzeilen(d.UiMux(), true))),
		ReadHeaderTimeout: 20 * time.Second,
	}
	uiLauscher, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", d.Einst.UiPort()))
	if err != nil {
		lauscher.Close()
		return fmt.Errorf("Port %d ist belegt: %w", d.Einst.UiPort(), err)
	}

	fehlerKanal := make(chan error, 2)
	go func() { fehlerKanal <- gegen.ServeTLS(lauscher, "", "") }()
	go func() { fehlerKanal <- ui.Serve(uiLauscher) }()

	select {
	case <-ctx.Done():
		aus, abbrechen := context.WithTimeout(context.Background(), 5*time.Second)
		defer abbrechen()
		_ = gegen.Shutdown(aus)
		_ = ui.Shutdown(aus)
		return nil
	case err := <-fehlerKanal:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func antworten(w http.ResponseWriter, wert any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(wert)
}

func fehler(w http.ResponseWriter, kode int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(kode)
	_ = json.NewEncoder(w).Encode(map[string]string{"fehler": err.Error()})
}
