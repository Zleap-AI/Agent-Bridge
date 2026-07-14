// -*- coding: utf-8 -*-
// Go 1.25+
//
// anp.go
// ANP (Agent Network Protocol) 消息类型
// 远程服务与 Bridge 之间通过 WebSocket 使用 ANP 协议通信
//
// Lzm 2026-07-09

package protocol

import "encoding/json"

// ANPMessage 远程服务与 Bridge 之间的 WebSocket 消息
type ANPMessage struct {
	JSONRPC string          `json:"jsonrpc,omitempty"`
	ID      string          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *ANPError       `json:"error,omitempty"`
}

// ANPError ANP 错误结构
type ANPError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ANPInvokeParams invoke 请求参数
type ANPInvokeParams struct {
	AgentID string          `json:"agent_id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
	Stream  bool            `json:"stream,omitempty"`
}

// ANPBridgeRegister Bridge 注册消息参数
type ANPBridgeRegister struct {
	BridgeID string     `json:"bridge_id"`
	Agents   []ANPAgent `json:"agents"`
}

// ANPAgent Bridge 注册的 Agent 信息
type ANPAgent struct {
	AgentID     string `json:"agent_id"`
	DisplayName string `json:"display_name,omitempty"`
	Status      string `json:"status,omitempty"`
}

// ANPStreamUpdate 流式推送消息参数
type ANPStreamUpdate struct {
	RequestID string          `json:"request_id"`
	Type      string          `json:"type"`
	Content   json.RawMessage `json:"content"`
}

// NewInvokeRequest 创建 invoke 请求消息
func NewInvokeRequest(id, agentID, method string, params json.RawMessage) *ANPMessage {
	invokeParams, _ := json.Marshal(ANPInvokeParams{
		AgentID: agentID,
		Method:  method,
		Params:  params,
	})
	return &ANPMessage{
		JSONRPC: "2.0",
		ID:      id,
		Method:  "invoke",
		Params:  invokeParams,
	}
}

// NewStreamUpdate 创建流式推送消息
func NewStreamUpdate(requestID, chunkType, text string) *ANPMessage {
	content, _ := json.Marshal(map[string]string{"text": text})
	params, _ := json.Marshal(ANPStreamUpdate{
		RequestID: requestID,
		Type:      chunkType,
		Content:   content,
	})
	return &ANPMessage{
		JSONRPC: "2.0",
		Method:  "session/update",
		Params:  params,
	}
}

// NewResultResponse 创建成功响应消息
func NewResultResponse(id string, result json.RawMessage) *ANPMessage {
	return &ANPMessage{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	}
}

// NewErrorResponse 创建错误响应消息
func NewErrorResponse(id string, code int, message string) *ANPMessage {
	return &ANPMessage{
		JSONRPC: "2.0",
		ID:      id,
		Error: &ANPError{
			Code:    code,
			Message: message,
		},
	}
}
