package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
)

const openAPIVersionPlaceholder = "__AGENT_BRIDGE_VERSION__"

func (h *Handler) openapi(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	version, _ := json.Marshal(h.config.Version)
	spec := strings.Replace(openAPISpec, `"`+openAPIVersionPlaceholder+`"`, string(version), 1)
	_, _ = w.Write([]byte(spec))
}

func (h *Handler) docs(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(docsHTML))
}

const openAPISpec = `{
  "openapi": "3.1.0",
  "info": {
    "title": "Agent-Bridge Server API",
    "version": "__AGENT_BRIDGE_VERSION__",
    "description": "Set up and administer Agent-Bridge Server, pair Devices, and call AI Agents on paired Devices. Conversation content remains on each Device."
  },
  "servers": [{"url": "/"}],
  "tags": [
    {"name": "Server", "description": "Server setup and Owner authentication"},
    {"name": "Administration", "description": "Owner-only Device, pairing, API key, and call metadata management"},
    {"name": "Pairing", "description": "One-time Device credential exchange"},
    {"name": "Caller", "description": "Owner Session or Caller API key access to remote Agents"}
  ],
  "paths": {
    "/api/v1/status": {
      "get": {
        "tags": ["Server"],
        "operationId": "getServerStatus",
        "summary": "Get Server status",
        "security": [],
        "responses": {
          "200": {
            "description": "Current health, setup state, and version",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/StatusResponse"}}}
          },
          "default": {"$ref": "#/components/responses/ErrorResponse"}
        }
      }
    },
    "/api/v1/setup": {
      "post": {
        "tags": ["Server"],
        "operationId": "setupServer",
        "summary": "Set the initial Owner password",
        "description": "Requires the one-time Setup Token. Send it as Authorization: Bearer <setup_token> or in setup_token in the JSON body. The token is invalidated after successful setup.",
        "security": [],
        "parameters": [{"$ref": "#/components/parameters/SetupTokenAuthorization"}],
        "requestBody": {
          "required": true,
          "content": {"application/json": {"schema": {"$ref": "#/components/schemas/SetupRequest"}}}
        },
        "responses": {
          "201": {
            "description": "Server initialized",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/SetupResponse"}}}
          },
          "default": {"$ref": "#/components/responses/ErrorResponse"}
        }
      }
    },
    "/api/v1/auth/login": {
      "post": {
        "tags": ["Server"],
        "operationId": "loginOwner",
        "summary": "Create an Owner Session",
        "description": "Authenticates with the Owner password and sets a 30-day HttpOnly Owner Session cookie.",
        "security": [],
        "requestBody": {
          "required": true,
          "content": {"application/json": {"schema": {"$ref": "#/components/schemas/LoginRequest"}}}
        },
        "responses": {
          "200": {
            "description": "Login succeeded",
            "headers": {
              "Set-Cookie": {"description": "Owner Session cookie", "schema": {"type": "string"}}
            },
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/LoginResponse"}}}
          },
          "default": {"$ref": "#/components/responses/ErrorResponse"}
        }
      }
    },
    "/api/v1/auth/logout": {
      "post": {
        "tags": ["Server"],
        "operationId": "logoutOwner",
        "summary": "End the current Owner Session",
        "security": [{"ownerSession": []}],
        "responses": {
          "204": {"description": "Logged out"},
          "default": {"$ref": "#/components/responses/ErrorResponse"}
        }
      }
    },
    "/api/v1/admin/devices": {
      "get": {
        "tags": ["Administration"],
        "operationId": "listAdminDevices",
        "summary": "List managed Devices",
        "security": [{"ownerSession": []}],
        "responses": {
          "200": {
            "description": "Devices with current online and Agent counts",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/DevicesResponse"}}}
          },
          "default": {"$ref": "#/components/responses/ErrorResponse"}
        }
      }
    },
    "/api/v1/admin/devices/{id}": {
      "patch": {
        "tags": ["Administration"],
        "operationId": "renameDevice",
        "summary": "Rename a Device",
        "security": [{"ownerSession": []}],
        "parameters": [{"$ref": "#/components/parameters/AdminResourceId"}],
        "requestBody": {
          "required": true,
          "content": {"application/json": {"schema": {"$ref": "#/components/schemas/RenameDeviceRequest"}}}
        },
        "responses": {
          "200": {
            "description": "Renamed Device",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/StoredDeviceResponse"}}}
          },
          "default": {"$ref": "#/components/responses/ErrorResponse"}
        }
      },
      "delete": {
        "tags": ["Administration"],
        "operationId": "deleteDevice",
        "summary": "Revoke and delete a Device",
        "description": "Immediately revokes the Device credential and closes its active connection.",
        "security": [{"ownerSession": []}],
        "parameters": [{"$ref": "#/components/parameters/AdminResourceId"}],
        "responses": {
          "204": {"description": "Device deleted"},
          "default": {"$ref": "#/components/responses/ErrorResponse"}
        }
      }
    },
    "/api/v1/admin/pairing-codes": {
      "post": {
        "tags": ["Administration"],
        "operationId": "createPairingCode",
        "summary": "Create a one-time Pairing Code",
        "description": "Replaces any previously unconsumed Pairing Code and expires after 10 minutes.",
        "security": [{"ownerSession": []}],
        "responses": {
          "201": {
            "description": "Pairing Code created",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/PairingCode"}}}
          },
          "default": {"$ref": "#/components/responses/ErrorResponse"}
        }
      }
    },
    "/api/v1/admin/api-keys": {
      "get": {
        "tags": ["Administration"],
        "operationId": "listApiKeys",
        "summary": "List Caller API keys",
        "description": "Returns metadata only; API key plaintext is never returned after creation.",
        "security": [{"ownerSession": []}],
        "responses": {
          "200": {
            "description": "API key metadata",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ApiKeysResponse"}}}
          },
          "default": {"$ref": "#/components/responses/ErrorResponse"}
        }
      },
      "post": {
        "tags": ["Administration"],
        "operationId": "createApiKey",
        "summary": "Create a Caller API key",
        "description": "The complete key is returned exactly once.",
        "security": [{"ownerSession": []}],
        "requestBody": {
          "required": true,
          "content": {"application/json": {"schema": {"$ref": "#/components/schemas/CreateApiKeyRequest"}}}
        },
        "responses": {
          "201": {
            "description": "API key created",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/CreatedApiKeyResponse"}}}
          },
          "default": {"$ref": "#/components/responses/ErrorResponse"}
        }
      }
    },
    "/api/v1/admin/api-keys/{id}": {
      "delete": {
        "tags": ["Administration"],
        "operationId": "deleteApiKey",
        "summary": "Revoke a Caller API key",
        "security": [{"ownerSession": []}],
        "parameters": [{"$ref": "#/components/parameters/AdminResourceId"}],
        "responses": {
          "204": {"description": "API key revoked"},
          "default": {"$ref": "#/components/responses/ErrorResponse"}
        }
      }
    },
    "/api/v1/admin/calls": {
      "get": {
        "tags": ["Administration"],
        "operationId": "listCalls",
        "summary": "List recent call metadata",
        "description": "Returns at most 1000 records. Message and response content is never stored in call metadata.",
        "security": [{"ownerSession": []}],
        "responses": {
          "200": {
            "description": "Recent call metadata",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/CallsResponse"}}}
          },
          "default": {"$ref": "#/components/responses/ErrorResponse"}
        }
      }
    },
    "/api/v1/pairings/claim": {
      "post": {
        "tags": ["Pairing"],
        "operationId": "claimPairingCode",
        "summary": "Exchange a Pairing Code for Device credentials",
        "description": "The one-time code is the credential for this request. The returned Bridge token is shown once and must be stored by Agent-Bridge Local.",
        "security": [],
        "requestBody": {
          "required": true,
          "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ClaimPairingRequest"}}}
        },
        "responses": {
          "201": {
            "description": "Pairing claimed and Device credentials issued",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ClaimPairingResponse"}}}
          },
          "default": {"$ref": "#/components/responses/ErrorResponse"}
        }
      }
    },
    "/api/v1/devices": {
      "get": {
        "tags": ["Caller"],
        "operationId": "listDevices",
        "summary": "List Devices",
        "security": [{"ownerSession": []}, {"apiKey": []}],
        "responses": {
          "200": {
            "description": "Devices with current online and Agent counts",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/DevicesResponse"}}}
          },
          "default": {"$ref": "#/components/responses/ErrorResponse"}
        }
      }
    },
    "/api/v1/devices/{device_id}/agents": {
      "get": {
        "tags": ["Caller"],
        "operationId": "listDeviceAgents",
        "summary": "List a Device's Agents",
        "security": [{"ownerSession": []}, {"apiKey": []}],
        "parameters": [{"$ref": "#/components/parameters/DeviceId"}],
        "responses": {
          "200": {
            "description": "Last registered Agent list",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/AgentsResponse"}}}
          },
          "default": {"$ref": "#/components/responses/ErrorResponse"}
        }
      }
    },
    "/api/v1/devices/{device_id}/agents/{agent_id}/sessions": {
      "get": {
        "tags": ["Caller"],
        "operationId": "listAgentSessions",
        "summary": "List an Agent's Sessions",
        "description": "The Device must be online. Session content is read from the Device and is not stored on Server.",
        "security": [{"ownerSession": []}, {"apiKey": []}],
        "parameters": [
          {"$ref": "#/components/parameters/DeviceId"},
          {"$ref": "#/components/parameters/AgentId"}
        ],
        "responses": {
          "200": {
            "description": "Sessions",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/SessionsResponse"}}}
          },
          "default": {"$ref": "#/components/responses/ErrorResponse"}
        }
      },
      "post": {
        "tags": ["Caller"],
        "operationId": "createAgentSession",
        "summary": "Create a new Session",
        "description": "The Device must be online. No request body is required.",
        "security": [{"ownerSession": []}, {"apiKey": []}],
        "parameters": [
          {"$ref": "#/components/parameters/DeviceId"},
          {"$ref": "#/components/parameters/AgentId"}
        ],
        "responses": {
          "201": {
            "description": "Session created",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/CreateSessionResponse"}}}
          },
          "default": {"$ref": "#/components/responses/ErrorResponse"}
        }
      }
    },
    "/api/v1/devices/{device_id}/agents/{agent_id}/sessions/{session_id}/messages": {
      "get": {
        "tags": ["Caller"],
        "operationId": "listSessionMessages",
        "summary": "Read a Session's local Message history",
        "description": "The Device must be online. Messages are returned directly from Device storage in cursor pages. Use the returned cursor for the next request until it equals total.",
        "security": [{"ownerSession": []}, {"apiKey": []}],
        "parameters": [
          {"$ref": "#/components/parameters/DeviceId"},
          {"$ref": "#/components/parameters/AgentId"},
          {"$ref": "#/components/parameters/SessionId"},
          {
            "name": "cursor",
            "in": "query",
            "description": "Zero-based offset of the first Message to return.",
            "schema": {"type": "integer", "minimum": 0, "default": 0}
          },
          {
            "name": "limit",
            "in": "query",
            "description": "Maximum number of Messages in this page.",
            "schema": {"type": "integer", "minimum": 1, "maximum": 100, "default": 100}
          }
        ],
        "responses": {
          "200": {
            "description": "Session Messages",
            "content": {"application/json": {"schema": {"$ref": "#/components/schemas/MessagesResponse"}}}
          },
          "default": {"$ref": "#/components/responses/ErrorResponse"}
        }
      },
      "post": {
        "tags": ["Caller"],
        "operationId": "sendSessionMessage",
        "summary": "Send a Message and stream the Agent response",
        "description": "Starts the Device operation and returns one Server-Sent Event stream. Disconnecting the SSE client stops forwarding only; the Agent operation continues on the Device. Only text content blocks are supported in v0.4.0, with at most 131072 UTF-8 bytes of input text across all blocks. Reasoning and response text are limited to 2097152 bytes per call; exceeding that limit produces PAYLOAD_TOO_LARGE while preserving the received part on Device. Errors before streaming are JSON ErrorResponse objects. Errors after streaming starts are error SSE events using the same code and message fields.",
        "security": [{"ownerSession": []}, {"apiKey": []}],
        "parameters": [
          {"$ref": "#/components/parameters/DeviceId"},
          {"$ref": "#/components/parameters/AgentId"},
          {"$ref": "#/components/parameters/SessionId"}
        ],
        "requestBody": {
          "required": true,
          "content": {"application/json": {"schema": {"$ref": "#/components/schemas/SendMessageRequest"}}}
        },
        "responses": {
          "200": {
            "description": "SSE stream. Each frame is event: <name> followed by data: <JSON>. Event names are message.delta, reasoning.delta, session.updated, done, and error. A terminal stream sends done or error, never both.",
            "content": {
              "text/event-stream": {
                "schema": {
                  "type": "string",
                  "description": "UTF-8 Server-Sent Events containing one JSON event object per data field. Comment-only keepalive frames may be sent every 15 seconds."
                },
                "examples": {
                  "messageDelta": {"summary": "Agent text delta", "value": "event: message.delta\ndata: {\"type\":\"message.delta\",\"delta\":\"Hello\"}\n\n"},
                  "reasoningDelta": {"summary": "Reasoning delta", "value": "event: reasoning.delta\ndata: {\"type\":\"reasoning.delta\",\"delta\":\"Working\"}\n\n"},
                  "sessionUpdated": {"summary": "Replacement Session ID", "value": "event: session.updated\ndata: {\"type\":\"session.updated\",\"session_id\":\"sess_new\"}\n\n"},
                  "done": {"summary": "Successful terminal event", "value": "event: done\ndata: {\"type\":\"done\"}\n\n"},
                  "error": {"summary": "Failed terminal event", "value": "event: error\ndata: {\"type\":\"error\",\"error\":{\"code\":\"AGENT_UNAVAILABLE\",\"message\":\"Agent failed\"}}\n\n"}
                }
              }
            },
            "x-sse-events": {
              "message.delta": {"$ref": "#/components/schemas/MessageDeltaEvent"},
              "reasoning.delta": {"$ref": "#/components/schemas/ReasoningDeltaEvent"},
              "session.updated": {"$ref": "#/components/schemas/SessionUpdatedEvent"},
              "done": {"$ref": "#/components/schemas/DoneEvent"},
              "error": {"$ref": "#/components/schemas/ErrorEvent"}
            }
          },
          "413": {"$ref": "#/components/responses/ErrorResponse"},
          "default": {"$ref": "#/components/responses/ErrorResponse"}
        }
      }
    }
  },
  "components": {
    "securitySchemes": {
      "ownerSession": {
        "type": "apiKey",
        "in": "cookie",
        "name": "agent_bridge_owner",
        "description": "30-day Owner Session cookie created by POST /api/v1/auth/login. Required for administration routes."
      },
      "apiKey": {
        "type": "http",
        "scheme": "bearer",
        "bearerFormat": "Agent-Bridge API Key",
        "description": "Caller API key beginning with abk_. API keys can use Caller routes but cannot use administration routes."
      }
    },
    "parameters": {
      "SetupTokenAuthorization": {
        "name": "Authorization",
        "in": "header",
        "required": false,
        "description": "Bearer <setup_token>. May be omitted when setup_token is provided in the request body.",
        "schema": {"type": "string"}
      },
      "AdminResourceId": {
        "name": "id",
        "in": "path",
        "required": true,
        "schema": {"type": "string"}
      },
      "DeviceId": {
        "name": "device_id",
        "in": "path",
        "required": true,
        "schema": {"type": "string"}
      },
      "AgentId": {
        "name": "agent_id",
        "in": "path",
        "required": true,
        "schema": {"type": "string"}
      },
      "SessionId": {
        "name": "session_id",
        "in": "path",
        "required": true,
        "schema": {"type": "string"}
      }
    },
    "responses": {
      "ErrorResponse": {
        "description": "Request failed. The HTTP status and stable error code describe the failure.",
        "content": {"application/json": {"schema": {"$ref": "#/components/schemas/ErrorResponse"}}}
      }
    },
    "schemas": {
      "StatusResponse": {
        "type": "object",
        "required": ["status", "initialized", "version"],
        "properties": {
          "status": {"type": "string", "const": "ok"},
          "initialized": {"type": "boolean"},
          "version": {"type": "string"}
        }
      },
      "SetupRequest": {
        "type": "object",
        "required": ["password"],
        "additionalProperties": false,
        "properties": {
          "setup_token": {"type": "string", "description": "One-time Setup Token; omit when sent in the Authorization header."},
          "password": {"type": "string", "format": "password", "minLength": 8, "maxLength": 1024}
        }
      },
      "SetupResponse": {
        "type": "object",
        "required": ["status", "initialized"],
        "properties": {
          "status": {"type": "string", "const": "ok"},
          "initialized": {"type": "boolean", "const": true}
        }
      },
      "LoginRequest": {
        "type": "object",
        "required": ["password"],
        "additionalProperties": false,
		"properties": {"password": {"type": "string", "format": "password", "minLength": 1, "maxLength": 1024}}
      },
      "LoginResponse": {
        "type": "object",
        "required": ["status", "expires_at"],
        "properties": {
          "status": {"type": "string", "const": "ok"},
          "expires_at": {"type": "string", "format": "date-time"}
        }
      },
      "Device": {
        "type": "object",
        "required": ["id", "name", "online", "agent_count", "created_at"],
        "properties": {
          "id": {"type": "string"},
          "name": {"type": "string"},
          "online": {"type": "boolean"},
          "agent_count": {"type": "integer", "minimum": 0},
          "created_at": {"type": "string", "format": "date-time"},
          "last_seen_at": {"type": "string", "format": "date-time"}
        }
      },
      "StoredDevice": {
        "type": "object",
        "required": ["id", "name", "created_at"],
        "properties": {
          "id": {"type": "string"},
          "name": {"type": "string"},
          "created_at": {"type": "string", "format": "date-time"},
          "last_seen_at": {"type": "string", "format": "date-time"}
        }
      },
      "DevicesResponse": {
        "type": "object",
        "required": ["devices"],
        "properties": {"devices": {"type": "array", "items": {"$ref": "#/components/schemas/Device"}}}
      },
      "RenameDeviceRequest": {
        "type": "object",
        "required": ["name"],
        "additionalProperties": false,
        "properties": {"name": {"type": "string", "minLength": 1, "maxLength": 120}}
      },
      "StoredDeviceResponse": {
        "type": "object",
        "required": ["device"],
        "properties": {"device": {"$ref": "#/components/schemas/StoredDevice"}}
      },
      "PairingCode": {
        "type": "object",
        "required": ["code", "expires_at", "expires_in"],
        "properties": {
          "code": {"type": "string"},
          "expires_at": {"type": "string", "format": "date-time"},
          "expires_in": {"type": "integer", "description": "Remaining lifetime in seconds", "example": 600}
        }
      },
      "ClaimPairingRequest": {
        "type": "object",
        "required": ["code"],
        "additionalProperties": false,
        "properties": {
          "code": {"type": "string"},
          "hostname": {"type": "string", "maxLength": 120, "description": "Preferred Device display name."},
          "name": {"type": "string", "maxLength": 120, "description": "Compatibility alias used only when hostname is empty."}
        }
      },
      "PairingCredentials": {
        "type": "object",
        "required": ["bridge_id", "token", "server_url"],
        "properties": {
          "bridge_id": {"type": "string"},
          "token": {"type": "string", "readOnly": true},
          "server_url": {"type": "string", "description": "Device WebSocket URL using ws or wss."}
        }
      },
      "ClaimPairingResponse": {
        "type": "object",
        "required": ["bridge_id", "token", "server_url", "device", "credentials"],
        "properties": {
          "bridge_id": {"type": "string"},
          "token": {"type": "string", "readOnly": true},
          "server_url": {"type": "string"},
          "device": {
            "type": "object",
            "required": ["id", "name"],
            "properties": {"id": {"type": "string"}, "name": {"type": "string"}}
          },
          "credentials": {"$ref": "#/components/schemas/PairingCredentials"}
        }
      },
      "ApiKey": {
        "type": "object",
        "required": ["id", "name", "prefix", "created_at"],
        "properties": {
          "id": {"type": "string"},
          "name": {"type": "string"},
          "prefix": {"type": "string"},
          "created_at": {"type": "string", "format": "date-time"},
          "last_used_at": {"type": "string", "format": "date-time"}
        }
      },
      "ApiKeysResponse": {
        "type": "object",
        "required": ["api_keys"],
        "properties": {"api_keys": {"type": "array", "items": {"$ref": "#/components/schemas/ApiKey"}}}
      },
      "CreateApiKeyRequest": {
        "type": "object",
        "required": ["name"],
        "additionalProperties": false,
        "properties": {"name": {"type": "string", "minLength": 1, "maxLength": 100}}
      },
      "CreatedApiKey": {
        "allOf": [
          {"$ref": "#/components/schemas/ApiKey"},
          {
            "type": "object",
            "required": ["key"],
            "properties": {"key": {"type": "string", "readOnly": true, "description": "Complete key, returned only by this response."}}
          }
        ]
      },
      "CreatedApiKeyResponse": {
        "type": "object",
        "required": ["api_key"],
        "properties": {"api_key": {"$ref": "#/components/schemas/CreatedApiKey"}}
      },
      "CallRecord": {
        "type": "object",
        "required": ["id", "device_id", "agent_id", "status", "duration_ms", "created_at"],
        "properties": {
          "id": {"type": "integer", "format": "int64"},
          "device_id": {"type": "string"},
          "agent_id": {"type": "string"},
          "status": {"type": "string", "enum": ["completed", "failed"]},
          "duration_ms": {"type": "integer", "format": "int64", "minimum": 0},
          "created_at": {"type": "string", "format": "date-time"}
        }
      },
      "CallsResponse": {
        "type": "object",
        "required": ["calls"],
        "properties": {"calls": {"type": "array", "maxItems": 1000, "items": {"$ref": "#/components/schemas/CallRecord"}}}
      },
      "Agent": {
        "type": "object",
        "required": ["id", "display_name", "status", "updated_at"],
        "properties": {
          "id": {"type": "string"},
          "display_name": {"type": "string"},
          "status": {"type": "string"},
          "updated_at": {"type": "string", "format": "date-time"}
        }
      },
      "AgentsResponse": {
        "type": "object",
        "required": ["agents"],
        "properties": {"agents": {"type": "array", "items": {"$ref": "#/components/schemas/Agent"}}}
      },
      "SessionSummary": {
        "type": "object",
        "required": ["agent_id", "session_id"],
        "properties": {
          "agent_id": {"type": "string"},
          "session_id": {"type": "string"},
          "message_count": {"type": "integer", "minimum": 0},
          "updated_at": {"type": "integer", "format": "int64"}
        }
      },
      "SessionsResponse": {
        "type": "object",
        "required": ["sessions"],
        "properties": {"sessions": {"type": "array", "items": {"$ref": "#/components/schemas/SessionSummary"}}}
      },
      "Session": {
        "type": "object",
        "required": ["id", "device_id", "agent_id"],
        "properties": {
          "id": {"type": "string"},
          "device_id": {"type": "string"},
          "agent_id": {"type": "string"}
        }
      },
      "CreateSessionResponse": {
        "type": "object",
        "required": ["session"],
        "properties": {"session": {"$ref": "#/components/schemas/Session"}}
      },
      "Message": {
        "type": "object",
        "required": ["role", "text"],
        "properties": {
          "role": {"type": "string", "enum": ["user", "assistant", "thought"]},
          "text": {"type": "string"}
        }
      },
      "MessagesResponse": {
        "type": "object",
        "required": ["messages", "total", "cursor"],
        "properties": {
          "messages": {"type": "array", "items": {"$ref": "#/components/schemas/Message"}},
          "total": {"type": "integer", "minimum": 0},
          "cursor": {"type": "integer", "minimum": 0}
        }
      },
      "ContentBlock": {
        "type": "object",
        "required": ["type", "text"],
        "additionalProperties": false,
        "properties": {
          "type": {"type": "string", "const": "text"},
          "text": {"type": "string"}
        }
      },
      "SendMessageRequest": {
        "type": "object",
        "required": ["content"],
        "additionalProperties": false,
        "properties": {
          "content": {"type": "array", "minItems": 1, "items": {"$ref": "#/components/schemas/ContentBlock"}}
        }
      },
      "MessageDeltaEvent": {
        "type": "object",
        "required": ["type", "delta"],
        "properties": {"type": {"type": "string", "const": "message.delta"}, "delta": {"type": "string"}}
      },
      "ReasoningDeltaEvent": {
        "type": "object",
        "required": ["type", "delta"],
        "properties": {"type": {"type": "string", "const": "reasoning.delta"}, "delta": {"type": "string"}}
      },
      "SessionUpdatedEvent": {
        "type": "object",
        "required": ["type", "session_id"],
        "properties": {"type": {"type": "string", "const": "session.updated"}, "session_id": {"type": "string"}}
      },
      "DoneEvent": {
        "type": "object",
        "required": ["type"],
        "properties": {"type": {"type": "string", "const": "done"}}
      },
      "ErrorEvent": {
        "type": "object",
        "required": ["type", "error"],
        "properties": {"type": {"type": "string", "const": "error"}, "error": {"$ref": "#/components/schemas/Error"}}
      },
      "Error": {
        "type": "object",
        "required": ["code", "message"],
        "properties": {
          "code": {
            "type": "string",
            "enum": [
              "DEVICE_NOT_FOUND", "DEVICE_OFFLINE", "AGENT_NOT_FOUND", "AGENT_UNAVAILABLE",
              "SESSION_NOT_FOUND", "PAIRING_CODE_INVALID", "PAIRING_CODE_EXPIRED",
              "PAIRING_CODE_CONSUMED", "UNSUPPORTED_CONTENT_TYPE", "UNAUTHORIZED",
              "FORBIDDEN", "RATE_LIMITED", "PAYLOAD_TOO_LARGE", "INVALID_REQUEST",
              "CONFLICT", "NOT_FOUND", "METHOD_NOT_ALLOWED", "INTERNAL_ERROR"
            ]
          },
          "message": {"type": "string"}
        }
      },
      "ErrorResponse": {
        "type": "object",
        "required": ["error"],
        "properties": {"error": {"$ref": "#/components/schemas/Error"}}
      }
    }
  }
}`

const docsHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Agent-Bridge Caller API</title><style>
:root{color:#171717;background:#fff;font-family:Inter,ui-sans-serif,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;font-synthesis:none;letter-spacing:0}*{box-sizing:border-box}html{scroll-behavior:smooth}body{min-width:320px;margin:0;color:#171717;background:#fff;font-size:14px;line-height:1.6}a{color:inherit;text-underline-offset:3px}code,pre{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace}.page{width:min(1180px,100%);margin:0 auto;display:grid;grid-template-columns:220px minmax(0,880px);justify-content:center}.sidebar{position:sticky;top:0;height:100vh;padding:34px 28px 40px 0;border-right:1px solid #e8e8e8}.brand{display:block;text-decoration:none;font-size:14px;font-weight:700}.sidebar-label{margin:3px 0 25px;color:#6f6f6f;font-size:12px}.nav{display:flex;flex-direction:column;gap:2px}.nav a{padding:6px 0;color:#565656;text-decoration:none;font-size:12px}.nav a:hover{color:#171717}.nav a:last-child{margin-top:14px;padding-top:14px;border-top:1px solid #e8e8e8}.content{min-width:0;padding:68px 0 100px 58px}.eyebrow{margin:0 0 10px;color:#6f6f6f;font-size:10px;font-weight:700;text-transform:uppercase}.hero h1{margin:0;font-size:34px;line-height:1.15;font-weight:700}.lead{max-width:680px;margin:13px 0 0;color:#565656;font-size:15px}.facts{margin-top:28px;display:grid;grid-template-columns:repeat(3,minmax(0,1fr));border-top:1px solid #e8e8e8;border-bottom:1px solid #e8e8e8}.fact{padding:12px 14px 12px 0}.fact+ .fact{padding-left:14px;border-left:1px solid #e8e8e8}.fact span{display:block;color:#8c8c8c;font-size:10px;text-transform:uppercase}.fact strong{display:block;margin-top:3px;font-size:12px;font-weight:620}.section{padding-top:48px;scroll-margin-top:20px}.section h2{margin:0 0 6px;font-size:20px;line-height:1.35}.section-intro{max-width:720px;margin:0 0 18px;color:#565656}.note{margin:16px 0;padding:12px 14px;border:1px solid #d6d6d6;border-radius:6px;background:#f8f8f8;color:#3f3f3f;font-size:12px}.steps{margin:20px 0 24px;padding:0;counter-reset:step;list-style:none;border-top:1px solid #e8e8e8}.steps li{display:grid;grid-template-columns:30px 170px minmax(0,1fr);gap:12px;padding:13px 0;border-bottom:1px solid #e8e8e8;counter-increment:step}.steps li:before{content:counter(step);color:#8c8c8c;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:11px}.steps strong{font-size:12px}.steps span{color:#565656;font-size:12px}.code-block{margin:16px 0}.code-head{min-height:36px;display:flex;align-items:center;justify-content:space-between;gap:14px}.code-head strong{font-size:11px;font-weight:620}.copy{height:30px;padding:0 10px;border:1px solid #d6d6d6;border-radius:5px;color:#171717;background:#fff;font-size:11px;cursor:pointer}.copy:hover{background:#f3f3f3}.copy:focus-visible{outline:2px solid #171717;outline-offset:2px}pre{margin:0;padding:15px;overflow:auto;border:1px solid #e8e8e8;border-radius:6px;color:#202020;background:#f8f8f8;font-size:11px;line-height:1.65;white-space:pre}p code,li code,td code{padding:2px 4px;border-radius:3px;background:#f3f3f3;font-size:.9em;overflow-wrap:anywhere}.table-wrap{width:100%;overflow:auto;border:1px solid #e8e8e8;border-radius:6px}table{width:100%;min-width:700px;border-collapse:collapse;table-layout:fixed;font-size:12px}th{padding:9px 11px;color:#6f6f6f;background:#f8f8f8;border-bottom:1px solid #e8e8e8;font-size:10px;font-weight:620;text-align:left}td{padding:11px;color:#3f3f3f;border-bottom:1px solid #e8e8e8;vertical-align:top}tr:last-child td{border-bottom:0}.endpoints th:first-child,.endpoints td:first-child{width:70px}.endpoints th:nth-child(2),.endpoints td:nth-child(2){width:54%}.method{color:#171717;font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;font-size:10px}.events th:first-child,.events td:first-child{width:125px}.events th:nth-child(2),.events td:nth-child(2){width:330px}.errors th:first-child,.errors td:first-child{width:210px}.rules{margin:18px 0 0;padding-left:19px;color:#3f3f3f}.rules li+li{margin-top:8px}.reference{margin-top:20px;display:flex;align-items:center;justify-content:space-between;gap:20px;padding:16px 0;border-top:1px solid #e8e8e8;border-bottom:1px solid #e8e8e8}.reference div{min-width:0}.reference strong{display:block;font-size:12px}.reference span{display:block;margin-top:3px;color:#6f6f6f;font-size:11px}.reference a{flex:0 0 auto;padding:8px 11px;border:1px solid #d6d6d6;border-radius:5px;text-decoration:none;font-size:11px}.reference a:hover{background:#f3f3f3}@media(max-width:900px){.page{display:block}.sidebar{position:static;width:auto;height:auto;padding:22px;border-right:0;border-bottom:1px solid #e8e8e8}.sidebar-label{margin-bottom:12px}.nav{flex-direction:row;flex-wrap:wrap;gap:2px 18px}.nav a:last-child{margin:0;padding:6px 0;border:0}.content{padding:42px 22px 80px}.section{padding-top:42px}}@media(max-width:600px){.hero h1{font-size:28px}.facts{grid-template-columns:1fr}.fact,.fact+ .fact{padding:10px 0;border-left:0;border-bottom:1px solid #e8e8e8}.fact:last-child{border-bottom:0}.steps li{grid-template-columns:20px minmax(0,1fr)}.steps li span{grid-column:2}.reference{align-items:flex-start;flex-direction:column}}@media(prefers-reduced-motion:reduce){html{scroll-behavior:auto}}
</style></head><body><div class="page"><aside class="sidebar"><a class="brand" href="#top">Agent-Bridge</a><p class="sidebar-label">Caller API reference</p><nav class="nav" aria-label="Documentation"><a href="#overview">Overview</a><a href="#authentication">Authentication</a><a href="#quick-start">Quick start</a><a href="#endpoints">Endpoints</a><a href="#messages">Messages</a><a href="#streaming">SSE streaming</a><a href="#errors">Errors</a><a href="#limits">Limits and behavior</a><a href="/openapi.json">OpenAPI JSON</a></nav></aside><main class="content" id="top"><header class="hero"><p class="eyebrow">API reference</p><h1>Caller API</h1><p class="lead">Call an Agent running on any paired Device from your own application. The Server routes requests and streams results while Session and Message content remains on the Device.</p><div class="facts"><div class="fact"><span>Protocol</span><strong>HTTP JSON + SSE</strong></div><div class="fact"><span>Authentication</span><strong>Bearer API Key</strong></div><div class="fact"><span>API version</span><strong>v1</strong></div></div></header>

<section class="section" id="overview"><h2>Overview</h2><p class="section-intro">The Caller API exposes one direct flow: discover a Device and Agent, create or choose a Session, then send a Message and consume the Agent output as Server-Sent Events.</p><ol class="steps"><li><strong>List Devices</strong><span>Choose an online <code>device_id</code>.</span></li><li><strong>List Agents</strong><span>Choose an available <code>agent_id</code> registered on that Device.</span></li><li><strong>Create a Session</strong><span>Keep the returned <code>session.id</code>.</span></li><li><strong>Send a Message</strong><span>Read SSE frames until <code>done</code> or <code>error</code>.</span></li></ol><div class="note">Caller requests are not queued. Session and Message operations fail immediately with <code>DEVICE_OFFLINE</code> when the selected Device is not connected.</div></section>

<section class="section" id="authentication"><h2>Authentication</h2><p class="section-intro">Create an API Key from <strong>Remote Console - API Keys</strong>. The complete key is displayed once, does not expire automatically, and can be revoked by the Owner at any time.</p><div class="code-block"><div class="code-head"><strong>Request header</strong><button class="copy" type="button" data-copy="auth-header">Copy</button></div><pre><code id="auth-header">Authorization: Bearer abk_your_api_key</code></pre></div><div class="note">An API Key can call every paired Device through Caller endpoints, but cannot use <code>/api/v1/admin/*</code>. Browser sessions used by Remote Console are separate from API Keys.</div></section>

<section class="section" id="quick-start"><h2>Quick start</h2><p class="section-intro">Set the Server URL and API Key, then replace the example IDs with values returned by the discovery calls.</p><div class="code-block"><div class="code-head"><strong>1. List Devices</strong><button class="copy" type="button" data-copy="quick-devices">Copy</button></div><pre><code id="quick-devices">export AGENT_BRIDGE_URL="https://bridge.example.com"
export AGENT_BRIDGE_API_KEY="abk_your_api_key"

curl -sS "$AGENT_BRIDGE_URL/api/v1/devices" \
  -H "Authorization: Bearer $AGENT_BRIDGE_API_KEY"</code></pre></div><div class="code-block"><div class="code-head"><strong>2. List Agents and create a Session</strong><button class="copy" type="button" data-copy="quick-session">Copy</button></div><pre><code id="quick-session">export DEVICE_ID="device_id_from_list"
export AGENT_ID="codex"

curl -sS \
  "$AGENT_BRIDGE_URL/api/v1/devices/$DEVICE_ID/agents" \
  -H "Authorization: Bearer $AGENT_BRIDGE_API_KEY"

curl -sS -X POST \
  "$AGENT_BRIDGE_URL/api/v1/devices/$DEVICE_ID/agents/$AGENT_ID/sessions" \
  -H "Authorization: Bearer $AGENT_BRIDGE_API_KEY"</code></pre></div><div class="code-block"><div class="code-head"><strong>3. Send a Message and consume SSE</strong><button class="copy" type="button" data-copy="quick-message">Copy</button></div><pre><code id="quick-message">export SESSION_ID="session_id_from_create_response"

curl -N -X POST \
  "$AGENT_BRIDGE_URL/api/v1/devices/$DEVICE_ID/agents/$AGENT_ID/sessions/$SESSION_ID/messages" \
  -H "Authorization: Bearer $AGENT_BRIDGE_API_KEY" \
  -H "Content-Type: application/json" \
  --data '{"content":[{"type":"text","text":"Hello"}]}'</code></pre></div></section>

<section class="section" id="endpoints"><h2>Caller endpoints</h2><p class="section-intro">All endpoints require Bearer authentication. Path parameters must use the unchanged IDs returned by earlier calls.</p><div class="table-wrap"><table class="endpoints"><thead><tr><th>Method</th><th>Path</th><th>Purpose</th></tr></thead><tbody><tr><td><strong class="method">GET</strong></td><td><code>/api/v1/devices</code></td><td>List paired Devices, online state, and Agent counts.</td></tr><tr><td><strong class="method">GET</strong></td><td><code>/api/v1/devices/{device_id}/agents</code></td><td>List Agents registered on a Device.</td></tr><tr><td><strong class="method">GET</strong></td><td><code>/api/v1/devices/{device_id}/agents/{agent_id}/sessions</code></td><td>Read Sessions from an online Device.</td></tr><tr><td><strong class="method">POST</strong></td><td><code>/api/v1/devices/{device_id}/agents/{agent_id}/sessions</code></td><td>Create a Session. No request body is required. Returns HTTP 201.</td></tr><tr><td><strong class="method">GET</strong></td><td><code>/api/v1/devices/{device_id}/agents/{agent_id}/sessions/{session_id}/messages?cursor=0&amp;limit=100</code></td><td>Read Message history using cursor pagination.</td></tr><tr><td><strong class="method">POST</strong></td><td><code>/api/v1/devices/{device_id}/agents/{agent_id}/sessions/{session_id}/messages</code></td><td>Send a Message and stream Agent output as SSE.</td></tr></tbody></table></div></section>

<section class="section" id="messages"><h2>Requests and responses</h2><p class="section-intro">Agent-Bridge v0.4.0 accepts text content blocks only. Multiple text blocks are allowed, with one combined size limit.</p><div class="code-block"><div class="code-head"><strong>Send Message request</strong><button class="copy" type="button" data-copy="message-body">Copy</button></div><pre><code id="message-body">{
  "content": [
    {"type": "text", "text": "Hello"}
  ]
}</code></pre></div><div class="code-block"><div class="code-head"><strong>Create Session response - HTTP 201</strong><button class="copy" type="button" data-copy="session-response">Copy</button></div><pre><code id="session-response">{
  "session": {
    "id": "sess_...",
    "device_id": "device_...",
    "agent_id": "codex"
  }
}</code></pre></div><div class="code-block"><div class="code-head"><strong>Message history response</strong><button class="copy" type="button" data-copy="messages-response">Copy</button></div><pre><code id="messages-response">{
  "messages": [
    {"role": "user", "text": "Hello"},
    {"role": "assistant", "text": "Hi"}
  ],
  "total": 2,
  "cursor": 2
}</code></pre></div><p>For history pagination, send the returned <code>cursor</code> in the next request. Reading is complete when <code>cursor</code> equals <code>total</code>.</p></section>

<section class="section" id="streaming"><h2>SSE streaming</h2><p class="section-intro"><code>POST .../messages</code> responds with <code>text/event-stream</code>. Each frame has an <code>event</code> name and one JSON object in <code>data</code>.</p><div class="code-block"><div class="code-head"><strong>Example stream</strong><button class="copy" type="button" data-copy="sse-example">Copy</button></div><pre><code id="sse-example">event: reasoning.delta
data: {"type":"reasoning.delta","delta":"Working"}

event: message.delta
data: {"type":"message.delta","delta":"Hello"}

event: done
data: {"type":"done"}</code></pre></div><div class="table-wrap"><table class="events"><thead><tr><th>Event</th><th>Data shape</th><th>Meaning</th></tr></thead><tbody><tr><td><code>message.delta</code></td><td><code>{"type":"message.delta","delta":"..."}</code></td><td>Agent response text delta.</td></tr><tr><td><code>reasoning.delta</code></td><td><code>{"type":"reasoning.delta","delta":"..."}</code></td><td>Reasoning text when the Agent provides it.</td></tr><tr><td><code>session.updated</code></td><td><code>{"type":"session.updated","session_id":"..."}</code></td><td>Replace the current Session ID for later requests.</td></tr><tr><td><code>done</code></td><td><code>{"type":"done"}</code></td><td>Successful terminal event.</td></tr><tr><td><code>error</code></td><td><code>{"type":"error","error":{"code":"...","message":"..."}}</code></td><td>Failed terminal event.</td></tr></tbody></table></div><ul class="rules"><li>A stream terminates with <code>done</code> or <code>error</code>, never both.</li><li>Comment-only <code>: keepalive</code> frames may arrive every 15 seconds and can be ignored.</li><li>Disconnecting the SSE client stops forwarding only. The Agent operation continues on the Device.</li><li>Disable proxy buffering and allow long-lived HTTP responses when placing the Server behind a reverse proxy.</li></ul></section>

<section class="section" id="errors"><h2>Error handling</h2><p class="section-intro">Before SSE starts, failures use an HTTP status and a JSON error body. After the stream starts, the same error object is sent in an <code>error</code> event.</p><div class="code-block"><div class="code-head"><strong>JSON error response</strong><button class="copy" type="button" data-copy="error-response">Copy</button></div><pre><code id="error-response">{
  "error": {
    "code": "DEVICE_OFFLINE",
    "message": "Device is offline"
  }
}</code></pre></div><div class="table-wrap"><table class="errors"><thead><tr><th>Stable code</th><th>Meaning</th></tr></thead><tbody><tr><td><code>UNAUTHORIZED</code></td><td>The API Key is missing, invalid, or revoked.</td></tr><tr><td><code>FORBIDDEN</code></td><td>The credential cannot access this resource.</td></tr><tr><td><code>DEVICE_NOT_FOUND</code></td><td>The Device ID does not exist.</td></tr><tr><td><code>DEVICE_OFFLINE</code></td><td>The Device is not currently connected.</td></tr><tr><td><code>AGENT_NOT_FOUND</code></td><td>The Agent is not registered on the Device.</td></tr><tr><td><code>AGENT_UNAVAILABLE</code></td><td>The Agent failed to start or execute.</td></tr><tr><td><code>SESSION_NOT_FOUND</code></td><td>The Session ID does not exist.</td></tr><tr><td><code>INVALID_REQUEST</code></td><td>A body, path, or query value is invalid.</td></tr><tr><td><code>UNSUPPORTED_CONTENT_TYPE</code></td><td>A content block type other than text was sent.</td></tr><tr><td><code>PAYLOAD_TOO_LARGE</code></td><td>Input or streamed output exceeded its limit.</td></tr><tr><td><code>INTERNAL_ERROR</code></td><td>An unexpected Server or Device response failed the call.</td></tr></tbody></table></div></section>

<section class="section" id="limits"><h2>Limits and behavior</h2><p class="section-intro">These rules are part of the Caller API contract and should shape client retries, timeouts, and storage behavior.</p><ul class="rules"><li><strong>Online requirement:</strong> Session and Message endpoints require an online Device. Requests are never queued for later delivery.</li><li><strong>Content location:</strong> Session and Message content remains on the Device. Server stores diagnostic metadata such as call status and duration only.</li><li><strong>Input:</strong> Message content must be text, with at most 131,072 UTF-8 bytes across all blocks.</li><li><strong>Output:</strong> Response and reasoning text are limited to 2,097,152 bytes per call. Exceeding the limit produces <code>PAYLOAD_TOO_LARGE</code>.</li><li><strong>History:</strong> <code>cursor</code> starts at 0. <code>limit</code> accepts 1-100 and defaults to 100.</li><li><strong>API Keys:</strong> Keys do not expire automatically. Revocation takes effect immediately.</li></ul><div class="reference"><div><strong>Machine-readable OpenAPI 3.1 definition</strong><span>Includes Caller, administration, setup, pairing, schemas, and error contracts.</span></div><a href="/openapi.json">Open /openapi.json</a></div></section>
</main></div><script>
document.addEventListener('click',async function(event){var button=event.target.closest('[data-copy]');if(!button)return;var target=document.getElementById(button.getAttribute('data-copy'));if(!target)return;var value=target.textContent;var original=button.textContent;try{if(navigator.clipboard&&window.isSecureContext){await navigator.clipboard.writeText(value)}else{var area=document.createElement('textarea');area.value=value;area.style.position='fixed';area.style.opacity='0';document.body.appendChild(area);area.select();document.execCommand('copy');area.remove()}button.textContent='Copied';setTimeout(function(){button.textContent=original},1600)}catch(error){button.textContent='Copy failed';setTimeout(function(){button.textContent=original},1600)}});
</script></body></html>`
