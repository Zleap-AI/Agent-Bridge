// -*- coding: utf-8 -*-
// Go 1.25+
//
// hermes.go
// Hermes Agent ACP 适配器实现
// 通过 stdio 运行 hermes acp，提供完整的 ACP 代理能力
// 需要 pip install -e '.[acp]' 安装 ACP 附加组件
// 配置：~/.hermes/.env、~/.hermes/config.yaml
//
// Lzm 2026-07-13

package agent

import (
	"context"
)

// HermesAgent Hermes Agent ACP 适配器
type HermesAgent struct {
	*baseAgent
}

// NewHermesAgent 创建 Hermes Agent 实例
func NewHermesAgent(meta AgentMeta) *HermesAgent {
	if meta.DisplayName == "" {
		meta.DisplayName = "Hermes Agent"
	}
	return &HermesAgent{
		baseAgent: newBaseAgent(meta),
	}
}

// Start 启动 Hermes ACP 进程并完成握手
// 进程生命周期由 Stop 管理，ctx 只约束启动与握手
// Lzm 2026-07-13
func (a *HermesAgent) Start(ctx context.Context) error {
	return a.start(ctx, nil)
}

// Send, Stream, NewSession, LoadSession 已提升到 baseAgent 实现
// 见 base.go Send / Stream / NewSession / LoadSession 方法
// Lzm 2026-07-21
