// -*- coding: utf-8 -*-
// Go 1.25+
//
// local_files.go
// 文件浏览与目录列表处理 — Windows 盘符列举、目录浏览
//
// Lzm 2026-07-22

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Zleap-AI/Agent-Bridge/internal/infra"
)

// browseEntry 目录浏览返回的条目
type browseEntry struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`
}

// handleBrowseDrives 返回 Windows 盘符列表。
// GET /api/v1/local/browse/drives
// Lzm 2026-07-21
func (app *localApplication) handleBrowseDrives(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodGet) {
		return
	}
	drives := []string{}
	for _, letter := range "ABCDEFGHIJKLMNOPQRSTUVWXYZ" {
		path := string(letter) + ":\\"
		if _, err := os.Stat(path); err == nil {
			drives = append(drives, path)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"drives": drives})
}

// handleBrowse 返回指定路径的子目录和文件列表。
// 结果按 目录优先、名称字母序 排序。
// POST /api/v1/local/browse
// 请求：{"path": "D:/project"}
// Lzm 2026-07-21
func (app *localApplication) handleBrowse(w http.ResponseWriter, r *http.Request) {
	if !allowMethod(w, r, http.MethodPost) {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := decodeJSONRequest(r, &req); err != nil {
		writeAPIError(w, http.StatusBadRequest, "INVALID_REQUEST", err.Error())
		return
	}
	if req.Path == "" {
		writeAPIError(w, http.StatusBadRequest, "PATH_REQUIRED", "路径不能为空")
		return
	}

	entries, err := os.ReadDir(req.Path)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "READ_DIR_FAILED", fmt.Sprintf("读取目录失败: %v", err))
		return
	}

	dirs := make([]browseEntry, 0, len(entries))
	for _, e := range entries {
		dirs = append(dirs, browseEntry{
			Name:  e.Name(),
			Path:  filepath.Join(req.Path, e.Name()),
			IsDir: e.IsDir(),
		})
	}

	// 排序：目录优先，再按名称（不区分大小写）
	sort.Slice(dirs, func(i, j int) bool {
		if dirs[i].IsDir != dirs[j].IsDir {
			return dirs[i].IsDir
		}
		return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name)
	})

	writeJSON(w, http.StatusOK, map[string]any{
		"path":    req.Path,
		"entries": dirs,
	})
}

// ---------------------------------------------------------------------------
// 通用辅助函数
// ---------------------------------------------------------------------------

func localListenAddress(host string, port int) string {
	host = strings.TrimSpace(host)
	if host == "" {
		host = defaultLocalHost
	}
	return net.JoinHostPort(host, fmt.Sprintf("%d", port))
}

// isAllowedLocalHost prevents a public hostname that resolves to loopback from
// turning the browser's same-origin policy into access to the Local Console.
func isAllowedLocalHost(listenAddress, requestHost string) bool {
	listenHost, listenPort, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return false
	}
	requestName, requestPort, err := net.SplitHostPort(requestHost)
	if err != nil || requestPort != listenPort {
		return false
	}
	listenHost = strings.Trim(strings.TrimSpace(listenHost), "[]")
	requestName = strings.Trim(strings.TrimSpace(requestName), "[]")
	if requestName == "" {
		return false
	}

	listenIP := net.ParseIP(listenHost)
	requestIP := net.ParseIP(requestName)
	if listenIP != nil && listenIP.IsUnspecified() {
		return requestIP != nil || strings.EqualFold(requestName, "localhost")
	}
	if isLoopbackAddress(listenHost) {
		return isLoopbackAddress(requestName)
	}
	if listenIP != nil {
		return requestIP != nil && listenIP.Equal(requestIP)
	}
	return strings.EqualFold(listenHost, requestName)
}

func isLoopbackAddress(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func requireLocalOrigin(w http.ResponseWriter, r *http.Request) bool {
	if infra.IsAllowedLocalOrigin(r) {
		return true
	}
	writeAPIError(w, http.StatusForbidden, "FORBIDDEN_ORIGIN", "请求来源不允许访问 Local Console")
	return false
}

func allowMethod(w http.ResponseWriter, r *http.Request, method string) bool {
	if r.Method == method {
		return true
	}
	w.Header().Set("Allow", method)
	writeAPIError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "不支持的请求方法")
	return false
}

func decodeJSONRequest(r *http.Request, value any) error {
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])); mediaType != "application/json" {
		return fmt.Errorf("Content-Type 必须是 application/json")
	}
	if r.ContentLength > maxLocalBodySize {
		return fmt.Errorf("请求正文不能超过 %d 字节", maxLocalBodySize)
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxLocalBodySize+1))
	if err != nil {
		return fmt.Errorf("读取请求正文失败: %w", err)
	}
	if len(body) > maxLocalBodySize {
		return fmt.Errorf("请求正文不能超过 %d 字节", maxLocalBodySize)
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("请求 JSON 无效: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("请求只能包含一个 JSON 对象")
	}
	return nil
}

func writeAPIError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiErrorResponse{Error: apiError{Code: code, Message: message}})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Debug("写入 JSON 响应失败", "error", err)
	}
}

func readRecentLocalLogs() []localLog {
	home, err := os.UserHomeDir()
	if err != nil {
		return []localLog{}
	}
	path := filepath.Join(home, infra.LogDir, time.Now().Format("2006-01-02")+".log")
	file, err := os.Open(path)
	if err != nil {
		return []localLog{}
	}
	defer file.Close()

	const limit = 200
	logs := make([]localLog, 0, limit)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		entry := parseLocalLog(scanner.Text())
		if len(logs) == limit {
			copy(logs, logs[1:])
			logs[len(logs)-1] = entry
		} else {
			logs = append(logs, entry)
		}
	}
	return logs
}

func parseLocalLog(line string) localLog {
	var wire struct {
		Time  string `json:"time"`
		Level string `json:"level"`
		Msg   string `json:"msg"`
	}
	if json.Unmarshal([]byte(line), &wire) == nil && wire.Msg != "" {
		return localLog{Timestamp: wire.Time, Level: strings.ToLower(wire.Level), Message: wire.Msg}
	}
	return localLog{Message: line}
}

// truncateString 截断字符串到指定长度（用于日志输出，避免过长）
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func shutdownHTTPServer(ctx context.Context, server *http.Server) {
	if err := server.Shutdown(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("关闭 Local HTTP 服务失败", "error", err)
	}
}
