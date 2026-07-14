import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { Conversation } from "../shared/components/Conversation";
import { I18nProvider, useI18n } from "../shared/i18n";

function LanguageProbe() {
  const { locale, setLocale, t } = useI18n();
  return <button onClick={() => setLocale(locale === "zh" ? "en" : "zh")}>{t("chat.send")}</button>;
}

describe("shared UI", () => {
  it("switches all copy through the shared locale provider", () => {
    localStorage.setItem("agent-bridge-language", "zh");
    render(<I18nProvider><LanguageProbe /></I18nProvider>);
    expect(screen.getByRole("button", { name: "发送" })).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "发送" }));
    expect(screen.getByRole("button", { name: "Send" })).toBeInTheDocument();
  });

  it("submits a Message and clears the composer", async () => {
    localStorage.setItem("agent-bridge-language", "en");
    const onSend = vi.fn().mockResolvedValue(undefined);
    render(
      <I18nProvider>
        <Conversation
          agent={{ id: "codex", displayName: "Codex", status: "idle" }}
          sessions={[{ id: "session-1" }]}
          sessionId="session-1"
          onSelectSession={() => undefined}
          onCreateSession={() => undefined}
          onRefreshSessions={() => undefined}
          messages={[]}
          enabled
          onSend={onSend}
          onOpenMobileMenu={() => undefined}
          mobileMenuOpen={false}
        />
      </I18nProvider>,
    );
    const input = screen.getByRole("textbox", { name: "Write a Message" });
    fireEvent.change(input, { target: { value: "Hello" } });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(onSend).toHaveBeenCalledWith("Hello"));
    expect(input).toHaveValue("");
  });

  it("disables chat and shows a clear offline state", () => {
    localStorage.setItem("agent-bridge-language", "en");
    render(
      <I18nProvider>
        <Conversation
          agent={{ id: "codex", displayName: "Codex", status: "disconnected" }}
          sessions={[]}
          sessionId=""
          onSelectSession={() => undefined}
          onCreateSession={() => undefined}
          onRefreshSessions={() => undefined}
          messages={[]}
          enabled={false}
          unavailableTitle="Device is offline"
          unavailableBody="Reconnect to continue"
          onSend={() => undefined}
          onOpenMobileMenu={() => undefined}
          mobileMenuOpen={false}
        />
      </I18nProvider>,
    );
    expect(screen.getByRole("heading", { name: "Device is offline" })).toBeInTheDocument();
    expect(screen.getByRole("textbox")).toBeDisabled();
  });

  it("locks Session controls while a Message is streaming", () => {
    localStorage.setItem("agent-bridge-language", "en");
    render(
      <I18nProvider>
        <Conversation
          agent={{ id: "codex", displayName: "Codex", status: "busy" }}
          sessions={[{ id: "session-1" }]}
          sessionId="session-1"
          onSelectSession={() => undefined}
          onCreateSession={() => undefined}
          onRefreshSessions={() => undefined}
          messages={[]}
          sending
          enabled
          onSend={() => undefined}
          onOpenMobileMenu={() => undefined}
          mobileMenuOpen={false}
        />
      </I18nProvider>,
    );
    expect(screen.getByRole("combobox", { name: "Session" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Refresh" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "New Session" })).toBeDisabled();
  });

  it("locks context and composer controls while Session history is loading", () => {
    localStorage.setItem("agent-bridge-language", "en");
    render(
      <I18nProvider>
        <Conversation
          agent={{ id: "codex", displayName: "Codex", status: "idle" }}
          sessions={[{ id: "session-1" }]}
          sessionId="session-1"
          onSelectSession={() => undefined}
          onCreateSession={() => undefined}
          onRefreshSessions={() => undefined}
          messages={[]}
          messagesLoading
          enabled
          onSend={() => undefined}
          onOpenMobileMenu={() => undefined}
          mobileMenuOpen={false}
        />
      </I18nProvider>,
    );
    expect(screen.getByRole("combobox", { name: "Session" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Refresh" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "New Session" })).toBeDisabled();
    expect(screen.getByRole("textbox", { name: "Write a Message" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
  });

  it("does not submit Enter while an IME composition is active", () => {
    localStorage.setItem("agent-bridge-language", "en");
    const onSend = vi.fn();
    render(
      <I18nProvider>
        <Conversation
          agent={{ id: "codex", displayName: "Codex", status: "idle" }}
          sessions={[{ id: "session-1" }]}
          sessionId="session-1"
          onSelectSession={() => undefined}
          onCreateSession={() => undefined}
          onRefreshSessions={() => undefined}
          messages={[]}
          enabled
          onSend={onSend}
          onOpenMobileMenu={() => undefined}
          mobileMenuOpen={false}
        />
      </I18nProvider>,
    );
    const input = screen.getByRole("textbox", { name: "Write a Message" });
    fireEvent.change(input, { target: { value: "nihao" } });
    fireEvent.keyDown(input, { key: "Enter", code: "Enter", isComposing: true });
    expect(onSend).not.toHaveBeenCalled();
    expect(input).toHaveValue("nihao");
  });

  it("grows the composer with its content", () => {
    localStorage.setItem("agent-bridge-language", "en");
    render(
      <I18nProvider>
        <Conversation
          agent={{ id: "codex", displayName: "Codex", status: "idle" }}
          sessions={[{ id: "session-1" }]}
          sessionId="session-1"
          onSelectSession={() => undefined}
          onCreateSession={() => undefined}
          onRefreshSessions={() => undefined}
          messages={[]}
          enabled
          onSend={() => undefined}
          onOpenMobileMenu={() => undefined}
          mobileMenuOpen={false}
        />
      </I18nProvider>,
    );
    const input = screen.getByRole("textbox", { name: "Write a Message" });
    Object.defineProperty(input, "scrollHeight", { configurable: true, value: 104 });
    fireEvent.change(input, { target: { value: "one\ntwo\nthree" } });
    expect(input).toHaveStyle({ height: "104px" });
  });

  it("keeps the reader's scroll position when new content arrives above the bottom", () => {
    localStorage.setItem("agent-bridge-language", "en");
    const renderConversation = (messages: Array<{ id: string; role: "assistant"; content: Array<{ type: "text"; text: string }> }>) => (
      <I18nProvider>
        <Conversation
          agent={{ id: "codex", displayName: "Codex", status: "idle" }}
          sessions={[{ id: "session-1" }]}
          sessionId="session-1"
          onSelectSession={() => undefined}
          onCreateSession={() => undefined}
          onRefreshSessions={() => undefined}
          messages={messages}
          enabled
          onSend={() => undefined}
          onOpenMobileMenu={() => undefined}
          mobileMenuOpen={false}
        />
      </I18nProvider>
    );
    const { container, rerender } = render(renderConversation([
      { id: "one", role: "assistant", content: [{ type: "text", text: "First" }] },
    ]));
    const history = container.querySelector<HTMLElement>(".workspace__messages");
    expect(history).not.toBeNull();
    Object.defineProperties(history!, {
      scrollHeight: { configurable: true, value: 1000 },
      clientHeight: { configurable: true, value: 400 },
    });
    history!.scrollTop = 200;
    fireEvent.scroll(history!);
    rerender(renderConversation([
      { id: "one", role: "assistant", content: [{ type: "text", text: "First" }] },
      { id: "two", role: "assistant", content: [{ type: "text", text: "Second" }] },
    ]));
    expect(history!.scrollTop).toBe(200);
    expect(history).not.toHaveAttribute("aria-live");
  });

  it("announces completion without making the full history a live region", async () => {
    localStorage.setItem("agent-bridge-language", "en");
    const view = (sending: boolean, pending: boolean) => (
      <I18nProvider>
        <Conversation
          agent={{ id: "codex", displayName: "Codex", status: sending ? "busy" : "idle" }}
          sessions={[{ id: "session-1" }]}
          sessionId="session-1"
          onSelectSession={() => undefined}
          onCreateSession={() => undefined}
          onRefreshSessions={() => undefined}
          messages={[{ id: "answer", role: "assistant", pending, content: [{ type: "text", text: pending ? "" : "Done" }] }]}
          sending={sending}
          enabled
          onSend={() => undefined}
          onOpenMobileMenu={() => undefined}
          mobileMenuOpen={false}
        />
      </I18nProvider>
    );
    const { container, rerender } = render(view(true, true));
    await waitFor(() => expect(container.querySelector(".sr-only")).toHaveTextContent("Waiting for Agent"));
    rerender(view(false, false));
    await waitFor(() => expect(container.querySelector(".sr-only")).toHaveTextContent("Agent response complete"));
    expect(container.querySelector(".workspace__messages")).not.toHaveAttribute("aria-live");
  });
});
