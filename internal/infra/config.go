// -*- coding: utf-8 -*-
// Go 1.25+
//
// config.go
// 配置文件管理，使用 JSON 格式
//
// Lzm 2026-07-09

package infra

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const (
	// DefaultConfigDir 默认配置目录 (~/.agent-bridge/tunnel)
	DefaultConfigDir = ".agent-bridge/tunnel"
	// DefaultConfigFile 默认配置文件名
	DefaultConfigFile = "config.json"
)

// Config 应用配置
type Config struct {
	// Bridge 身份
	BridgeID string `json:"bridge_id,omitempty"`
	Token    string `json:"token,omitempty"`

	// 连接
	ServerURL string `json:"server_url,omitempty"`
	AdminPort int    `json:"admin_port,omitempty"`

	// Agent 配置路径
	ClaudeSettingsFile string `json:"claude_settings_file,omitempty"`

	// 调试
	Debug bool `json:"debug,omitempty"`
}

// DefaultConfig 返回默认配置
func DefaultConfig() *Config {
	return &Config{
		ServerURL: "ws://localhost:9201/ws",
		AdminPort: 9202,
		Debug:     false,
	}
}

// LoadConfig 从标准路径加载配置
// 优先级：环境变量 > 配置文件 > 默认值
func LoadConfig() (*Config, error) {
	cfg := DefaultConfig()

	// 1. 尝试从配置文件加载
	configPath := getConfigPath()
	if data, err := os.ReadFile(configPath); err == nil {
		if err := json.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("解析配置文件 %s 失败: %w", configPath, err)
		}
		fmt.Printf("[Config] 已加载配置文件: %s\n", configPath)
	} else {
		fmt.Printf("[Config] 无配置文件 %s，使用默认配置\n", configPath)
	}

	// 2. 环境变量覆盖（可选）
	if v := os.Getenv("AGENT_BRIDGE_SERVER_URL"); v != "" {
		cfg.ServerURL = v
	}
	if v := os.Getenv("AGENT_BRIDGE_DEBUG"); v == "1" || v == "true" {
		cfg.Debug = true
	}

	return cfg, nil
}

// SaveConfig 保存配置到标准路径
func SaveConfig(cfg *Config) error {
	configPath := getConfigPath()
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("创建配置目录 %s 失败: %w", dir, err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("写入配置文件 %s 失败: %w", configPath, err)
	}
	return nil
}

// getConfigPath 获取配置文件完整路径
func getConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", DefaultConfigFile)
	}
	return filepath.Join(home, DefaultConfigDir, DefaultConfigFile)
}
