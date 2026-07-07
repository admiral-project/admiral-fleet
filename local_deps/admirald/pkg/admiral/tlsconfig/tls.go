package tlsconfig

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
)

func ClientConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
}

// NewClientConfig creates a TLS config that optionally uses a custom CA
// certificate file. When s is empty, the system CA pool is used.
func NewClientConfig(s string) (*tls.Config, error) {
	cfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}
	if s != "" {
		caCert, err := os.ReadFile(s)
		if err != nil {
			return nil, fmt.Errorf("read CA cert file %q: %w", s, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("CA cert file %q contains no valid PEM certificates", s)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

func ValidateURLScheme(u, scheme string) error {
	if !strings.HasPrefix(u, scheme+"://") {
		return fmt.Errorf("invalid scheme")
	}
	return nil
}
