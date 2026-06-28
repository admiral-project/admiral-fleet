package tlsconfig

import (
	"crypto/tls"
	"fmt"
	"strings"
)

func ClientConfig() *tls.Config {
	return &tls.Config{}
}

func NewClientConfig(s string) (*tls.Config, error) {
	return &tls.Config{}, nil
}

func ValidateURLScheme(u, scheme string) error {
	if !strings.HasPrefix(u, scheme+"://") {
		return fmt.Errorf("invalid scheme")
	}
	return nil
}
