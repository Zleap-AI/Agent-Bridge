import { BookOpen, ExternalLink } from "lucide-react";
import { CopyButton, ListLink, Notice } from "../shared/components/ui";
import { useI18n } from "../shared/i18n";

const endpoints = [
  ["GET", "/api/v1/devices", "remote.apiEndpointDevices"],
  ["GET", "/api/v1/devices/{device_id}/agents", "remote.apiEndpointAgents"],
  ["GET", "/api/v1/devices/{device_id}/agents/{agent_id}/sessions", "remote.apiEndpointSessions"],
  ["POST", "/api/v1/devices/{device_id}/agents/{agent_id}/sessions", "remote.apiEndpointCreateSession"],
  ["GET", "/api/v1/devices/{device_id}/agents/{agent_id}/sessions/{session_id}/messages?cursor=0&limit=100", "remote.apiEndpointMessages"],
  ["POST", "/api/v1/devices/{device_id}/agents/{agent_id}/sessions/{session_id}/messages", "remote.apiEndpointSendMessage"],
] as const;

const streamEvents = [
  ["message.delta", '{"type":"message.delta","delta":"..."}', "remote.apiEventMessage"],
  ["reasoning.delta", '{"type":"reasoning.delta","delta":"..."}', "remote.apiEventReasoning"],
  ["session.updated", '{"type":"session.updated","session_id":"..."}', "remote.apiEventSession"],
  ["done", '{"type":"done"}', "remote.apiEventDone"],
  ["error", '{"type":"error","error":{"code":"...","message":"..."}}', "remote.apiEventError"],
] as const;

const errorCodes = [
  ["UNAUTHORIZED", "remote.apiErrorUnauthorized"],
  ["FORBIDDEN", "remote.apiErrorForbidden"],
  ["DEVICE_NOT_FOUND", "remote.apiErrorDeviceNotFound"],
  ["DEVICE_OFFLINE", "remote.apiErrorDeviceOffline"],
  ["AGENT_NOT_FOUND", "remote.apiErrorAgentNotFound"],
  ["AGENT_UNAVAILABLE", "remote.apiErrorAgentUnavailable"],
  ["SESSION_NOT_FOUND", "remote.apiErrorSessionNotFound"],
  ["INVALID_REQUEST", "remote.apiErrorInvalidRequest"],
  ["UNSUPPORTED_CONTENT_TYPE", "remote.apiErrorUnsupportedContent"],
  ["PAYLOAD_TOO_LARGE", "remote.apiErrorPayloadTooLarge"],
  ["INTERNAL_ERROR", "remote.apiErrorInternal"],
] as const;

function CodeExample({ title, value }: { title: string; value: string }) {
  const { t } = useI18n();
  return (
    <div className="api-code">
      <div className="api-code__header">
        <strong>{title}</strong>
        <CopyButton value={value} label={t("remote.apiCopyExample")} />
      </div>
      <pre className="code-block">{value}</pre>
    </div>
  );
}

export function ApiDocumentation() {
  const { t } = useI18n();
  const baseURL = window.location.origin;
  const authenticationExample = `export AGENT_BRIDGE_URL="${baseURL}"
export AGENT_BRIDGE_API_KEY="abk_your_key"

curl -sS "$AGENT_BRIDGE_URL/api/v1/devices" \\
  -H "Authorization: Bearer $AGENT_BRIDGE_API_KEY"`;
  const createSessionExample = `export DEVICE_ID="device_id_from_previous_response"
export AGENT_ID="codex"

curl -sS \\
  "$AGENT_BRIDGE_URL/api/v1/devices/$DEVICE_ID/agents" \\
  -H "Authorization: Bearer $AGENT_BRIDGE_API_KEY"

curl -sS -X POST \\
  "$AGENT_BRIDGE_URL/api/v1/devices/$DEVICE_ID/agents/$AGENT_ID/sessions" \\
  -H "Authorization: Bearer $AGENT_BRIDGE_API_KEY"`;
  const sendMessageExample = `export SESSION_ID="session_id_from_create_response"

curl -N -X POST \\
  "$AGENT_BRIDGE_URL/api/v1/devices/$DEVICE_ID/agents/$AGENT_ID/sessions/$SESSION_ID/messages" \\
  -H "Authorization: Bearer $AGENT_BRIDGE_API_KEY" \\
  -H "Content-Type: application/json" \\
  --data '{"content":[{"type":"text","text":"Hello"}]}'`;
  const createSessionResponse = `{
  "session": {
    "id": "sess_...",
    "device_id": "device_...",
    "agent_id": "codex"
  }
}`;
  const errorExample = `{
  "error": {
    "code": "DEVICE_OFFLINE",
    "message": "Device is offline"
  }
}`;

  return (
    <div className="api-docs">
      <section className="api-docs__section" aria-labelledby="api-overview-title">
        <div className="section-heading"><div><h3 id="api-overview-title">{t("remote.apiOverview")}</h3><p>{t("remote.apiOverviewBody")}</p></div></div>
        <Notice>{t("remote.apiAuth")}</Notice>
        <div className="api-base-url">
          <div><span>{t("remote.apiBaseURL")}</span><code>{baseURL}</code></div>
          <CopyButton value={baseURL} />
        </div>
      </section>

      <section className="api-docs__section" aria-labelledby="api-quick-start-title">
        <div className="section-heading"><div><h3 id="api-quick-start-title">{t("remote.apiQuickStart")}</h3><p>{t("remote.apiQuickStartBody")}</p></div></div>
        <ol className="api-steps">
          <li><strong>{t("remote.apiStepCreateKey")}</strong><span>{t("remote.apiStepCreateKeyBody")}</span></li>
          <li><strong>{t("remote.apiStepDiscover")}</strong><span>{t("remote.apiStepDiscoverBody")}</span></li>
          <li><strong>{t("remote.apiStepCreateSession")}</strong><span>{t("remote.apiStepCreateSessionBody")}</span></li>
          <li><strong>{t("remote.apiStepSend")}</strong><span>{t("remote.apiStepSendBody")}</span></li>
        </ol>
        <CodeExample title={t("remote.apiAuthenticateAndList")} value={authenticationExample} />
        <CodeExample title={t("remote.apiCreateSessionExample")} value={createSessionExample} />
        <CodeExample title={t("remote.apiSendMessageExample")} value={sendMessageExample} />
      </section>

      <section className="api-docs__section" aria-labelledby="api-endpoints-title">
        <div className="section-heading"><div><h3 id="api-endpoints-title">{t("remote.resources")}</h3><p>{t("remote.apiEndpointsBody")}</p></div></div>
        <div className="api-table-wrap">
          <table className="api-table">
            <thead><tr><th>{t("remote.apiMethod")}</th><th>{t("remote.apiPath")}</th><th>{t("remote.apiPurpose")}</th></tr></thead>
            <tbody>{endpoints.map(([method, path, description]) => (
              <tr key={`${method}:${path}`}><td><strong className="api-method">{method}</strong></td><td><code>{path}</code></td><td>{t(description)}</td></tr>
            ))}</tbody>
          </table>
        </div>
      </section>

      <section className="api-docs__section" aria-labelledby="api-response-title">
        <div className="section-heading"><div><h3 id="api-response-title">{t("remote.apiRequestResponse")}</h3><p>{t("remote.apiRequestResponseBody")}</p></div></div>
        <div className="api-code-grid">
          <CodeExample title={t("remote.messageFormat")} value={'{\n  "content": [\n    {"type": "text", "text": "Hello"}\n  ]\n}'} />
          <CodeExample title={t("remote.apiSessionResponse")} value={createSessionResponse} />
        </div>
      </section>

      <section className="api-docs__section" aria-labelledby="api-stream-title">
        <div className="section-heading"><div><h3 id="api-stream-title">{t("remote.streamEvents")}</h3><p>{t("remote.apiStreamBody")}</p></div></div>
        <div className="api-table-wrap">
          <table className="api-table api-table--events">
            <thead><tr><th>{t("remote.apiEvent")}</th><th>{t("remote.apiPayload")}</th><th>{t("remote.apiMeaning")}</th></tr></thead>
            <tbody>{streamEvents.map(([event, payload, description]) => (
              <tr key={event}><td><code>{event}</code></td><td><code>{payload}</code></td><td>{t(description)}</td></tr>
            ))}</tbody>
          </table>
        </div>
        <ul className="api-notes">
          <li>{t("remote.apiStreamTerminal")}</li>
          <li>{t("remote.apiStreamKeepalive")}</li>
          <li>{t("remote.apiStreamDisconnect")}</li>
        </ul>
      </section>

      <section className="api-docs__section" aria-labelledby="api-errors-title">
        <div className="section-heading"><div><h3 id="api-errors-title">{t("remote.apiErrors")}</h3><p>{t("remote.apiErrorsBody")}</p></div></div>
        <CodeExample title={t("remote.apiErrorResponse")} value={errorExample} />
        <div className="api-error-list">{errorCodes.map(([code, description]) => (
          <div key={code}><code>{code}</code><span>{t(description)}</span></div>
        ))}</div>
      </section>

      <section className="api-docs__section" aria-labelledby="api-behavior-title">
        <div className="section-heading"><div><h3 id="api-behavior-title">{t("remote.apiLimits")}</h3><p>{t("remote.apiLimitsBody")}</p></div></div>
        <ul className="api-notes">
          <li>{t("remote.apiLimitOnline")}</li>
          <li>{t("remote.apiLimitStorage")}</li>
          <li>{t("remote.apiLimitInput")}</li>
          <li>{t("remote.apiLimitOutput")}</li>
          <li>{t("remote.apiLimitPagination")}</li>
          <li>{t("remote.apiLimitKey")}</li>
        </ul>
      </section>

      <section className="api-docs__section" aria-labelledby="api-reference-title">
        <div className="section-heading"><div><h3 id="api-reference-title">{t("remote.apiReference")}</h3><p>{t("remote.apiReferenceBody")}</p></div></div>
        <div className="data-list">
          <ListLink icon={BookOpen} title={t("remote.openDocs")} body="/docs" onClick={() => window.open("/docs", "_blank", "noopener")} />
          <ListLink icon={ExternalLink} title={t("remote.downloadOpenApi")} body="/openapi.json" onClick={() => window.open("/openapi.json", "_blank", "noopener")} />
        </div>
      </section>
    </div>
  );
}
