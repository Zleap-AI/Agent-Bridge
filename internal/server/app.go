package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/server/auth"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/caller"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/device"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/gateway"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/httpapi"
	serverdb "github.com/Zleap-AI/Agent-Bridge/internal/server/sqlite"
)

type App struct {
	config     Config
	store      *serverdb.Store
	auth       *auth.Service
	http       *httpapi.Handler
	setupToken string
}

func New(ctx context.Context, config Config) (*App, error) {
	if config.Version == "" {
		config.Version = Version
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	store, err := serverdb.Open(ctx, config.DatabasePath)
	if err != nil {
		return nil, err
	}
	authService := auth.New(store)
	hub := gateway.New(store)
	deviceService := device.New(store, hub)
	callerService := caller.New(store, hub)
	handler := httpapi.New(authService, deviceService, callerService, hub, httpapi.Config{
		Version: config.Version, PublicURL: config.PublicURL, Console: config.Console,
	})
	app := &App{config: config, store: store, auth: authService, http: handler}
	initialized, err := authService.IsInitialized(ctx)
	if err != nil {
		store.Close()
		return nil, err
	}
	if !initialized {
		app.setupToken, err = authService.PrepareSetupToken(ctx)
		if err != nil {
			store.Close()
			return nil, err
		}
	}
	return app, nil
}

func (a *App) Handler() http.Handler { return a.http }

func (a *App) SetupToken() string { return a.setupToken }

func (a *App) SetupURL() string {
	if a.setupToken == "" {
		return ""
	}
	base := strings.TrimRight(a.config.PublicURL, "/")
	if base == "" {
		host, port, _ := net.SplitHostPort(a.config.ListenAddr)
		if host == "" || host == "0.0.0.0" || host == "::" {
			host = advertisedHost()
		}
		scheme := "http"
		if a.config.TLSCertFile != "" {
			scheme = "https"
		}
		base = scheme + "://" + net.JoinHostPort(host, port)
	}
	return base + "/setup?token=" + url.QueryEscape(a.setupToken)
}

func advertisedHost() string {
	interfaces, err := net.Interfaces()
	if err == nil {
		for _, networkInterface := range interfaces {
			if networkInterface.Flags&net.FlagUp == 0 || networkInterface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addresses, err := networkInterface.Addrs()
			if err != nil {
				continue
			}
			for _, address := range addresses {
				ip, _, err := net.ParseCIDR(address.String())
				if err == nil && ip.IsGlobalUnicast() && ip.To4() != nil {
					return ip.String()
				}
			}
		}
	}
	return "127.0.0.1"
}

func (a *App) ResetPassword(ctx context.Context, password string) error {
	return a.auth.ResetPassword(ctx, password)
}

func (a *App) Serve(listener net.Listener) error {
	server := &http.Server{
		Handler:           a.http,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       75 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	var err error
	if a.config.TLSCertFile != "" {
		err = server.ServeTLS(listener, a.config.TLSCertFile, a.config.TLSKeyFile)
	} else {
		err = server.Serve(listener)
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (a *App) ListenAndServe(ctx context.Context) error {
	listener, err := net.Listen("tcp", a.config.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", a.config.ListenAddr, err)
	}
	server := &http.Server{
		Handler:           a.http,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       75 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = server.Shutdown(shutdownCtx)
		case <-done:
		}
	}()
	if a.config.TLSCertFile != "" {
		err = server.ServeTLS(listener, a.config.TLSCertFile, a.config.TLSKeyFile)
	} else {
		err = server.Serve(listener)
	}
	close(done)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (a *App) Close() error { return a.store.Close() }
