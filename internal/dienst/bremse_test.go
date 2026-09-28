package dienst

import (
	"testing"
	"time"
)

func TestBremseSperrtErstNachDenFreienVersuchen(t *testing.T) {
	uhr := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	b := neueBremse()
	b.jetzt = func() time.Time { return uhr }

	for i := 0; i < FreieVersuche-1; i++ {
		b.Fehlschlag("10.0.0.5")
		if frei, _ := b.Darf("10.0.0.5"); !frei {
			t.Fatalf("nach %d Fehlversuchen schon gesperrt", i+1)
		}
	}

	b.Fehlschlag("10.0.0.5")
	frei, rest := b.Darf("10.0.0.5")
	if frei {
		t.Fatalf("nach %d Fehlversuchen muss gesperrt sein", FreieVersuche)
	}
	if rest > ErsteSperre || rest <= ErsteSperre-time.Second {
		t.Fatalf("Wartezeit %s statt %s", rest, ErsteSperre)
	}

	// Waiting it out lets exactly one more attempt through.
	uhr = uhr.Add(ErsteSperre + time.Second)
	if frei, _ := b.Darf("10.0.0.5"); !frei {
		t.Fatal("nach Ablauf der Sperre muss wieder ein Versuch gehen")
	}

	// The next miss costs twice as long.
	b.Fehlschlag("10.0.0.5")
	_, rest = b.Darf("10.0.0.5")
	if rest <= ErsteSperre {
		t.Fatalf("Wartezeit wächst nicht: %s", rest)
	}
}

func TestBremseTrenntAdressen(t *testing.T) {
	b := neueBremse()
	for i := 0; i < FreieVersuche+3; i++ {
		b.Fehlschlag("10.0.0.5")
	}
	if frei, _ := b.Darf("10.0.0.5"); frei {
		t.Fatal("die ratende Adresse muss gesperrt sein")
	}
	if frei, _ := b.Darf("10.0.0.6"); !frei {
		t.Fatal("eine andere Adresse darf nicht mitgesperrt werden")
	}
}

func TestErfolgLoeschtDenStand(t *testing.T) {
	b := neueBremse()
	for i := 0; i < FreieVersuche; i++ {
		b.Fehlschlag("10.0.0.7")
	}
	b.Erfolg("10.0.0.7")
	if frei, rest := b.Darf("10.0.0.7"); !frei {
		t.Fatalf("nach einem richtigen Code darf keine Sperre bleiben, noch %s", rest)
	}
}

func TestSperreVerfaelltNachRuhe(t *testing.T) {
	uhr := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	b := neueBremse()
	b.jetzt = func() time.Time { return uhr }
	for i := 0; i < FreieVersuche+4; i++ {
		b.Fehlschlag("10.0.0.8")
	}
	uhr = uhr.Add(Vergessen + time.Minute)
	if frei, _ := b.Darf("10.0.0.8"); !frei {
		t.Fatal("nach einer Stunde Ruhe muss die Adresse vergessen sein")
	}
}

func TestSperreWaechstNichtUeberDasMaximum(t *testing.T) {
	uhr := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	b := neueBremse()
	b.jetzt = func() time.Time { return uhr }
	for i := 0; i < 50; i++ {
		b.Fehlschlag("10.0.0.9")
	}
	_, rest := b.Darf("10.0.0.9")
	if rest > MaxSperre {
		t.Fatalf("Wartezeit %s über dem Maximum %s", rest, MaxSperre)
	}
	if rest < MaxSperre-time.Second {
		t.Fatalf("Wartezeit %s erreicht das Maximum %s nicht", rest, MaxSperre)
	}
}
