package dienst_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Dschonas04/quicksend/internal/dienst"
	"github.com/Dschonas04/quicksend/internal/einstellungen"
	"github.com/Dschonas04/quicksend/internal/empfang"
	"github.com/Dschonas04/quicksend/internal/kennung"
	"github.com/Dschonas04/quicksend/internal/suche"
	"github.com/Dschonas04/quicksend/internal/tagebuch"
	"github.com/Dschonas04/quicksend/internal/versand"
)

const testCode = "123456"

// abbruchLeser stands for a connection that dies mid-transfer: it hands out a fixed
// number of bytes and then fails, exactly like a dropped WLAN connection.
type abbruchLeser struct {
	unten  io.Reader
	uebrig int64
}

func (a *abbruchLeser) Read(p []byte) (int, error) {
	if a.uebrig <= 0 {
		return 0, errors.New("Verbindung abgebrochen")
	}
	if int64(len(p)) > a.uebrig {
		p = p[:a.uebrig]
	}
	n, err := a.unten.Read(p)
	a.uebrig -= int64(n)
	return n, err
}

// gegenseite starts a receiving side on a random port. abbrucheBei cuts the first n data
// pushes short, so the resume path is what finishes the transfer.
func gegenseite(t *testing.T, zielordner string, abbrueche int32) (adresse, finger string, gezaehlt *int32) {
	t.Helper()
	kennOrdner := t.TempDir()
	kenn, err := kennung.Laden(kennOrdner, "empfaenger")
	if err != nil {
		t.Fatal(err)
	}
	annahme, err := empfang.Neu(zielordner, true)
	if err != nil {
		t.Fatal(err)
	}
	einst := &einstellungen.Einstellungen{
		GeraeteId:    "empfaenger-id",
		GeraeteName:  "Empfänger",
		Freigabecode: testCode,
		Zielordner:   zielordner,
		AutoAnnehmen: true,
	}
	d := &dienst.Dienst{Einst: einst, Kennung: kenn, Empfang: annahme, Liste: suche.NeueListe(einst.GeraeteId)}

	var zaehler int32
	innen := d.GegenMux()
	aussen := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gegen/daten" && atomic.LoadInt32(&zaehler) < abbrueche {
			atomic.AddInt32(&zaehler, 1)
			r.Body = io.NopCloser(&abbruchLeser{unten: r.Body, uebrig: 1 << 20})
		}
		innen.ServeHTTP(w, r)
	})

	lauscher, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{
		Handler:   aussen,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{kenn.Zertifikat}, MinVersion: tls.VersionTLS12},
	}
	go func() { _ = server.ServeTLS(lauscher, "", "") }()
	t.Cleanup(func() {
		aus, abbrechen := context.WithTimeout(context.Background(), 2*time.Second)
		defer abbrechen()
		_ = server.Shutdown(aus)
	})
	return lauscher.Addr().String(), kenn.Fingerabdruck, &zaehler
}

func warten(t *testing.T, abgabe *versand.Versand, id string, dauer time.Duration) tagebuch.Eintrag {
	t.Helper()
	ende := time.Now().Add(dauer)
	var letzter tagebuch.Eintrag
	for time.Now().Before(ende) {
		for _, e := range abgabe.Liste() {
			if e.Id != id {
				continue
			}
			letzter = e
			if e.Zustand == tagebuch.Fertig || e.Zustand == tagebuch.Fehler {
				return e
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return letzter
}

func TestUebertragungUeberlebtAbbruch(t *testing.T) {
	zielordner := t.TempDir()
	adresse, finger, abbrueche := gegenseite(t, zielordner, 2)

	daten := bytes.Repeat([]byte("Quicksend überträgt auch nach einem Abbruch weiter. "), 60000)
	abgabe, err := versand.Neu(t.TempDir(), "Sender", func(string) string { return testCode })
	if err != nil {
		t.Fatal(err)
	}
	ctx, abbrechen := context.WithCancel(context.Background())
	defer abbrechen()
	go abgabe.Schleife(ctx)

	e, err := abgabe.Einreihen(versand.Ziel{
		Id: "empfaenger-id", Name: "Empfänger", Adresse: adresse, Finger: finger,
	}, bytes.NewReader(daten), "grosse-datei.bin")
	if err != nil {
		t.Fatal(err)
	}

	fertig := warten(t, abgabe, e.Id, 60*time.Second)
	if fertig.Zustand != tagebuch.Fertig {
		t.Fatalf("Zustand %s, Meldung %q", fertig.Zustand, fertig.Meldung)
	}
	if got := atomic.LoadInt32(abbrueche); got != 2 {
		t.Fatalf("%d Abbrüche ausgelöst, erwartet 2", got)
	}
	gelesen, err := os.ReadFile(filepath.Join(zielordner, "grosse-datei.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gelesen, daten) {
		t.Fatalf("Inhalt weicht ab: %d statt %d Bytes", len(gelesen), len(daten))
	}
	if _, err := os.Stat(fertig.Quelle); !os.IsNotExist(err) {
		t.Fatal("Zwischendatei wurde nach Erfolg nicht gelöscht")
	}
}

func TestFalscherFingerabdruckWirdAbgelehnt(t *testing.T) {
	zielordner := t.TempDir()
	adresse, _, _ := gegenseite(t, zielordner, 0)

	abgabe, err := versand.Neu(t.TempDir(), "Sender", func(string) string { return testCode })
	if err != nil {
		t.Fatal(err)
	}
	ctx, abbrechen := context.WithCancel(context.Background())
	defer abbrechen()
	go abgabe.Schleife(ctx)

	e, err := abgabe.Einreihen(versand.Ziel{
		Id: "empfaenger-id", Name: "Fremd", Adresse: adresse,
		Finger: strings.Repeat("ab", 32), // fingerprint of some other device
	}, bytes.NewReader([]byte("geheim")), "heikel.txt")
	if err != nil {
		t.Fatal(err)
	}
	stand := warten(t, abgabe, e.Id, 8*time.Second)
	if stand.Zustand == tagebuch.Fertig {
		t.Fatal("Übertragung an ein Gerät mit falschem Fingerabdruck darf nicht gelingen")
	}
	if !strings.Contains(stand.Meldung, "Fingerabdruck") {
		t.Fatalf("Meldung nennt den Grund nicht: %q", stand.Meldung)
	}
	if _, err := os.Stat(filepath.Join(zielordner, "heikel.txt")); !os.IsNotExist(err) {
		t.Fatal("es wurde trotzdem etwas geschrieben")
	}
}

func TestFalscherCodeWirdAbgelehnt(t *testing.T) {
	adresse, finger, _ := gegenseite(t, t.TempDir(), 0)
	abgabe, err := versand.Neu(t.TempDir(), "Sender", func(string) string { return "000000" })
	if err != nil {
		t.Fatal(err)
	}
	ctx, abbrechen := context.WithCancel(context.Background())
	defer abbrechen()
	go abgabe.Schleife(ctx)
	e, err := abgabe.Einreihen(versand.Ziel{Id: "empfaenger-id", Adresse: adresse, Finger: finger},
		bytes.NewReader([]byte("ohne Code")), "nein.txt")
	if err != nil {
		t.Fatal(err)
	}
	stand := warten(t, abgabe, e.Id, 6*time.Second)
	if stand.Zustand == tagebuch.Fertig {
		t.Fatal("falscher Code hätte abgelehnt werden müssen")
	}
	// Either the plain refusal or, after enough tries, the brake kicking in.
	if !strings.Contains(stand.Meldung, "403") && !strings.Contains(stand.Meldung, "Freigabecode") &&
		!strings.Contains(stand.Meldung, "429") && !strings.Contains(stand.Meldung, "falschen Codes") {
		t.Fatalf("Meldung nennt den Grund nicht: %q", stand.Meldung)
	}
}

func TestZuVieleFalscheCodesWerdenGebremst(t *testing.T) {
	adresse, _, _ := gegenseite(t, t.TempDir(), 0)
	kunde := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, // pinning is tested elsewhere
	}}
	rufen := func(code string) int {
		anfrage, err := http.NewRequest(http.MethodGet, "https://"+adresse+"/gegen/hallo", nil)
		if err != nil {
			t.Fatal(err)
		}
		anfrage.Header.Set("X-Quicksend-Code", code)
		antwort, err := kunde.Do(anfrage)
		if err != nil {
			t.Fatal(err)
		}
		defer antwort.Body.Close()
		return antwort.StatusCode
	}

	for i := 0; i < dienst.FreieVersuche; i++ {
		if kode := rufen("000000"); kode != http.StatusForbidden {
			t.Fatalf("Versuch %d antwortete %d statt 403", i+1, kode)
		}
	}
	if kode := rufen("000000"); kode != http.StatusTooManyRequests {
		t.Fatalf("nach %d Fehlversuchen kam %d statt 429", dienst.FreieVersuche+1, kode)
	}
	// While the brake holds, even the right code has to wait.
	if kode := rufen(testCode); kode != http.StatusTooManyRequests {
		t.Fatalf("während der Sperre kam %d statt 429", kode)
	}
}

func TestOberflaecheWeistFremdenWirtAb(t *testing.T) {
	aufzeichnung := httptest.NewRecorder()
	anfrage := httptest.NewRequest(http.MethodGet, "http://beispiel.invalid/api/zustand", nil)
	dienst.NurOertlich(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(aufzeichnung, anfrage)
	if aufzeichnung.Code != http.StatusForbidden {
		t.Fatalf("fremder Wirtsname kam mit %d durch", aufzeichnung.Code)
	}

	aufzeichnung = httptest.NewRecorder()
	anfrage = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:51766/api/zustand", nil)
	dienst.NurOertlich(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(aufzeichnung, anfrage)
	if aufzeichnung.Code != http.StatusOK {
		t.Fatalf("127.0.0.1 wurde mit %d abgewiesen", aufzeichnung.Code)
	}
}

func TestSchreibenVonFremderSeiteWirdAbgewiesen(t *testing.T) {
	weiter := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	faelle := []struct {
		name, art, seite, ursprung string
		erwartet                   int
	}{
		{"fremde Seite", http.MethodPost, "cross-site", "", http.StatusForbidden},
		{"fremder Ursprung", http.MethodPost, "", "http://beispiel.invalid", http.StatusForbidden},
		{"eigene Seite", http.MethodPost, "same-origin", "http://127.0.0.1:51766", http.StatusOK},
		{"Lesen bleibt frei", http.MethodGet, "cross-site", "", http.StatusOK},
	}
	for _, f := range faelle {
		aufzeichnung := httptest.NewRecorder()
		anfrage := httptest.NewRequest(f.art, "http://127.0.0.1:51766/api/senden", nil)
		if f.seite != "" {
			anfrage.Header.Set("Sec-Fetch-Site", f.seite)
		}
		if f.ursprung != "" {
			anfrage.Header.Set("Origin", f.ursprung)
		}
		dienst.GleicherUrsprung(weiter).ServeHTTP(aufzeichnung, anfrage)
		if aufzeichnung.Code != f.erwartet {
			t.Fatalf("%s: %d statt %d", f.name, aufzeichnung.Code, f.erwartet)
		}
	}
}

func TestWarteschlangeUeberlebtNeustart(t *testing.T) {
	zielordner := t.TempDir()
	sendeOrdner := t.TempDir()
	adresse, finger, _ := gegenseite(t, zielordner, 0)
	daten := []byte("dieser Auftrag übersteht einen Neustart des Programms")

	// First instance only queues the file, without ever running its worker.
	erste, err := versand.Neu(sendeOrdner, "Sender", func(string) string { return testCode })
	if err != nil {
		t.Fatal(err)
	}
	e, err := erste.Einreihen(versand.Ziel{Id: "empfaenger-id", Adresse: adresse, Finger: finger},
		bytes.NewReader(daten), "spaeter.txt")
	if err != nil {
		t.Fatal(err)
	}

	// Second instance stands for the app after a restart: it must find the job again.
	zweite, err := versand.Neu(sendeOrdner, "Sender", func(string) string { return testCode })
	if err != nil {
		t.Fatal(err)
	}
	ctx, abbrechen := context.WithCancel(context.Background())
	defer abbrechen()
	go zweite.Schleife(ctx)

	fertig := warten(t, zweite, e.Id, 20*time.Second)
	if fertig.Zustand != tagebuch.Fertig {
		t.Fatalf("Zustand %s, Meldung %q", fertig.Zustand, fertig.Meldung)
	}
	gelesen, err := os.ReadFile(filepath.Join(zielordner, "spaeter.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gelesen, daten) {
		t.Fatal("Inhalt stimmt nicht")
	}
}

func TestHalloBrauchtCode(t *testing.T) {
	adresse, finger, _ := gegenseite(t, t.TempDir(), 0)
	kunde := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		InsecureSkipVerify:    true,
		VerifyPeerCertificate: func(ketten [][]byte, _ [][]*x509.Certificate) error { return nil },
	}}}
	_ = finger
	antwort, err := kunde.Get(fmt.Sprintf("https://%s/gegen/hallo", adresse))
	if err != nil {
		t.Fatal(err)
	}
	defer antwort.Body.Close()
	if antwort.StatusCode != http.StatusForbidden {
		t.Fatalf("ohne Code kam %s", antwort.Status)
	}
}
