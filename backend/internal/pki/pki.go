// Package pki provides lightweight Certificate Authority helpers used by
// FeatherDeploy to establish mutual TLS (mTLS) between the main server and
// worker nodes.
//
// Key operations:
//   - GenerateCA        — create a self-signed root CA key + certificate
//   - SignNodeCert      — issue a leaf certificate for a node, signed by the CA
//   - ParseCertPEM      — decode a PEM certificate for inspection
//   - EncryptKey/DecryptKey — AES-256-GCM wrapper for private key storage
package pki

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
	"time"

	"github.com/ojhapranjal26/featherdeploy/backend/internal/crypto"
)

// CA holds a CA certificate and its private key.
type CA struct {
	CertPEM string
	KeyPEM  string // plain-text PEM (keep in memory only; store encrypted)
}

// NodeCert holds the signed certificate for one node.
type NodeCert struct {
	CertPEM string
	KeyPEM  string
}

// GenerateCA creates a new ECDSA P-256 root CA that is valid for 10 years.
func GenerateCA(commonName string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generate CA key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   commonName,
			Organization: []string{"FeatherDeploy"},
		},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("pki: create CA cert: %w", err)
	}

	certPEM := pemEncodeCert(certDER)
	keyPEM, err := pemEncodeECKey(key)
	if err != nil {
		return nil, err
	}
	return &CA{CertPEM: certPEM, KeyPEM: keyPEM}, nil
}

// SignNodeCert issues a 2-year leaf TLS certificate for a node identified by
// nodeName (used as CN) and optionally bound to nodeIP.
func SignNodeCert(ca *CA, nodeName, nodeIP string) (*NodeCert, error) {
	// Load CA cert + key
	caCert, err := parseCertPEM(ca.CertPEM)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CA cert: %w", err)
	}
	caKey, err := parseECKeyPEM(ca.KeyPEM)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CA key: %w", err)
	}

	// Generate node key
	nodeKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("pki: generate node key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   nodeName,
			Organization: []string{"FeatherDeploy Node"},
		},
		DNSNames:  []string{nodeName},
		NotBefore: time.Now().Add(-time.Minute),
		NotAfter:  time.Now().Add(2 * 365 * 24 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageClientAuth,
			x509.ExtKeyUsageServerAuth,
		},
	}
	if ip := net.ParseIP(nodeIP); ip != nil {
		tmpl.IPAddresses = []net.IP{ip}
	}

	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &nodeKey.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("pki: sign node cert: %w", err)
	}

	certPEM := pemEncodeCert(certDER)
	keyPEM, err := pemEncodeECKey(nodeKey)
	if err != nil {
		return nil, err
	}
	return &NodeCert{CertPEM: certPEM, KeyPEM: keyPEM}, nil
}

// TLSConfig returns a *tls.Config configured for mTLS using the provided
// certPEM/keyPEM and the CA certificate for peer verification.
func TLSConfig(certPEM, keyPEM, caCertPEM string) (*tls.Config, error) {
	cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("pki: key pair: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(caCertPEM)) {
		return nil, fmt.Errorf("pki: could not parse CA cert for pool")
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		RootCAs:      pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

// EncryptKey encrypts a PEM private key using AES-256-GCM with the given
// passphrase (typically the server's JWT secret).
func EncryptKey(keyPEM, passphrase string) (string, error) {
	return crypto.Encrypt(keyPEM, passphrase)
}

// DecryptKey reverses EncryptKey.
func DecryptKey(encrypted, passphrase string) (string, error) {
	return crypto.Decrypt(encrypted, passphrase)
}

// ─── internal helpers ─────────────────────────────────────────────────────────

func randomSerial() (*big.Int, error) {
	max := new(big.Int).Lsh(big.NewInt(1), 128)
	n, err := rand.Int(rand.Reader, max)
	if err != nil {
		return nil, fmt.Errorf("pki: serial: %w", err)
	}
	return n, nil
}

func pemEncodeCert(der []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func pemEncodeECKey(key *ecdsa.PrivateKey) (string, error) {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("pki: marshal EC key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})), nil
}

func parseCertPEM(certPEM string) (*x509.Certificate, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil, fmt.Errorf("pki: no PEM block in cert")
	}
	return x509.ParseCertificate(block.Bytes)
}

func parseECKeyPEM(keyPEM string) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(keyPEM))
	if block == nil {
		return nil, fmt.Errorf("pki: no PEM block in key")
	}
	return x509.ParseECPrivateKey(block.Bytes)
}

