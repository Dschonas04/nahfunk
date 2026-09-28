package tagebuch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSchreibenUndLesen(t *testing.T) {
	ordner := t.TempDir()
	e := Eintrag{Id: "abc123", Name: "datei.bin", Groesse: 10, Sha256: "ff", Zustand: Laeuft, Uebertragen: 4}
	if err := Schreiben(ordner, e); err != nil {
		t.Fatal(err)
	}
	gelesen, err := Lesen(ordner, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if gelesen.Uebertragen != 4 || gelesen.Name != "datei.bin" || gelesen.Zustand != Laeuft {
		t.Fatalf("falsch gelesen: %+v", gelesen)
	}
	if gelesen.Geaendert.IsZero() {
		t.Fatal("Änderungszeit fehlt")
	}
	if _, err := os.Stat(filepath.Join(ordner, "abc123.json.neu")); !os.IsNotExist(err) {
		t.Fatal("die vorläufige Datei wurde nicht umbenannt")
	}
}

func TestAlleSortiertNeuesteZuerst(t *testing.T) {
	ordner := t.TempDir()
	for _, id := range []string{"eins", "zwei"} {
		if err := Schreiben(ordner, Eintrag{Id: id, Name: id}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := os.WriteFile(filepath.Join(ordner, "kaputt.json"), []byte("{kein json"), 0o600); err != nil {
		t.Fatal(err)
	}
	alle, err := Alle(ordner)
	if err != nil {
		t.Fatal(err)
	}
	if len(alle) != 2 {
		t.Fatalf("%d Einträge, erwartet 2 (ein beschädigter darf den Rest nicht verstecken)", len(alle))
	}
	if alle[0].Id != "zwei" {
		t.Fatalf("Reihenfolge falsch: %s zuerst", alle[0].Id)
	}
}

func TestAnteil(t *testing.T) {
	if a := (Eintrag{Groesse: 200, Uebertragen: 50}).Anteil(); a != 0.25 {
		t.Fatalf("Anteil %v statt 0.25", a)
	}
	if a := (Eintrag{}).Anteil(); a != 0 {
		t.Fatalf("Anteil %v statt 0", a)
	}
}

func TestLoeschenIstNachsichtig(t *testing.T) {
	if err := Loeschen(t.TempDir(), "gibtesnicht"); err != nil {
		t.Fatalf("Löschen eines fehlenden Eintrags soll still gelingen: %v", err)
	}
}
