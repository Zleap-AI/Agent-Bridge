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
<title>Agent-Bridge API</title><style>
body{margin:0;background:#fff;color:#111;font:15px/1.6 -apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}main{max-width:880px;margin:auto;padding:48px 24px 80px}h1{font-size:30px;margin:0 0 8px}h2{font-size:19px;margin-top:40px;border-bottom:1px solid #ddd;padding-bottom:8px}p{color:#444}code,pre{font-family:ui-monospace,SFMono-Regular,Menlo,monospace}code{background:#f5f5f5;padding:2px 5px;border-radius:4px}pre{background:#111;color:#fff;padding:16px;overflow:auto;border-radius:6px}a{color:#111}.event{display:grid;grid-template-columns:160px 1fr;gap:12px;padding:8px 0;border-bottom:1px solid #eee}@media(max-width:600px){.event{grid-template-columns:1fr}main{padding-top:28px}}
</style></head><body><main><h1>Agent-Bridge Caller API</h1><p>Use one API Key to call Agents on every paired Device. Session and Message content stays on the Device.</p>
<h2>Authentication</h2><pre>Authorization: Bearer abk_your_api_key</pre>
<h2>Flow</h2><p>List a Device and Agent, create a Session, then send Messages to that Session.</p>
<pre>POST /api/v1/devices/{device_id}/agents/{agent_id}/sessions

POST /api/v1/devices/{device_id}/agents/{agent_id}/sessions/{session_id}/messages
Content-Type: application/json

{"content":[{"type":"text","text":"Hello"}]}</pre>
<h2>SSE events</h2><div class="event"><code>message.delta</code><span>Agent response text</span></div><div class="event"><code>reasoning.delta</code><span>Reasoning text when provided by the Agent</span></div><div class="event"><code>session.updated</code><span>A replacement Session ID</span></div><div class="event"><code>done</code><span>The Message completed</span></div><div class="event"><code>error</code><span>A structured error</span></div>
<h2>OpenAPI</h2><p>The complete machine-readable definition is available at <a href="/openapi.json"><code>/openapi.json</code></a>.</p></main></body></html>`
