package distill

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// A content_filter stop is the gateway cutting the answer short, not a finished
// reply: it is an error that names the finish_reason, and no partial text is
// returned.
func TestStreamContentFilterIsError(t *testing.T) {
	server := httptest.NewServer(sseHandler(nil,
		`data: {"choices":[{"delta":{"content":"{\"actions\":["}}]}`+"\n\n",
		`data: {"choices":[{"delta":{},"finish_reason":"content_filter"}]}`+"\n\n",
		"data: [DONE]\n\n",
	))
	defer server.Close()

	content, err := newTestGateway(t, server, "").Complete(context.Background(), "system", "user", 0, nil)
	if err == nil {
		t.Fatalf("Complete succeeded with %q, want an error", content)
	}
	if !strings.Contains(err.Error(), "content_filter") {
		t.Fatalf("error = %v, want it to name content_filter", err)
	}
	if content != "" {
		t.Fatalf("content = %q on error, want no partial answer", content)
	}
}
