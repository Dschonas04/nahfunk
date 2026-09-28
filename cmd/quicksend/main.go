// Quicksend sends files to another device on the same network. Start it without arguments
// and it opens its page in the browser; the transfer itself runs in the background service,
// which picks interrupted transfers up again where they stopped.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/Dschonas04/quicksend/internal/dienst"
	"github.com/Dschonas04/quicksend/internal/einstellungen"
	"github.com/Dschonas04/quicksend/internal/empfang"
	"github.com/Dschonas04/quicksend/internal/kennung"
	"github.com/Dschonas04/quicksend/internal/suche"
	"github.com/Dschonas04/quicksend/internal/tagebuch"
	"github.com/Dschonas04/quicksend/internal/versand"
)

func main() {
	port := flag.Int("port", 0, "Port für die Gegenseite (Standard 51765)")
	name := flag.String("name", "", "Gerätename, den andere sehen")
	ziel := flag.String("ziel", "", "Ordner für empfangene Dateien")
	ohneBrowser := flag.Bool("ohne-browser", false, "Seite nicht automatisch öffnen")
	an := flag.String("an", "", "Zielgerät für 'senden' (Name oder Id)")
	code := flag.String("code", "", "Freigabecode des Zielgeräts für 'senden'")
	flag.Parse()

	if err := starten(flag.Args(), *port, *name, *ziel, *an, *code, *ohneBrowser); err != nil {
		fmt.Fprintln(os.Stderr, "Fehler:", err)
		os.Exit(1)
	}
}

func starten(befehle []string, port int, name, ziel, an, code string, ohneBrowser bool) error {
	einst, err := einstellungen.Laden()
	if err != nil {
		return err
	}
	if port != 0 {
		einst.Port = port
	}
	if name != "" {
		einst.GeraeteName = name
	}
	if ziel != "" {
		einst.Zielordner = ziel
		if err := os.MkdirAll(ziel, 0o755); err != nil {
			return err
		}
	}
	if err := einst.Speichern(); err != nil {
		return err
	}

	ordner, err := einstellungen.Ordner()
	if err != nil {
		return err
	}
	kenn, err := kennung.Laden(ordner, einst.GeraeteName)
	if err != nil {
		return err
	}
	liste := suche.NeueListe(einst.GeraeteId)
	abgabe, err := versand.Neu(filepath.Join(ordner, "ausgang"), einst.GeraeteName, einst.CodeFuer)
	if err != nil {
		return err
	}

	ctx, abbrechen := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer abbrechen()

	switch {
	case len(befehle) > 0 && befehle[0] == "geraete":
		return geraeteZeigen(ctx, liste)
	case len(befehle) > 0 && befehle[0] == "senden":
		return sendenBefehl(ctx, einst, liste, abgabe, befehle[1:], an, code)
	}

	annahme, err := empfang.Neu(einst.Zielordner, einst.AutoAnnehmen)
	if err != nil {
		return err
	}
	aus, err := suche.Anmelden(einst.GeraeteId, einst.GeraeteName, kenn.Fingerabdruck, einst.Port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Hinweis: Anmeldung im Netzwerk nicht möglich:", err)
	} else {
		defer aus()
	}
	go liste.Beobachten(ctx, 15*time.Second)
	go abgabe.Schleife(ctx)

	d := &dienst.Dienst{Einst: einst, Kennung: kenn, Empfang: annahme, Versand: abgabe, Liste: liste}
	adresse := fmt.Sprintf("http://127.0.0.1:%d", einst.UiPort())
	fmt.Printf("Quicksend läuft.\n  Oberfläche: %s\n  Gerätename: %s\n  Freigabecode: %s\n  Empfang in: %s\n",
		adresse, einst.GeraeteName, einst.Freigabecode, einst.Zielordner)
	if !ohneBrowser {
		oeffnen(adresse)
	}
	return d.Starten(ctx)
}

func geraeteZeigen(ctx context.Context, liste *suche.Liste) error {
	suchZeit, abbrechen := context.WithTimeout(ctx, 5*time.Second)
	defer abbrechen()
	if err := liste.EinmalSuchen(suchZeit, 4*time.Second); err != nil {
		return err
	}
	gefunden := liste.Geraete()
	if len(gefunden) == 0 {
		fmt.Println("Kein Gerät gefunden. Läuft Quicksend auf der Gegenseite, und sind beide im gleichen Netz?")
		return nil
	}
	for _, g := range gefunden {
		fmt.Printf("%-24s %-22s %s\n", g.Name, g.Adresse, g.Id)
	}
	return nil
}

func sendenBefehl(ctx context.Context, einst *einstellungen.Einstellungen, liste *suche.Liste,
	abgabe *versand.Versand, dateien []string, an, code string) error {
	if len(dateien) == 0 {
		return fmt.Errorf("welche Datei? Aufruf: quicksend senden <datei> -an <gerät> -code <code>")
	}
	if an == "" {
		return fmt.Errorf("welches Gerät? -an <Name oder Id>, sichtbare Geräte zeigt 'quicksend geraete'")
	}
	suchZeit, abbrechen := context.WithTimeout(ctx, 6*time.Second)
	defer abbrechen()
	if err := liste.EinmalSuchen(suchZeit, 4*time.Second); err != nil {
		return err
	}
	g, da := liste.Finden(an)
	if !da {
		return fmt.Errorf("Gerät %q ist nicht zu sehen", an)
	}
	if code == "" {
		code = einst.CodeFuer(g.Id)
	}
	if code == "" {
		return fmt.Errorf("Freigabecode von %s fehlt: -code <sechs Ziffern>", g.Name)
	}
	if err := einst.CodeMerken(g.Id, code); err != nil {
		return err
	}

	var warten []string
	for _, pfad := range dateien {
		datei, err := os.Open(pfad)
		if err != nil {
			return err
		}
		e, fehler := abgabe.Einreihen(versand.Ziel{Id: g.Id, Name: g.Name, Adresse: g.Adresse, Finger: g.Finger},
			datei, filepath.Base(pfad))
		datei.Close()
		if fehler != nil {
			return fehler
		}
		warten = append(warten, e.Id)
		fmt.Printf("eingereiht: %s (%d Bytes)\n", e.Name, e.Groesse)
	}

	lauf, anhalten := context.WithCancel(ctx)
	defer anhalten()
	go abgabe.Schleife(lauf)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(time.Second):
		}
		fertig := 0
		for _, id := range warten {
			for _, e := range abgabe.Liste() {
				if e.Id != id {
					continue
				}
				switch e.Zustand {
				case tagebuch.Fertig:
					fertig++
				case tagebuch.Fehler:
					return fmt.Errorf("%s ist fehlgeschlagen: %s", e.Name, e.Meldung)
				default:
					grund := ""
					if e.Meldung != "" {
						grund = " — " + e.Meldung
					}
					fmt.Printf("\r%s: %d%% (%s%s)        ", e.Name, int(100*e.Anteil()), e.Zustand, grund)
				}
			}
		}
		if fertig == len(warten) {
			fmt.Printf("\rfertig: %d Datei(en) angekommen        \n", fertig)
			return nil
		}
	}
}

// oeffnen shows the page in whatever browser the system uses.
func oeffnen(adresse string) {
	var befehl *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		befehl = exec.Command("open", adresse)
	case "windows":
		befehl = exec.Command("rundll32", "url.dll,FileProtocolHandler", adresse)
	default:
		befehl = exec.Command("xdg-open", adresse)
	}
	if err := befehl.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "Seite bitte selbst öffnen:", adresse)
	}
}
