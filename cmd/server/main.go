package main

import (
	"bufio"
	"context"
	"embed"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"

	server "github.com/Zleap-AI/Agent-Bridge/internal/server"
	"golang.org/x/term"
)

var version = server.Version

//go:embed html/*
var consoleFiles embed.FS

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "agent-bridge-server:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	command := "serve"
	if len(args) != 0 && !strings.HasPrefix(args[0], "-") {
		command = args[0]
		args = args[1:]
	}
	config, debug, err := parseConfig(command, args)
	if err != nil {
		return err
	}
	level := slog.LevelInfo
	if debug {
		level = slog.LevelDebug
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
	if command == "version" {
		fmt.Println(version)
		return nil
	}
	config.Console = remoteConsoleHandler()

	ctx := context.Background()
	app, err := server.New(ctx, config)
	if err != nil {
		return err
	}
	defer app.Close()

	switch command {
	case "serve":
		if setupURL := app.SetupURL(); setupURL != "" {
			slog.Info("Server requires owner setup", "setup_url", setupURL)
		}
		slog.Info("Agent-Bridge Server starting", "version", version, "listen", config.ListenAddr, "database", config.DatabasePath)
		runCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		return app.ListenAndServe(runCtx)
	case "setup-url":
		if app.SetupURL() == "" {
			return errors.New("server is already initialized")
		}
		fmt.Println(app.SetupURL())
		return nil
	case "reset-password":
		password, err := readNewPassword()
		if err != nil {
			return err
		}
		if err := app.ResetPassword(ctx, password); err != nil {
			return err
		}
		fmt.Println("Owner password reset. Existing Console sessions are now invalid.")
		return nil
	default:
		return fmt.Errorf("unknown command %q (use serve, setup-url, reset-password, or version)", command)
	}
}

func remoteConsoleHandler() http.Handler {
	content, err := fs.Sub(consoleFiles, "html")
	if err != nil {
		panic(err)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") || r.URL.Path == "/ws" || strings.HasPrefix(r.URL.Path, "/docs") || r.URL.Path == "/openapi.json" {
			http.NotFound(w, r)
			return
		}
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if name == "." || name == "" {
			name = "index.html"
		}
		data, readErr := fs.ReadFile(content, name)
		if readErr != nil {
			// Client-side routes use the embedded SPA entry point.
			data, readErr = fs.ReadFile(content, "index.html")
			name = "index.html"
		}
		if readErr != nil {
			http.NotFound(w, r)
			return
		}
		if contentType := mime.TypeByExtension(path.Ext(name)); contentType != "" {
			w.Header().Set("Content-Type", contentType)
		}
		if name == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		}
		_, _ = w.Write(data)
	})
}

func parseConfig(command string, args []string) (server.Config, bool, error) {
	config := server.DefaultConfig()
	databaseFromEnvironment := strings.TrimSpace(os.Getenv("AGENT_BRIDGE_DATABASE_PATH")) != ""
	set := flag.NewFlagSet("agent-bridge-server "+command, flag.ContinueOnError)
	listen := set.String("listen", config.ListenAddr, "HTTP listen address")
	dataDir := set.String("data-dir", config.DataDir, "Server data directory")
	database := set.String("database", config.DatabasePath, "SQLite database path")
	publicURL := set.String("public-url", config.PublicURL, "Public HTTP or HTTPS URL")
	tlsCert := set.String("tls-cert", config.TLSCertFile, "Optional TLS certificate file")
	tlsKey := set.String("tls-key", config.TLSKeyFile, "Optional TLS private key file")
	debug := set.Bool("debug", false, "Enable debug logs")
	if err := set.Parse(args); err != nil {
		return server.Config{}, false, err
	}
	config.ListenAddr = *listen
	config.DataDir = *dataDir
	config.DatabasePath = *database
	if !flagWasSet(set, "database") && !databaseFromEnvironment {
		config.DatabasePath = filepath.Join(config.DataDir, "agent-bridge.db")
	}
	config.PublicURL = *publicURL
	config.TLSCertFile = *tlsCert
	config.TLSKeyFile = *tlsKey
	config.Version = version
	return config, *debug, nil
}

func flagWasSet(set *flag.FlagSet, name string) bool {
	found := false
	set.Visit(func(item *flag.Flag) {
		if item.Name == name {
			found = true
		}
	})
	return found
}

func readNewPassword() (string, error) {
	if password := os.Getenv("AGENT_BRIDGE_NEW_PASSWORD"); password != "" {
		return password, nil
	}
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "New owner password: ")
		first, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		fmt.Fprint(os.Stderr, "Confirm owner password: ")
		second, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		if string(first) != string(second) {
			return "", errors.New("passwords do not match")
		}
		return string(first), nil
	}
	fmt.Fprint(os.Stderr, "New owner password: ")
	value, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(value), nil
}
