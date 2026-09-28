// Package kennung gives the device its TLS identity. The certificate is generated once
// and kept; its fingerprint is announced over mDNS, so a sender can tell whether it is
// really talking to the device it discovered.
package kennung

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

type Kennung struct {
	Zertifikat    tls.Certificate
	Fingerabdruck string
}

// Laden reads the certificate from the config folder, creating one on first start.
func Laden(ordner, name string) (*Kennung, error) {
	zertPfad := filepath.Join(ordner, "geraet.crt")
	schluesselPfad := filepath.Join(ordner, "geraet.key")
	if _, err := os.Stat(zertPfad); os.IsNotExist(err) {
		if fehler := erzeugen(zertPfad, schluesselPfad, name); fehler != nil {
			return nil, fehler
		}
	}
	paar, err := tls.LoadX509KeyPair(zertPfad, schluesselPfad)
	if err != nil {
		return nil, err
	}
	return &Kennung{Zertifikat: paar, Fingerabdruck: Fingerabdruck(paar.Certificate[0])}, nil
}

// Fingerabdruck is the SHA-256 of the certificate in DER form, lower case hex.
func Fingerabdruck(der []byte) string {
	summe := sha256.Sum256(der)
	return hex.EncodeToString(summe[:])
}

func erzeugen(zertPfad, schluesselPfad, name string) error {
	schluessel, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	seriennummer, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	vorlage := x509.Certificate{
		SerialNumber:          seriennummer,
		Subject:               pkix.Name{CommonName: "quicksend " + name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &vorlage, &vorlage, &schluessel.PublicKey, schluessel)
	if err != nil {
		return err
	}
	if err := schreiben(zertPfad, "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	rohschluessel, err := x509.MarshalECPrivateKey(schluessel)
	if err != nil {
		return err
	}
	return schreiben(schluesselPfad, "EC PRIVATE KEY", rohschluessel, 0o600)
}

func schreiben(pfad, art string, rohdaten []byte, rechte os.FileMode) error {
	datei, err := os.OpenFile(pfad, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, rechte)
	if err != nil {
		return err
	}
	defer datei.Close()
	if err := pem.Encode(datei, &pem.Block{Type: art, Bytes: rohdaten}); err != nil {
		return err
	}
	return datei.Sync()
}
