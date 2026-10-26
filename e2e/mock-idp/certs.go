package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// The stack's throw-away certificate authority. `serve` makes it on its first
// start and writes three files into the shared volume:
//
//	tls.key  the server key (read by edge and github-tls, which run as root)
//	tls.pem  the server certificate, then the CA (the chain a TLS server sends)
//	ca.pem   the CA alone, written LAST: its presence means the set is complete.
//	         The app and the validators trust it through SSL_CERT_FILE.
//
// Nothing here is ever reused outside one run of the stack: the volume is
// removed with `compose down -v`, and the CA lives a week at most.
const (
	caFile   = "ca.pem"
	certFile = "tls.pem"
	keyFile  = "tls.key"
)

func ensureCerts(dir string, hosts []string) error {
	if _, err := os.Stat(filepath.Join(dir, caFile)); err == nil {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	now := time.Now()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	caTmpl := &x509.Certificate{
		SerialNumber:          serial(),
		Subject:               pkix.Name{CommonName: "filex-apps e2e throw-away CA"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(7 * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLenZero:        true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	leaf := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: hosts[0]},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(7 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			leaf.IPAddresses = append(leaf.IPAddresses, ip)
		} else {
			leaf.DNSNames = append(leaf.DNSNames, h)
		}
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &key.PublicKey, caKey)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}

	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}), caPEM...)
	if err := writeAtomic(filepath.Join(dir, keyFile), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, certFile), chain, 0o644); err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, caFile), caPEM, 0o644)
}

// waitCerts loads the server certificate once `serve` has written it.
func waitCerts(dir string, timeout time.Duration) (tls.Certificate, error) {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(filepath.Join(dir, caFile)); err == nil {
			return tls.LoadX509KeyPair(filepath.Join(dir, certFile), filepath.Join(dir, keyFile))
		}
		if time.Now().After(deadline) {
			return tls.Certificate{}, fmt.Errorf("no certificate in %s after %s (is mock-idp serve running?)", dir, timeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func writeAtomic(path string, b []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		panic(err)
	}
	return n
}
