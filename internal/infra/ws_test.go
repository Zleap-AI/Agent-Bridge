package infra

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

type closeTrackingBody struct {
	closed bool
}

func (*closeTrackingBody) Read([]byte) (int, error) { return 0, io.EOF }

func (b *closeTrackingBody) Close() error {
	b.closed = true
	return nil
}

func TestLocalWebSocketOriginPolicy(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		origin string
		want   bool
	}{
		{name: "non browser client", host: "127.0.0.1:9202", want: true},
		{name: "same localhost origin", host: "localhost:9202", origin: "http://localhost:9202", want: true},
		{name: "equivalent loopback hosts", host: "127.0.0.1:9202", origin: "http://localhost:9202", want: true},
		{name: "same configured LAN origin", host: "192.0.2.5:9202", origin: "http://192.0.2.5:9202", want: true},
		{name: "different port", host: "127.0.0.1:9202", origin: "http://localhost:3000", want: false},
		{name: "public website", host: "127.0.0.1:9202", origin: "https://evil.example", want: false},
		{name: "opaque file origin", host: "127.0.0.1:9202", origin: "null", want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "http://"+test.host+"/ws/admin", nil)
			req.Host = test.host
			if test.origin != "" {
				req.Header.Set("Origin", test.origin)
			}
			if got := IsAllowedLocalOrigin(req); got != test.want {
				t.Fatalf("IsAllowedLocalOrigin(host=%q, origin=%q) = %v, want %v", test.host, test.origin, got, test.want)
			}
		})
	}
}

func TestIsWebSocketCloseRecognizesWrappedCloseError(t *testing.T) {
	err := fmt.Errorf("read failed: %w", &websocket.CloseError{Code: 4001, Text: "connection_replaced"})
	if !IsWebSocketClose(err, 4001, "connection_replaced") {
		t.Fatal("wrapped close error was not recognized")
	}
	if IsWebSocketClose(err, 4001, "different_reason") {
		t.Fatal("different close reason was accepted")
	}
}

func TestNewWSClientPreservesRejectedHandshakeStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "credentials revoked", http.StatusForbidden)
	}))
	defer server.Close()

	_, err := NewWSClient(context.Background(), WSClientConfig{
		URL: strings.Replace(server.URL, "http://", "ws://", 1),
	})
	if err == nil {
		t.Fatal("rejected handshake unexpectedly connected")
	}
	if !IsWebSocketHandshakeStatus(err, http.StatusUnauthorized, http.StatusForbidden) {
		t.Fatalf("handshake error = %v, want HTTP 403", err)
	}
	var handshakeError *WebSocketHandshakeError
	if !errors.As(err, &handshakeError) || handshakeError.StatusCode != http.StatusForbidden {
		t.Fatalf("typed handshake error = %#v", handshakeError)
	}
}

func TestWebSocketDialErrorClosesRejectedResponseBody(t *testing.T) {
	body := &closeTrackingBody{}
	err := newWebSocketDialError("ws://example.test", &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       body,
	}, errors.New("bad handshake"))

	if !body.closed {
		t.Fatal("rejected handshake response body was not closed")
	}
	if !IsWebSocketHandshakeStatus(err, http.StatusUnauthorized) {
		t.Fatalf("dial error = %v, want HTTP 401", err)
	}
}
