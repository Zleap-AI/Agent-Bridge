package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/agent"
	"github.com/Zleap-AI/Agent-Bridge/internal/infra"
	"github.com/Zleap-AI/Agent-Bridge/internal/service"
)

var version = "0.4.0"

func main() {
	debugFlag := flag.Bool("debug", false, "启用调试模式")
	port := flag.Int("port", defaultLocalPort, "Local Console 端口")
	listenHost := flag.String("listen", defaultLocalHost, "Local Console 监听地址")
	background := flag.Bool("background", false, "后台启动，不自动打开 Local Console")
	uninstall := flag.Bool("uninstall", false, "撤销 Windows 用户级自启动")
	showVersion := flag.Bool("version", false, "输出版本号")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}
	prepareBackgroundMode(*background)
	if *uninstall {
		if err := removeUserAutostart(); err != nil {
			fmt.Fprintf(os.Stderr, "撤销自启动失败: %v\n", err)
			os.Exit(1)
		}
		executable, _ := os.Executable()
		fmt.Printf("已撤销 Agent-Bridge Local 自启动。需要时可手动删除 %s\n", executable)
		return
	}

	cfg, err := infra.LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}
	portWasSet := false
	flag.Visit(func(current *flag.Flag) {
		if current.Name == "port" {
			portWasSet = true
		}
	})
	if !portWasSet && cfg.AdminPort > 0 {
		*port = cfg.AdminPort
	}
	debug := *debugFlag || cfg.Debug
	if err := infra.InitLogger(debug); err != nil {
		fmt.Fprintf(os.Stderr, "初始化日志失败: %v\n", err)
		os.Exit(1)
	}
	if err := ensureUserAutostart(); err != nil {
		slog.Warn("注册用户级自启动失败", "error", err)
	}

	listenAddress := localListenAddress(*listenHost, *port)
	slog.Info("Agent-Bridge Local 启动", "version", version, "listen", listenAddress, "debug", debug)
	if err := infra.EnsureAddress(*listenHost, *port); err != nil {
		slog.Error("Local Console 端口不可用", "port", *port, "error", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	registry, err := startLocalAgents(ctx, cfg)
	if err != nil {
		slog.Error("Agent 发现失败", "error", err)
		os.Exit(1)
	}

	// Preserve the existing startup window so Agent status is useful in the
	// first bridge/register message, while keeping Local fully usable offline.
	time.Sleep(2 * time.Second)

	sessions := service.NewSessionManager(registry)
	tunnels := newTunnelManager(registry, sessions)
	if cfg.HasRemoteConnection() {
		tunnels.Switch(tunnelConfigFrom(cfg))
	} else {
		slog.Info("Local 尚未配对，跳过远程连接")
	}

	state := newConfigState(cfg, infra.SaveConfig)
	app := &localApplication{
		version:       version,
		listenAddress: listenAddress,
		registry:      registry,
		sessions:      sessions,
		config:        state,
		pairer:        newPairingClient(&http.Client{Timeout: 15 * time.Second}),
		tunnels:       tunnels,
		hostname:      os.Hostname,
		readLogs:      readRecentLocalLogs,
	}
	server := &http.Server{
		Addr:              listenAddress,
		Handler:           newLocalHandler(app, newLocalConsoleHandler()),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("Local Console 已启动", "url", "http://"+listenAddress)
		serverErr <- server.ListenAndServe()
	}()
	if !*background {
		go func() {
			time.Sleep(250 * time.Millisecond)
			if err := openLocalConsole("http://" + listenAddress); err != nil {
				slog.Debug("自动打开 Local Console 失败", "error", err)
			}
		}()
	}

	select {
	case <-ctx.Done():
		slog.Info("正在关闭 Agent-Bridge Local")
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Local HTTP 服务异常退出", "error", err)
		}
	}

	tunnels.Stop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	shutdownHTTPServer(shutdownCtx, server)
	registry.DisconnectAll(shutdownCtx)
}

func startLocalAgents(ctx context.Context, cfg *infra.Config) (*agent.AgentRegistry, error) {
	registryConfig := agent.DefaultAgentRegistryConfig()
	registryConfig.BridgeID = cfg.BridgeID
	if cfg.ClaudeSettingsFile != "" {
		registryConfig.ClaudeSettingsFile = cfg.ClaudeSettingsFile
	}
	registry := agent.NewAgentRegistry(registryConfig)
	if err := registry.Discover(); err != nil {
		return nil, err
	}
	slog.Info("Agent 发现完成", "count", len(registry.List()))
	for _, availableAgent := range registry.List() {
		go func(localAgent agent.Agent) {
			slog.Info("正在启动 Agent", "id", localAgent.ID())
			if err := localAgent.Start(ctx); err != nil {
				slog.Warn("Agent 启动失败", "id", localAgent.ID(), "error", err)
			}
		}(availableAgent)
	}
	return registry, nil
}
