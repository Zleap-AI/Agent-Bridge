package service

import "testing"

func TestWantsStreaming(t *testing.T) {
	tests := []struct {
		name      string
		explicit  bool
		requestID string
		want      bool
	}{
		{name: "explicit flag", explicit: true, requestID: "request-1", want: true},
		{name: "default blocking", requestID: "request-1", want: false},
		{name: "legacy stream suffix", requestID: "request_stream", want: true},
		{name: "legacy bridge suffix", requestID: "request_bridge", want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := wantsStreaming(test.explicit, test.requestID); got != test.want {
				t.Fatalf("wantsStreaming(%v, %q) = %v, want %v", test.explicit, test.requestID, got, test.want)
			}
		})
	}
}
