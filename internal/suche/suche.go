// Package suche is the "find the other device" part: every instance announces itself on
// the local network over mDNS, and every instance keeps a list of the ones it hears.
package suche

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/grandcat/zeroconf"
)

// Dienstart is the DNS-SD service type. It carries the app's own name so nothing else
// answers by accident.
const Dienstart = "_nahfunk._tcp"

type Geraet struct {
	Id      string    `json:"id"`
	Name    string    `json:"name"`
	Adresse string    `json:"adresse"`
	Finger  string    `json:"finger"`
	Gesehen time.Time `json:"gesehen"`
}

// Anmelden puts this device on the network. The returned function takes it off again.
func Anmelden(id, name, finger string, port int) (func(), error) {
	server, err := zeroconf.Register("nahfunk-"+id, Dienstart, "local.", port,
		[]string{"id=" + id, "name=" + name, "fp=" + finger}, nil)
	if err != nil {
		return nil, err
	}
	return server.Shutdown, nil
}

// Liste keeps what was heard recently, so the user interface always has something to show
// even between scans.
type Liste struct {
	eigeneId string
	sperre   sync.Mutex
	geraete  map[string]Geraet
}

func NeueListe(eigeneId string) *Liste {
	return &Liste{eigeneId: eigeneId, geraete: map[string]Geraet{}}
}

// Beobachten scans over and over until the context ends. A failed scan is not fatal: on a
// network without multicast the list simply stays empty.
func (l *Liste) Beobachten(ctx context.Context, abstand time.Duration) {
	for {
		_ = l.EinmalSuchen(ctx, 3*time.Second)
		select {
		case <-ctx.Done():
			return
		case <-time.After(abstand):
		}
	}
}

// EinmalSuchen runs a single scan and folds the result into the list.
func (l *Liste) EinmalSuchen(ctx context.Context, dauer time.Duration) error {
	sucher, err := zeroconf.NewResolver(nil)
	if err != nil {
		return err
	}
	treffer := make(chan *zeroconf.ServiceEntry, 16)
	fertig := make(chan struct{})
	go func() {
		defer close(fertig)
		for eintrag := range treffer {
			g, ok := ausEintrag(eintrag)
			if !ok || g.Id == l.eigeneId {
				continue
			}
			l.sperre.Lock()
			l.geraete[g.Id] = g
			l.sperre.Unlock()
		}
	}()
	suchZeit, abbrechen := context.WithTimeout(ctx, dauer)
	defer abbrechen()
	if err := sucher.Browse(suchZeit, Dienstart, "local.", treffer); err != nil {
		return err
	}
	<-suchZeit.Done()
	<-fertig
	return nil
}

// Geraete returns everything heard within the last two minutes, sorted by name.
func (l *Liste) Geraete() []Geraet {
	l.sperre.Lock()
	defer l.sperre.Unlock()
	var raus []Geraet
	for _, g := range l.geraete {
		if time.Since(g.Gesehen) > 2*time.Minute {
			continue
		}
		raus = append(raus, g)
	}
	sort.Slice(raus, func(i, j int) bool { return raus[i].Name < raus[j].Name })
	return raus
}

// Finden looks a device up by id or by name.
func (l *Liste) Finden(was string) (Geraet, bool) {
	for _, g := range l.Geraete() {
		if g.Id == was || strings.EqualFold(g.Name, was) {
			return g, true
		}
	}
	return Geraet{}, false
}

func ausEintrag(e *zeroconf.ServiceEntry) (Geraet, bool) {
	if e == nil || e.Port == 0 {
		return Geraet{}, false
	}
	g := Geraet{Gesehen: time.Now()}
	for _, text := range e.Text {
		schluessel, wert, gefunden := strings.Cut(text, "=")
		if !gefunden {
			continue
		}
		switch schluessel {
		case "id":
			g.Id = wert
		case "name":
			g.Name = wert
		case "fp":
			g.Finger = wert
		}
	}
	if g.Id == "" {
		return Geraet{}, false
	}
	if g.Name == "" {
		g.Name = e.Instance
	}
	adresse := ersteAdresse(e)
	if adresse == "" {
		return Geraet{}, false
	}
	g.Adresse = net.JoinHostPort(adresse, fmt.Sprint(e.Port))
	return g, true
}

func ersteAdresse(e *zeroconf.ServiceEntry) string {
	for _, ip := range e.AddrIPv4 {
		if ip != nil && !ip.IsLoopback() {
			return ip.String()
		}
	}
	for _, ip := range e.AddrIPv6 {
		if ip != nil && !ip.IsLoopback() && ip.IsGlobalUnicast() {
			return ip.String()
		}
	}
	return ""
}
