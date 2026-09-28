package empfang

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Dschonas04/nahfunk/internal/tagebuch"
)

func summe(daten []byte) string {
	s := sha256.Sum256(daten)
	return hex.EncodeToString(s[:])
}

func TestWiederaufnahmeNachAbbruch(t *testing.T) {
	ordner := t.TempDir()
	x, err := Neu(ordner, true)
	if err != nil {
		t.Fatal(err)
	}
	daten := bytes.Repeat([]byte("nahfunk"), 5000)
	a := Anmeldung{Name: "probe.bin", Groesse: int64(len(daten)), Sha256: summe(daten), Gegenseite: "Testgerät"}

	erste, err := x.Anmelden(a)
	if err != nil {
		t.Fatal(err)
	}
	haelfte := len(daten) / 2
	if _, err := x.Schreiben(erste.Id, 0, bytes.NewReader(daten[:haelfte])); err != nil {
		t.Fatal(err)
	}

	// Same file offered again: must continue, not start over.
	zweite, err := x.Anmelden(a)
	if err != nil {
		t.Fatal(err)
	}
	if zweite.Id != erste.Id {
		t.Fatalf("neue Id %s statt %s", zweite.Id, erste.Id)
	}
	if zweite.Versatz != int64(haelfte) {
		t.Fatalf("Versatz %d statt %d", zweite.Versatz, haelfte)
	}

	if _, err := x.Schreiben(erste.Id, zweite.Versatz, bytes.NewReader(daten[haelfte:])); err != nil {
		t.Fatal(err)
	}
	e, err := x.Abschluss(erste.Id)
	if err != nil {
		t.Fatal(err)
	}
	gelesen, err := os.ReadFile(e.Ziel)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gelesen, daten) {
		t.Fatal("Inhalt stimmt nicht")
	}
	if _, err := os.Stat(filepath.Join(ordner, ".nahfunk", erste.Id+".teil")); !os.IsNotExist(err) {
		t.Fatal("Teildatei wurde nicht aufgeräumt")
	}
}

func TestNeustartSetztFort(t *testing.T) {
	ordner := t.TempDir()
	daten := bytes.Repeat([]byte("x"), 4096)
	a := Anmeldung{Name: "halb.bin", Groesse: int64(len(daten)), Sha256: summe(daten)}

	erst, err := Neu(ordner, true)
	if err != nil {
		t.Fatal(err)
	}
	antwort, err := erst.Anmelden(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := erst.Schreiben(antwort.Id, 0, bytes.NewReader(daten[:1000])); err != nil {
		t.Fatal(err)
	}

	// A fresh instance stands for the app after a crash or a restart.
	wieder, err := Neu(ordner, true)
	if err != nil {
		t.Fatal(err)
	}
	nochmal, err := wieder.Anmelden(a)
	if err != nil {
		t.Fatal(err)
	}
	if nochmal.Versatz != 1000 {
		t.Fatalf("nach Neustart Versatz %d statt 1000", nochmal.Versatz)
	}
	if _, err := wieder.Schreiben(nochmal.Id, nochmal.Versatz, bytes.NewReader(daten[1000:])); err != nil {
		t.Fatal(err)
	}
	if _, err := wieder.Abschluss(nochmal.Id); err != nil {
		t.Fatal(err)
	}
}

func TestFalscheSummeWirdAbgelehnt(t *testing.T) {
	ordner := t.TempDir()
	x, err := Neu(ordner, true)
	if err != nil {
		t.Fatal(err)
	}
	daten := []byte("richtig")
	antwort, err := x.Anmelden(Anmeldung{Name: "falsch.bin", Groesse: int64(len(daten)), Sha256: summe([]byte("etwas anderes"))})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.Schreiben(antwort.Id, 0, bytes.NewReader(daten)); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Abschluss(antwort.Id); err == nil {
		t.Fatal("Abschluss hätte scheitern müssen")
	}
	if _, err := os.Stat(filepath.Join(ordner, ".nahfunk", antwort.Id+".teil")); err != nil {
		t.Fatal("Teildatei muss für einen neuen Versuch liegen bleiben")
	}
	if _, err := os.Stat(filepath.Join(ordner, "falsch.bin")); !os.IsNotExist(err) {
		t.Fatal("beschädigte Datei darf nicht im Zielordner landen")
	}
}

func TestOhneFreigabeKeinSchreiben(t *testing.T) {
	ordner := t.TempDir()
	x, err := Neu(ordner, false)
	if err != nil {
		t.Fatal(err)
	}
	daten := []byte("wartet auf Freigabe")
	antwort, err := x.Anmelden(Anmeldung{Name: "warte.bin", Groesse: int64(len(daten)), Sha256: summe(daten)})
	if err != nil {
		t.Fatal(err)
	}
	if antwort.Zustand != tagebuch.Freigabe {
		t.Fatalf("Zustand %s statt %s", antwort.Zustand, tagebuch.Freigabe)
	}
	if _, err := x.Schreiben(antwort.Id, 0, bytes.NewReader(daten)); err == nil {
		t.Fatal("ohne Freigabe darf nicht geschrieben werden")
	}
	if err := x.Freigeben(antwort.Id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Schreiben(antwort.Id, 0, bytes.NewReader(daten)); err != nil {
		t.Fatal(err)
	}
	if _, err := x.Abschluss(antwort.Id); err != nil {
		t.Fatal(err)
	}
}

func TestVersatzZuWeitMeldetStand(t *testing.T) {
	ordner := t.TempDir()
	x, err := Neu(ordner, true)
	if err != nil {
		t.Fatal(err)
	}
	daten := []byte("kurz")
	antwort, err := x.Anmelden(Anmeldung{Name: "versatz.bin", Groesse: int64(len(daten)), Sha256: summe(daten)})
	if err != nil {
		t.Fatal(err)
	}
	auf, err := x.Schreiben(antwort.Id, 99, bytes.NewReader(daten))
	if err == nil {
		t.Fatal("zu großer Versatz muss abgelehnt werden")
	}
	if auf != 0 {
		t.Fatalf("gemeldeter Stand %d statt 0", auf)
	}
	var versatzFehler ErrVersatz
	if !asVersatz(err, &versatzFehler) {
		t.Fatalf("falscher Fehlertyp: %v", err)
	}
}

func TestNameWirdNichtUeberschrieben(t *testing.T) {
	ordner := t.TempDir()
	if err := os.WriteFile(filepath.Join(ordner, "doppelt.txt"), []byte("alt"), 0o600); err != nil {
		t.Fatal(err)
	}
	x, err := Neu(ordner, true)
	if err != nil {
		t.Fatal(err)
	}
	daten := []byte("neu")
	antwort, err := x.Anmelden(Anmeldung{Name: "doppelt.txt", Groesse: int64(len(daten)), Sha256: summe(daten)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.Schreiben(antwort.Id, 0, bytes.NewReader(daten)); err != nil {
		t.Fatal(err)
	}
	e, err := x.Abschluss(antwort.Id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(filepath.Base(e.Ziel), "(2)") {
		t.Fatalf("Zielname %s überschreibt die vorhandene Datei", e.Ziel)
	}
	alt, err := os.ReadFile(filepath.Join(ordner, "doppelt.txt"))
	if err != nil || string(alt) != "alt" {
		t.Fatal("die vorhandene Datei wurde verändert")
	}
}

func asVersatz(err error, ziel *ErrVersatz) bool {
	v, ok := err.(ErrVersatz)
	if ok {
		*ziel = v
	}
	return ok
}
