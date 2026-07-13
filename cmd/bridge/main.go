// -*- coding: utf-8 -*-
// Go 1.26+
//
// main.go
// zleap-bridge 入口点 — Phase 2: 核心链路
// Agent 发现 → ACP 握手 → WebSocket Tunnel 服务
//
// Lzm 2026-07-09

package main

import (
	"context"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/zleap/bridge/internal/agent"
	"github.com/zleap/bridge/internal/infra"
	"github.com/zleap/bridge/internal/protocol"
	"github.com/zleap/bridge/internal/service"
)

//go:embed html/*.html
var htmlFS embed.FS

var version = "0.3.0"

func main() {
	// 命令行参数
	debug := flag.Bool("debug", false, "启用调试模式")
	port := flag.Int("port", 9202, "WebSocket Admin 端口")
	flag.Parse()

	// 初始化日志
	if err := infra.InitLogger(*debug); err != nil {
		fmt.Fprintf(os.Stderr, "初始化日志失败: %v\n", err)
		os.Exit(1)
	}
	slog.Info("zleap-bridge 启动",
		"version", version,
		"debug", *debug,
		"port", *port,
	)

	// 加载配置
	cfg, err := infra.LoadConfig()
	if err != nil {
		slog.Error("加载配置失败", "error", err)
		os.Exit(1)
	}
	slog.Info("配置加载完成",
		"server_url", cfg.ServerURL,
		"bridge_id", cfg.BridgeID,
	)

	// --- Phase 2 主流程 ---

	// 1. 扫描系统 Agent
	registryCfg := agent.DefaultAgentRegistryConfig()
	registryCfg.BridgeID = cfg.BridgeID
	if cfg.ClaudeSettingsFile != "" {
		registryCfg.ClaudeSettingsFile = cfg.ClaudeSettingsFile
	}

	reg := agent.NewAgentRegistry(registryCfg)
	if err := reg.Discover(); err != nil {
		slog.Error("Agent 发现失败", "error", err)
		os.Exit(1)
	}
	slog.Info("Agent 发现完成", "count", len(reg.List()))

	// 2. 启动所有 Agent（后台 goroutine 自动重连）
	ctx := context.Background()
	for _, a := range reg.List() {
		go func(ag agent.Agent) {
			slog.Info("正在启动 Agent", "id", ag.ID())
			if err := ag.Start(ctx); err != nil {
				slog.Error("Agent 启动失败", "id", ag.ID(), "error", err)
				// Phase 3: 加入重试逻辑
			}
		}(a)
	}

	// 等待 Agent 启动完成
	time.Sleep(2 * time.Second)

	// 3. 启动 TunnelService（连接 SaaS）
	tunnelCfg := service.DefaultTunnelConfig()
	tunnelCfg.ServerURL = cfg.ServerURL
	tunnelCfg.BridgeID = cfg.BridgeID
	tunnelCfg.Token = cfg.Token

	tunnel := service.NewTunnelService(reg, tunnelCfg)
	if err := tunnel.Start(); err != nil {
		slog.Warn("TunnelService 启动失败（SaaS 可能未就绪）",
			"error", err,
		)
		// 不阻塞启动，Admin 接口仍然可用
	} else {
		slog.Info("TunnelService 已启动")
	}

	// 4. 确保端口可用 — 自动清理旧 bridge 进程（单例保障）
	if err := infra.EnsurePort(*port); err != nil {
		slog.Error("端口不可用", "port", *port, "error", err)
		os.Exit(1)
	}

	// 5. 启动 Admin HTTP 服务（健康检查 + WebSocket 管理）
	startAdminServer(*port, reg, cfg)
}

// startAdminServer 启动 Admin HTTP 服务
// 提供健康检查、Agent 状态查询、WebSocket 管理接口
// Lzm 2026-07-10
func startAdminServer(port int, reg *agent.AgentRegistry, cfg *infra.Config) {
	wsServer := infra.NewWSServer()

	// 创建会话管理器和路由器（用于 Admin WS 的 invoke/sessions/list 路由）
	sessionMgr := service.NewSessionManager(reg)
	ctx := context.Background()

	// 测试界面 — 从嵌入的 HTML 文件系统服务
	htmlContent := func(name string) func(w http.ResponseWriter, r *http.Request) {
		return func(w http.ResponseWriter, r *http.Request) {
			data, err := htmlFS.ReadFile("html/" + name)
			if err != nil {
				http.Error(w, "Not Found", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(data)
		}
	}
	http.HandleFunc("/test_acp.html", htmlContent("test_acp.html"))
	http.HandleFunc("/test_saas.html", htmlContent("test_saas.html"))
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/test_acp.html", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})

	// 健康检查接口
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// 收集各 Agent 状态
		agentStatus := make(map[string]string)
		for _, a := range reg.List() {
			agentStatus[a.ID()] = a.Status().String()
		}

		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "ok",
			"version": version,
			"agents":  agentStatus,
		})
	})

	// Agent 状态接口
	http.HandleFunc("/agents", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(reg.ListDescriptors())
	})

	// REST API 接口（统一前缀，避免 ServeMux 模式匹配问题）
	// GET /api/sessions?agent_id=kimi&limit=20
	// GET /api/messages?agent_id=kimi&session_id=xxx&limit=50
	http.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case strings.HasPrefix(r.URL.Path, "/api/sessions"):
			agentID := r.URL.Query().Get("agent_id")
			if agentID != "" {
				sessions := sessionMgr.ListSessions(agentID, 20)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"status":   "ok",
					"agent_id": agentID,
					"sessions": sessions,
				})
			} else {
				all := sessionMgr.ListAllSessions()
				json.NewEncoder(w).Encode(map[string]interface{}{
					"status": "ok",
					"agents": all,
				})
			}

		case strings.HasPrefix(r.URL.Path, "/api/messages"):
			agentID := r.URL.Query().Get("agent_id")
			sessionID := r.URL.Query().Get("session_id")
			if agentID == "" || sessionID == "" {
				json.NewEncoder(w).Encode(map[string]string{
					"status":  "error",
					"message": "缺少 agent_id 或 session_id",
				})
				return
			}
			messages := sessionMgr.LoadMessages(agentID, sessionID)
			if messages == nil {
				messages = []service.StoredMessage{}
			}
			json.NewEncoder(w).Encode(map[string]interface{}{
				"status":   "ok",
				"agent_id": agentID,
				"session":  sessionID,
				"messages": messages,
				"count":    len(messages),
			})

		default:
			http.NotFound(w, r)
		}
	})

	// WebSocket Admin 接口
	http.HandleFunc("/ws/admin", func(w http.ResponseWriter, r *http.Request) {
		conn, err := wsServer.Upgrade(w, r)
		if err != nil {
			slog.Error("升级 WebSocket 失败", "error", err)
			return
		}
		defer conn.Close()
		slog.Info("Admin 客户端已连接")

		// 为每个 Admin 连接创建独立的路由器（需要将流式结果写回当前 WS 连接）
		adminRouter := service.NewRequestRouter(reg)
		adminRouter.SetStreamCallback(func(requestID string, chunkType string, text string) error {
			// 流式块推送到 Admin WebSocket 客户端
			update := protocol.NewStreamUpdate(requestID, chunkType, text)
			return infra.WriteJSON(conn, update)
		})

		// 发送 bridge/list 欢迎通知（兼容 test_saas.html 等前端）
		agentDescs := reg.ListDescriptors()
		welcome := map[string]interface{}{
			"method": "bridge/list",
			"params": map[string]interface{}{
				"bridges": []map[string]interface{}{
					{
						"bridge_id": cfg.BridgeID,
						"connected": true,
						"agents":    agentDescs,
					},
				},
			},
		}
		infra.WriteJSON(conn, welcome)

		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				if !websocket.IsCloseError(err, websocket.CloseNormalClosure,
					websocket.CloseGoingAway) {
					slog.Error("Admin 连接读取失败", "error", err)
				}
				break
			}

			// 解析消息
			var anpMsg protocol.ANPMessage
			if err := json.Unmarshal(msg, &anpMsg); err != nil {
				slog.Warn("Admin 消息格式错误", "error", err)
				continue
			}

			slog.Debug("收到 Admin 消息",
				"method", anpMsg.Method,
				"id", anpMsg.ID,
			)

			// 处理 ping
			if anpMsg.Method == "ping" {
				pong := protocol.NewResultResponse(anpMsg.ID,
					json.RawMessage(`"pong"`))
				infra.WriteJSON(conn, pong)
				continue
			}

			// 处理 admin/agents 列表
			if anpMsg.Method == "admin/agents" {
				result, _ := json.Marshal(reg.ListDescriptors())
				resp := protocol.NewResultResponse(anpMsg.ID, result)
				infra.WriteJSON(conn, resp)
				continue
			}

			// 处理 bridge/health — 返回 Agent 状态
			if anpMsg.Method == "bridge/health" {
				descs := reg.ListDescriptors()
				result, _ := json.Marshal(descs)
				resp := protocol.NewResultResponse(anpMsg.ID, result)
				infra.WriteJSON(conn, resp)
				continue
			}

			// 处理 bridge/list — 返回 Bridge+Agent 列表（兼容 test_saas.html）
			if anpMsg.Method == "bridge/list" {
				descs := reg.ListDescriptors()
				result, _ := json.Marshal(map[string]interface{}{
					"bridges": []map[string]interface{}{
						{
							"bridge_id": cfg.BridgeID,
							"connected": true,
							"agents":    descs,
						},
					},
				})
				resp := protocol.NewResultResponse(anpMsg.ID, result)
				infra.WriteJSON(conn, resp)
				continue
			}

			// 处理 sessions/list — 列出历史会话
			if anpMsg.Method == "sessions/list" {
				response := adminRouter.Route(ctx, &anpMsg, sessionMgr)
				if response != nil {
					infra.WriteJSON(conn, response)
				}
				continue
			}

			// 处理 sessions/messages — 获取会话消息
			if anpMsg.Method == "sessions/messages" {
				response := adminRouter.HandleSessionMessages(&anpMsg, sessionMgr)
				if response != nil {
					infra.WriteJSON(conn, response)
				}
				continue
			}

			// 处理 invoke — 通过 RequestRouter 转发到 Agent
			if anpMsg.Method == "invoke" {
				response := adminRouter.Route(ctx, &anpMsg, sessionMgr)
				if response != nil {
					infra.WriteJSON(conn, response)
				}
				continue
			}

			slog.Warn("未知 Admin 方法",
				"method", anpMsg.Method,
				"id", anpMsg.ID,
			)
		}
	})

	addr := fmt.Sprintf(":%d", port)
	slog.Info("启动 Admin HTTP 服务", "addr", addr)

	// 优雅关闭
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt)
		<-sigCh
		slog.Info("收到中断信号，正在关闭...")
		os.Exit(0)
	}()

	if err := http.ListenAndServe(addr, nil); err != nil {
		slog.Error("HTTP 服务异常退出", "error", err)
	}
}
