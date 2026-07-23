package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/server/apierror"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/auth"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/caller"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/device"
	"github.com/Zleap-AI/Agent-Bridge/internal/server/gateway"
)

type Config struct {
	Version   string
	PublicURL string
	Console   http.Handler
}

type Handler struct {
	auth    *auth.Service
	devices *device.Service
	caller  *caller.Service
	gateway *gateway.Hub
	config  Config
	handler http.Handler
}

func New(authService *auth.Service, deviceService *device.Service, callerService *caller.Service, hub *gateway.Hub, config Config) *Handler {
	h := &Handler{auth: authService, devices: deviceService, caller: callerService, gateway: hub, config: config}
	h.handler = h.routes()
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.handler.ServeHTTP(w, r)
}

func (h *Handler) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/status", h.status)
	mux.HandleFunc("POST /api/v1/setup", h.setup)
	mux.HandleFunc("POST /api/v1/auth/login", h.login)
	mux.Handle("POST /api/v1/auth/logout", h.ownerOnly(http.HandlerFunc(h.logout)))

	mux.Handle("GET /api/v1/admin/devices", h.ownerOnly(http.HandlerFunc(h.listDevices)))
	mux.Handle("PATCH /api/v1/admin/devices/{id}", h.ownerOnly(http.HandlerFunc(h.renameDevice)))
	mux.Handle("DELETE /api/v1/admin/devices/{id}", h.ownerOnly(http.HandlerFunc(h.deleteDevice)))
	mux.Handle("POST /api/v1/admin/pairing-codes", h.ownerOnly(http.HandlerFunc(h.createPairingCode)))
	mux.Handle("GET /api/v1/admin/api-keys", h.ownerOnly(http.HandlerFunc(h.listAPIKeys)))
	mux.Handle("POST /api/v1/admin/api-keys", h.ownerOnly(http.HandlerFunc(h.createAPIKey)))
	mux.Handle("DELETE /api/v1/admin/api-keys/{id}", h.ownerOnly(http.HandlerFunc(h.deleteAPIKey)))
	mux.Handle("GET /api/v1/admin/calls", h.ownerOnly(http.HandlerFunc(h.calls)))

	mux.HandleFunc("POST /api/v1/pairings/claim", h.claimPairing)
	mux.Handle("GET /ws", h.gateway)

	mux.Handle("GET /api/v1/devices", h.callerOnly(http.HandlerFunc(h.listDevices)))
	mux.Handle("GET /api/v1/devices/{device_id}/agents", h.callerOnly(http.HandlerFunc(h.listAgents)))
	mux.Handle("GET /api/v1/devices/{device_id}/agents/{agent_id}/sessions", h.callerOnly(http.HandlerFunc(h.listSessions)))
	mux.Handle("POST /api/v1/devices/{device_id}/agents/{agent_id}/sessions", h.callerOnly(http.HandlerFunc(h.createSession)))
	mux.Handle("GET /api/v1/devices/{device_id}/agents/{agent_id}/sessions/{session_id}/messages", h.callerOnly(http.HandlerFunc(h.messages)))
	mux.Handle("POST /api/v1/devices/{device_id}/agents/{agent_id}/sessions/{session_id}/messages", h.callerOnly(http.HandlerFunc(h.sendMessage)))

	mux.HandleFunc("GET /openapi.json", h.openapi)
	mux.HandleFunc("GET /docs", h.docs)
	mux.HandleFunc("GET /docs/", h.docs)
	if h.config.Console != nil {
		mux.Handle("/", h.config.Console)
	} else {
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			http.Redirect(w, r, "/docs", http.StatusTemporaryRedirect)
		})
	}
	return securityHeaders(apiBoundary(mux))
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	initialized, err := h.auth.IsInitialized(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok", "initialized": initialized, "version": h.config.Version,
	})
}

func (h *Handler) setup(w http.ResponseWriter, r *http.Request) {
	var request struct {
		SetupToken string `json:"setup_token"`
		Password   string `json:"password"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, err)
		return
	}
	token := bearerToken(r.Header.Get("Authorization"))
	if token == "" {
		token = request.SetupToken
	}
	if err := h.auth.Setup(r.Context(), token, request.Password); err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"status": "ok", "initialized": true})
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Password string `json:"password"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, err)
		return
	}
	session, err := h.auth.Login(r.Context(), request.Password)
	if err != nil {
		writeError(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: auth.OwnerCookieName, Value: session.Token, Path: "/", HttpOnly: true,
		Secure: requestIsHTTPS(r), SameSite: http.SameSiteLaxMode,
		Expires: session.ExpiresAt, MaxAge: int(time.Until(session.ExpiresAt).Seconds()),
	})
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "expires_at": session.ExpiresAt})
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) {
	cookie, _ := r.Cookie(auth.OwnerCookieName)
	if cookie != nil {
		if err := h.auth.Logout(r.Context(), cookie.Value); err != nil {
			writeError(w, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: auth.OwnerCookieName, Value: "", Path: "/", HttpOnly: true,
		Secure: requestIsHTTPS(r), SameSite: http.SameSiteLaxMode, MaxAge: -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) listDevices(w http.ResponseWriter, r *http.Request) {
	items, err := h.caller.ListDevices(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": items})
}

func (h *Handler) renameDevice(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, err)
		return
	}
	item, err := h.devices.Rename(r.Context(), r.PathValue("id"), request.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"device": item})
}

func (h *Handler) deleteDevice(w http.ResponseWriter, r *http.Request) {
	if err := h.devices.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) createPairingCode(w http.ResponseWriter, r *http.Request) {
	item, err := h.devices.CreatePairingCode(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (h *Handler) claimPairing(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Code     string `json:"code"`
		Hostname string `json:"hostname"`
		Name     string `json:"name"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, err)
		return
	}
	name := request.Hostname
	if name == "" {
		name = request.Name
	}
	item, credentials, err := h.devices.Claim(r.Context(), request.Code, name)
	if err != nil {
		writeError(w, err)
		return
	}
	serverURL := h.deviceWebSocketURL(r)
	writeJSON(w, http.StatusCreated, map[string]any{
		"bridge_id":  credentials.BridgeID,
		"token":      credentials.Token,
		"server_url": serverURL,
		"device":     map[string]string{"id": item.BridgeID, "name": item.Name},
		"credentials": map[string]string{
			"bridge_id": credentials.BridgeID, "token": credentials.Token, "server_url": serverURL,
		},
	})
}

func (h *Handler) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	items, err := h.auth.ListAPIKeys(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"api_keys": items})
}

func (h *Handler) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, err)
		return
	}
	item, err := h.auth.CreateAPIKey(r.Context(), request.Name)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"api_key": item})
}

func (h *Handler) deleteAPIKey(w http.ResponseWriter, r *http.Request) {
	if err := h.auth.DeleteAPIKey(r.Context(), r.PathValue("id")); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) calls(w http.ResponseWriter, r *http.Request) {
	items, err := h.caller.Calls(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"calls": items})
}

func (h *Handler) listAgents(w http.ResponseWriter, r *http.Request) {
	items, err := h.caller.Agents(r.Context(), r.PathValue("device_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": items})
}

func (h *Handler) listSessions(w http.ResponseWriter, r *http.Request) {
	raw, err := h.caller.Sessions(r.Context(), r.PathValue("device_id"), r.PathValue("agent_id"))
	if err != nil {
		writeError(w, err)
		return
	}
	writeRawEnvelope(w, http.StatusOK, "sessions", raw)
}

func (h *Handler) createSession(w http.ResponseWriter, r *http.Request) {
	deviceID, agentID := r.PathValue("device_id"), r.PathValue("agent_id")

	// 解析可选的 cwd（工作目录）和 permission_mode（授权模式）参数
	// 请求体为空时视为不传递参数，保持向后兼容
	var request struct {
		CWD            string `json:"cwd"`
		PermissionMode string `json:"permission_mode"`
	}
	if r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&request)
	}

	id, err := h.caller.CreateSession(r.Context(), deviceID, agentID, request.CWD, request.PermissionMode)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"session": map[string]string{
		"id": id, "device_id": deviceID, "agent_id": agentID,
	}})
}

func (h *Handler) messages(w http.ResponseWriter, r *http.Request) {
	cursor, err := queryInteger(r, "cursor", 0, 0, 0)
	if err != nil {
		writeError(w, err)
		return
	}
	limit, err := queryInteger(r, "limit", caller.MaxMessagesPageSize, 1, caller.MaxMessagesPageSize)
	if err != nil {
		writeError(w, err)
		return
	}
	raw, err := h.caller.Messages(r.Context(), r.PathValue("device_id"), r.PathValue("agent_id"), r.PathValue("session_id"), cursor, limit)
	if err != nil {
		writeError(w, err)
		return
	}
	// Local already returns the public messages/total/cursor object.
	writeRaw(w, http.StatusOK, raw)
}

func queryInteger(r *http.Request, name string, defaultValue, minimum, maximum int) (int, error) {
	values, present := r.URL.Query()[name]
	if !present {
		return defaultValue, nil
	}
	if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
		return 0, apierror.New(apierror.CodeInvalidRequest, fmt.Sprintf("Query parameter %s must be one integer", name), http.StatusBadRequest)
	}
	value, err := strconv.Atoi(values[0])
	if err != nil || value < minimum || (maximum > 0 && value > maximum) {
		rangeDescription := fmt.Sprintf("at least %d", minimum)
		if maximum > 0 {
			rangeDescription = fmt.Sprintf("between %d and %d", minimum, maximum)
		}
		return 0, apierror.New(apierror.CodeInvalidRequest, fmt.Sprintf("Query parameter %s must be an integer %s", name, rangeDescription), http.StatusBadRequest)
	}
	return value, nil
}

func (h *Handler) sendMessage(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Content []caller.ContentBlock `json:"content"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(w, err)
		return
	}
	stream, err := h.caller.SendMessage(r.Context(), r.PathValue("device_id"), r.PathValue("agent_id"), r.PathValue("session_id"), request.Content)
	if err != nil {
		writeError(w, err)
		return
	}
	_, ok := w.(http.Flusher)
	if !ok {
		writeError(w, apierror.New(apierror.CodeInternal, "Streaming is not supported by this server", http.StatusInternalServerError))
		return
	}
	controller := http.NewResponseController(w)
	defer stream.Detach()
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
	w.WriteHeader(http.StatusOK)
	if err := controller.Flush(); err != nil {
		return
	}

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	hadStreamError := false
	sentMessage := false
	for {
		select {
		case <-r.Context().Done():
			// The Device operation intentionally continues after the HTTP subscriber leaves.
			return
		case <-keepalive.C:
			_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		case event, open := <-stream.Events():
			_ = controller.SetWriteDeadline(time.Now().Add(30 * time.Second))
			if !open {
				if subscriptionErr := stream.SubscriptionError(); subscriptionErr != nil {
					_ = writeSSE(w, "error", map[string]any{"type": "error", "error": apierror.As(subscriptionErr)})
				} else if !hadStreamError {
					if err := gateway.ResultError(stream.Result()); err != nil {
						_ = writeSSE(w, "error", map[string]any{"type": "error", "error": apierror.As(err)})
					} else {
						_ = writeSSE(w, "done", map[string]string{"type": "done"})
					}
				}
				_ = controller.Flush()
				return
			}
			messageSent, streamError, err := h.writeStreamEvent(w, event, sentMessage)
			if err != nil {
				return
			}
			sentMessage = sentMessage || messageSent
			hadStreamError = hadStreamError || streamError
			if err := controller.Flush(); err != nil {
				return
			}
		}
	}
}

func (h *Handler) writeStreamEvent(w http.ResponseWriter, event gateway.StreamEvent, alreadySentMessage bool) (bool, bool, error) {
	var content struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal(event.Content, &content)
	switch event.Type {
	case "thought":
		return false, false, writeSSE(w, "reasoning.delta", map[string]string{"type": "reasoning.delta", "delta": content.Text})
	case "response":
		if content.Text != "" {
			return true, false, writeSSE(w, "message.delta", map[string]string{"type": "message.delta", "delta": content.Text})
		}
	case "final":
		if content.Text != "" && !alreadySentMessage {
			return true, false, writeSSE(w, "message.delta", map[string]string{"type": "message.delta", "delta": content.Text})
		}
	case "session_refreshed":
		return false, false, writeSSE(w, "session.updated", map[string]string{"type": "session.updated", "session_id": content.Text})
	case "error":
		return false, true, writeSSE(w, "error", map[string]any{
			"type": "error", "error": apierror.New(apierror.CodeAgentUnavailable, content.Text, http.StatusConflict),
		})
	}
	return false, false, nil
}

func (h *Handler) ownerOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, _ := r.Cookie(auth.OwnerCookieName)
		if cookie != nil {
			valid, err := h.auth.AuthenticateOwner(r.Context(), cookie.Value)
			if err != nil {
				writeError(w, err)
				return
			}
			if valid {
				if err := h.validateOwnerRequestOrigin(r); err != nil {
					writeError(w, err)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
		}
		if key := bearerToken(r.Header.Get("Authorization")); key != "" {
			_, valid, err := h.auth.AuthenticateAPIKey(r.Context(), key)
			if err != nil {
				writeError(w, err)
				return
			}
			if valid {
				writeError(w, apierror.New(apierror.CodeForbidden, "API keys cannot access administration endpoints", http.StatusForbidden))
				return
			}
		}
		writeError(w, apierror.New(apierror.CodeUnauthorized, "Owner authentication is required", http.StatusUnauthorized))
	})
}

func (h *Handler) callerOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := bearerToken(r.Header.Get("Authorization"))
		if key != "" {
			_, valid, err := h.auth.AuthenticateAPIKey(r.Context(), key)
			if err != nil {
				writeError(w, err)
				return
			}
			if valid {
				next.ServeHTTP(w, r)
				return
			}
		}
		if cookie, _ := r.Cookie(auth.OwnerCookieName); cookie != nil {
			valid, err := h.auth.AuthenticateOwner(r.Context(), cookie.Value)
			if err != nil {
				writeError(w, err)
				return
			}
			if valid {
				if err := h.validateOwnerRequestOrigin(r); err != nil {
					writeError(w, err)
					return
				}
				next.ServeHTTP(w, r)
				return
			}
		}
		writeError(w, apierror.New(apierror.CodeUnauthorized, "A valid Owner session or API key is required", http.StatusUnauthorized))
	})
}

func (h *Handler) validateOwnerRequestOrigin(r *http.Request) error {
	if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
		return nil
	}
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		if strings.EqualFold(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")), "cross-site") {
			return apierror.New(apierror.CodeForbidden, "Cross-origin Owner requests are not allowed", http.StatusForbidden)
		}
		return nil
	}
	actual, ok := canonicalOrigin(origin, true)
	if !ok {
		return apierror.New(apierror.CodeForbidden, "Request Origin is invalid", http.StatusForbidden)
	}
	expectedURL := strings.TrimSpace(h.config.PublicURL)
	if expectedURL == "" {
		scheme := "http"
		if requestIsHTTPS(r) {
			scheme = "https"
		}
		expectedURL = scheme + "://" + r.Host
	}
	expected, ok := canonicalOrigin(expectedURL, false)
	if !ok || actual != expected {
		return apierror.New(apierror.CodeForbidden, "Cross-origin Owner requests are not allowed", http.StatusForbidden)
	}
	return nil
}

func canonicalOrigin(raw string, requireOriginForm bool) (string, bool) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", false
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", false
	}
	if requireOriginForm && parsed.Path != "" {
		return "", false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", false
	}
	port := parsed.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return scheme + "://" + host, true
}

func (h *Handler) deviceWebSocketURL(r *http.Request) string {
	base := strings.TrimSpace(h.config.PublicURL)
	if base == "" {
		scheme := "http"
		if requestIsHTTPS(r) {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" {
		return base
	}
	if parsed.Scheme == "https" || parsed.Scheme == "wss" {
		parsed.Scheme = "wss"
	} else {
		parsed.Scheme = "ws"
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/ws"
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}

func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]), "https")
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	const maxBodyBytes int64 = 1024 * 1024
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(15 * time.Second))
	defer controller.SetReadDeadline(time.Time{})
	if r.ContentLength > maxBodyBytes {
		return requestBodyTooLargeError(maxBodyBytes)
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return requestBodyTooLargeError(maxBodyBytes)
		}
		return apierror.Wrap(apierror.CodeInvalidRequest, "Request body must be valid JSON", http.StatusBadRequest, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return requestBodyTooLargeError(maxBodyBytes)
		}
		return apierror.New(apierror.CodeInvalidRequest, "Request body must contain one JSON value", http.StatusBadRequest)
	}
	return nil
}

func requestBodyTooLargeError(limit int64) error {
	return apierror.New(apierror.CodePayloadTooLarge, fmt.Sprintf("Request body must not exceed %d bytes", limit), http.StatusRequestEntityTooLarge)
}

func writeError(w http.ResponseWriter, err error) {
	apiErr := apierror.As(err)
	if apiErr.Status == 0 {
		apiErr.Status = http.StatusInternalServerError
	}
	if apiErr.Status == http.StatusTooManyRequests {
		w.Header().Set("Retry-After", "1")
	}
	writeJSON(w, apiErr.Status, map[string]any{"error": apiErr})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeRaw(w http.ResponseWriter, status int, raw json.RawMessage) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if len(raw) == 0 {
		_, _ = w.Write([]byte("{}\n"))
		return
	}
	_, _ = w.Write(raw)
	_, _ = w.Write([]byte("\n"))
}

func writeRawEnvelope(w http.ResponseWriter, status int, key string, raw json.RawMessage) {
	if len(raw) == 0 {
		raw = json.RawMessage(`[]`)
	}
	writeJSON(w, status, map[string]json.RawMessage{key: raw})
}

func writeSSE(w io.Writer, event string, value any) error {
	data, _ := json.Marshal(value)
	_, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
	return err
}

func bearerToken(value string) string {
	parts := strings.Fields(value)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return ""
	}
	return parts[1]
}

func apiBoundary(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAPIV1Path(r.URL.Path) {
			mux.ServeHTTP(w, r)
			return
		}
		if _, pattern := mux.Handler(r); isAPIV1Pattern(pattern) {
			mux.ServeHTTP(w, r)
			return
		}

		allowed := allowedAPIMethods(mux, r)
		if len(allowed) > 0 {
			w.Header().Set("Allow", strings.Join(allowed, ", "))
			writeError(w, apierror.New(apierror.CodeMethodNotAllowed, "Method is not allowed for this API route", http.StatusMethodNotAllowed))
			return
		}
		writeError(w, apierror.New(apierror.CodeNotFound, "API route was not found", http.StatusNotFound))
	})
}

func isAPIV1Path(path string) bool {
	return path == "/api/v1" || strings.HasPrefix(path, "/api/v1/")
}

func isAPIV1Pattern(pattern string) bool {
	_, path, found := strings.Cut(pattern, " ")
	return found && isAPIV1Path(path)
}

func allowedAPIMethods(mux *http.ServeMux, r *http.Request) []string {
	methods := []string{http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions}
	allowed := make([]string, 0, 2)
	seen := make(map[string]struct{})
	for _, method := range methods {
		probe := r.Clone(r.Context())
		probe.Method = method
		_, pattern := mux.Handler(probe)
		if !isAPIV1Pattern(pattern) {
			continue
		}
		registeredMethod, _, _ := strings.Cut(pattern, " ")
		if _, exists := seen[registeredMethod]; !exists {
			seen[registeredMethod] = struct{}{}
			allowed = append(allowed, registeredMethod)
		}
		if registeredMethod == http.MethodGet {
			if _, exists := seen[http.MethodHead]; !exists {
				seen[http.MethodHead] = struct{}{}
				allowed = append(allowed, http.MethodHead)
			}
		}
	}
	return allowed
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
