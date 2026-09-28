package dienst

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A six digit code has a million combinations, which a program on the same network would
// work through in minutes. The brake makes that pointless: after a handful of wrong codes
// an address has to wait, and the wait doubles with every further miss.
const (
	FreieVersuche = 5
	ErsteSperre   = 30 * time.Second
	MaxSperre     = 15 * time.Minute
	Vergessen     = time.Hour // a quiet address is forgotten again
)

type bremse struct {
	sperre sync.Mutex
	stand  map[string]*versuchsstand
	jetzt  func() time.Time // swapped out in tests
}

type versuchsstand struct {
	fehler        int
	gesperrtBis   time.Time
	letzterFehler time.Time
}

func neueBremse() *bremse {
	return &bremse{stand: map[string]*versuchsstand{}, jetzt: time.Now}
}

// Darf reports whether an address may try, and if not, how long it still has to wait.
func (b *bremse) Darf(adresse string) (bool, time.Duration) {
	b.sperre.Lock()
	defer b.sperre.Unlock()
	s, da := b.stand[adresse]
	if !da {
		return true, 0
	}
	jetzt := b.jetzt()
	if jetzt.Sub(s.letzterFehler) > Vergessen {
		delete(b.stand, adresse)
		return true, 0
	}
	if rest := s.gesperrtBis.Sub(jetzt); rest > 0 {
		return false, rest
	}
	return true, 0
}

// Fehlschlag counts a wrong code and locks the address once the allowance is used up.
func (b *bremse) Fehlschlag(adresse string) {
	b.sperre.Lock()
	defer b.sperre.Unlock()
	jetzt := b.jetzt()
	s, da := b.stand[adresse]
	if !da {
		s = &versuchsstand{}
		b.stand[adresse] = s
	}
	s.fehler++
	s.letzterFehler = jetzt
	if s.fehler < FreieVersuche {
		return
	}
	// The first lock comes after the allowance is used up, and every further miss
	// doubles the wait: 30s, 1m, 2m … up to the maximum.
	dauer := ErsteSperre
	for i := 1; i < s.fehler-FreieVersuche+1; i++ {
		dauer *= 2
		if dauer >= MaxSperre {
			dauer = MaxSperre
			break
		}
	}
	s.gesperrtBis = jetzt.Add(dauer)
}

// Erfolg clears the record of an address that got the code right.
func (b *bremse) Erfolg(adresse string) {
	b.sperre.Lock()
	defer b.sperre.Unlock()
	delete(b.stand, adresse)
}

// absender is the address a request came from, without its port, so all attempts from one
// machine share one counter.
func absender(r *http.Request) string {
	wirt, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return wirt
}

// NurOertlich keeps the user interface reachable from this machine only. Binding to
// loopback already does most of that; this also stops a web page from elsewhere pointing a
// name at 127.0.0.1 and talking to the interface through the browser.
func NurOertlich(weiter http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		wirt := r.Host
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			wirt = h
		}
		erlaubt := wirt == "localhost" || wirt == "127.0.0.1" || wirt == "[::1]" || wirt == "::1"
		if !erlaubt {
			http.Error(w, "die Oberfläche antwortet nur auf 127.0.0.1", http.StatusForbidden)
			return
		}
		weiter.ServeHTTP(w, r)
	})
}

// mitKopfzeilen sets the headers that keep a browser from doing anything clever with the
// answers: no sniffing, no framing, no outside resources.
func mitKopfzeilen(weiter http.Handler, oberflaeche bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		kopf := w.Header()
		kopf.Set("X-Content-Type-Options", "nosniff")
		kopf.Set("Referrer-Policy", "no-referrer")
		if oberflaeche {
			kopf.Set("Content-Security-Policy",
				"default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; "+
					"connect-src 'self'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
			kopf.Set("X-Frame-Options", "DENY")
			kopf.Set("Cache-Control", "no-store")
		}
		weiter.ServeHTTP(w, r)
	})
}

// istSchreibend tells the calls that change something from the ones that only read.
func istSchreibend(art string) bool {
	return art == http.MethodPost || art == http.MethodPut || art == http.MethodDelete
}

// GleicherUrsprung rejects a cross-site request to the interface. A browser always sends
// Sec-Fetch-Site, so a page on the internet cannot forge it.
func GleicherUrsprung(weiter http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if istSchreibend(r.Method) {
			seite := r.Header.Get("Sec-Fetch-Site")
			if seite != "" && seite != "same-origin" && seite != "none" {
				http.Error(w, "Anfrage von einer fremden Seite", http.StatusForbidden)
				return
			}
			if ursprung := r.Header.Get("Origin"); ursprung != "" {
				if !strings.Contains(ursprung, "127.0.0.1") && !strings.Contains(ursprung, "localhost") {
					http.Error(w, "fremder Ursprung", http.StatusForbidden)
					return
				}
			}
		}
		weiter.ServeHTTP(w, r)
	})
}
