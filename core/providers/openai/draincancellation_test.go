package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

// 客户端取消后，上游中转商通常仍会完成生成并下发带真实 usage 的终止事件。
// drain 窗口应继续读到它并按真实值结算，而不是立即断开回退本地估算。
func TestResponsesStreamCancellationDrainsTerminalUsage(t *testing.T) {
	proceed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("test server ResponseWriter is not an http.Flusher")
			return
		}
		if _, err := w.Write([]byte("data: {\"type\":\"response.output_text.delta\",\"sequence_number\":1,\"delta\":\"partial\"}\n\n")); err != nil {
			return
		}
		flusher.Flush()
		<-proceed
		// 上游在客户端取消之后才完成生成，下发带真实 usage 的终止事件
		if _, err := w.Write([]byte("data: {\"type\":\"response.completed\",\"sequence_number\":2,\"response\":{\"id\":\"r1\",\"status\":\"completed\",\"usage\":{\"input_tokens\":100,\"output_tokens\":7,\"total_tokens\":107}}}\n\n")); err != nil {
			return
		}
	}))
	defer server.Close()

	captured := make(chan *schemas.BifrostError, 8)
	hook := func(_ *schemas.BifrostContext, resp *schemas.BifrostResponse, err *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError) {
		if err != nil {
			captured <- err
		}
		return resp, err
	}

	ctx, cancel := schemas.NewBifrostContextWithCancel(context.Background())
	defer cancel()

	provider := newStreamTestProvider(server.URL)
	request := &schemas.BifrostResponsesRequest{
		Provider: schemas.OpenAI,
		Model:    "repro-model",
		Input: []schemas.ResponsesMessage{{
			Type:    schemas.Ptr(schemas.ResponsesMessageTypeMessage),
			Role:    schemas.Ptr(schemas.ResponsesInputMessageRoleUser),
			Content: &schemas.ResponsesMessageContent{ContentStr: schemas.Ptr("hi")},
		}},
	}
	stream, bifrostErr := provider.ResponsesStream(ctx, hook, nil, testKey(), request)
	if bifrostErr != nil {
		t.Fatalf("stream setup failed: %v", bifrostErr)
	}

	// 等第一个 chunk 送达后再取消，确保取消发生在流中途
	select {
	case <-stream:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for first chunk")
	}
	cancel()
	close(proceed)

	deadline := time.After(10 * time.Second)
	for {
		select {
		case err := <-captured:
			if err == nil {
				continue
			}
			usage := err.ExtraFields.BilledUsage
			if usage == nil {
				t.Fatalf("cancellation error has no billed usage: %+v", err)
			}
			if usage.Estimated {
				t.Fatalf("billed usage should be actual, got estimated: %+v", usage)
			}
			if usage.PromptTokens != 100 || usage.CompletionTokens != 7 {
				t.Fatalf("billed usage = %+v, want prompt=100 completion=7", usage)
			}
			return
		case <-deadline:
			t.Fatal("timed out waiting for cancellation billing with actual usage")
		}
	}
}
