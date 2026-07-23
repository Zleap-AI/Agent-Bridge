package server

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

const Version = "0.5.0"

type Config struct {
	ListenAddr   string
	DataDir      string
	DatabasePath string
	PublicURL    string
	TLSCertFile  string
	TLSKeyFile   string
	Version      string
	Console      http.Handler
}

func DefaultConfig() Config {
	dataDir := strings.TrimSpace(os.Getenv("AGENT_BRIDGE_DATA_DIR"))
	if dataDir == "" {
		dataDir = "/var/lib/agent-bridge"
	}
	listen := strings.TrimSpace(os.Getenv("AGENT_BRIDGE_LISTEN_ADDR"))
	if listen == "" {
		listen = "0.0.0.0:9201"
	}
	databasePath := strings.TrimSpace(os.Getenv("AGENT_BRIDGE_DATABASE_PATH"))
	if databasePath == "" {
		databasePath = filepath.Join(dataDir, "agent-bridge.db")
	}
	return Config{
		ListenAddr: listen, DataDir: dataDir, DatabasePath: databasePath,
		PublicURL:   strings.TrimSpace(os.Getenv("AGENT_BRIDGE_PUBLIC_URL")),
		TLSCertFile: strings.TrimSpace(os.Getenv("AGENT_BRIDGE_TLS_CERT_FILE")),
		TLSKeyFile:  strings.TrimSpace(os.Getenv("AGENT_BRIDGE_TLS_KEY_FILE")), Version: Version,
	}
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.ListenAddr) == "" {
		return fmt.Errorf("listen address is required")
	}
	if _, _, err := net.SplitHostPort(c.ListenAddr); err != nil {
		return fmt.Errorf("invalid listen address %q: %w", c.ListenAddr, err)
	}
	if strings.TrimSpace(c.DatabasePath) == "" {
		return fmt.Errorf("database path is required")
	}
	if c.PublicURL != "" {
		parsed, err := url.Parse(c.PublicURL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("public URL must be an absolute http or https URL")
		}
		if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
			return fmt.Errorf("public URL must contain only scheme and host")
		}
	}
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return fmt.Errorf("TLS certificate and key files must be configured together")
	}
	return nil
}
