package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const maxPairingResponseSize = 1 << 20

type pairingResult struct {
	BridgeID   string
	Token      string
	ServerURL  string
	DeviceName string
}

type pairingError struct {
	Code    string
	Message string
	Status  int
}

func (e *pairingError) Error() string { return e.Message }

type pairingClaimer interface {
	Claim(ctx context.Context, serverURL, code, hostname string) (pairingResult, error)
}

type pairingClient struct {
	httpClient *http.Client
}

func newPairingClient(client *http.Client) *pairingClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &pairingClient{httpClient: client}
}

func (c *pairingClient) Claim(ctx context.Context, serverURL, code, hostname string) (pairingResult, error) {
	_, parsed, err := normalizeHTTPServerURL(serverURL)
	if err != nil {
		return pairingResult{}, err
	}
	if strings.TrimSpace(code) == "" {
		return pairingResult{}, fmt.Errorf("Pairing Code 不能为空")
	}

	body, err := json.Marshal(map[string]string{
		"code":     strings.TrimSpace(code),
		"hostname": strings.TrimSpace(hostname),
	})
	if err != nil {
		return pairingResult{}, fmt.Errorf("创建配对请求失败: %w", err)
	}

	claimURL := *parsed
	claimURL.Path = strings.TrimRight(claimURL.Path, "/") + "/api/v1/pairings/claim"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, claimURL.String(), bytes.NewReader(body))
	if err != nil {
		return pairingResult{}, fmt.Errorf("创建配对请求失败: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return pairingResult{}, fmt.Errorf("连接 Agent-Bridge Server 失败: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxPairingResponseSize+1))
	if err != nil {
		return pairingResult{}, fmt.Errorf("读取配对响应失败: %w", err)
	}
	if len(responseBody) > maxPairingResponseSize {
		return pairingResult{}, fmt.Errorf("配对响应过大")
	}

	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return pairingResult{}, decodePairingError(response.StatusCode, responseBody)
	}

	var wire struct {
		BridgeID  string `json:"bridge_id"`
		Token     string `json:"token"`
		ServerURL string `json:"server_url"`
		Device    struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"device"`
		Credentials struct {
			BridgeID  string `json:"bridge_id"`
			Token     string `json:"token"`
			ServerURL string `json:"server_url"`
		} `json:"credentials"`
	}
	if err := json.Unmarshal(responseBody, &wire); err != nil {
		return pairingResult{}, fmt.Errorf("解析配对响应失败: %w", err)
	}

	result := pairingResult{
		BridgeID:   firstNonEmpty(wire.Credentials.BridgeID, wire.BridgeID, wire.Device.ID),
		Token:      firstNonEmpty(wire.Credentials.Token, wire.Token),
		ServerURL:  firstNonEmpty(wire.Credentials.ServerURL, wire.ServerURL),
		DeviceName: wire.Device.Name,
	}
	if result.BridgeID == "" || result.Token == "" {
		return pairingResult{}, fmt.Errorf("配对响应缺少 Bridge 凭证")
	}
	if result.ServerURL == "" {
		result.ServerURL = deriveWebSocketURL(parsed)
	}
	if err := validateWebSocketURL(result.ServerURL); err != nil {
		return pairingResult{}, fmt.Errorf("配对响应中的 server_url 无效: %w", err)
	}

	return result, nil
}

func decodePairingError(status int, body []byte) error {
	var wire apiErrorResponse
	if err := json.Unmarshal(body, &wire); err == nil && wire.Error.Code != "" {
		message := wire.Error.Message
		if message == "" {
			message = wire.Error.Code
		}
		return &pairingError{Code: wire.Error.Code, Message: message, Status: status}
	}
	message := strings.TrimSpace(string(body))
	if message == "" {
		message = http.StatusText(status)
	}
	return &pairingError{Code: "PAIRING_FAILED", Message: message, Status: status}
}

func normalizeHTTPServerURL(raw string) (string, *url.URL, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil {
		return "", nil, fmt.Errorf("Server URL 无效: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", nil, fmt.Errorf("Server URL 必须使用 HTTP 或 HTTPS")
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", nil, fmt.Errorf("Server URL 无效")
	}
	if u.Path != "" && u.Path != "/" {
		return "", nil, fmt.Errorf("Server URL 不能包含路径")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u.String(), u, nil
}

func deriveWebSocketURL(server *url.URL) string {
	wsURL := *server
	if wsURL.Scheme == "https" {
		wsURL.Scheme = "wss"
	} else {
		wsURL.Scheme = "ws"
	}
	wsURL.Path = strings.TrimRight(wsURL.Path, "/") + "/ws"
	return wsURL.String()
}

func validateWebSocketURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("必须是完整的 WS 或 WSS URL")
	}
	return nil
}

func ordinaryServerURL(rawWebSocketURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawWebSocketURL))
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
		return ""
	}
	if u.Scheme == "wss" {
		u.Scheme = "https"
	} else {
		u.Scheme = "http"
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/ws")
	return strings.TrimRight(u.String(), "/")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
