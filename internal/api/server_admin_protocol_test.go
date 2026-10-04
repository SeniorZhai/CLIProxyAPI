package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

const adminProtocolTestModel = "test-admin-protocol-model"

var adminProtocolFixtures = []struct {
	format, path, header, request, response, stream string
}{
	{
		format: "openai", path: "/v1/chat/completions", header: "Authorization",
		request:  `{"model":"test-admin-protocol-model","messages":[{"role":"user","content":"fixture input"}],"stream":false}`,
		response: `{"id":"test-chat","object":"chat.completion","choices":[{"message":{"role":"assistant","content":"fixture reply"},"finish_reason":"stop"}]}`,
		stream:   `{"id":"test-chat","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"fixture reply"},"finish_reason":"stop"}]}`,
	},
	{
		format: "openai-response", path: "/v1/responses", header: "Authorization",
		request:  `{"model":"test-admin-protocol-model","input":[{"role":"user","content":"fixture input"}],"stream":false}`,
		response: `{"id":"test-response","object":"response","status":"completed","output":[{"id":"test-message","type":"message","role":"assistant","content":[{"type":"output_text","text":"fixture reply"}]}]}`,
		stream:   "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"test-response\",\"status\":\"completed\",\"output\":[{\"id\":\"test-message\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"fixture reply\"}]}]}}\n\n",
	},
	{
		format: "claude", path: "/v1/messages", header: "X-Api-Key",
		request:  `{"model":"test-admin-protocol-model","max_tokens":32,"messages":[{"role":"user","content":"fixture input"}],"stream":false}`,
		response: `{"id":"test-message","type":"message","role":"assistant","content":[{"type":"text","text":"fixture reply"}],"stop_reason":"end_turn"}`,
		stream:   "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"fixture reply\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	},
	{
		format: "gemini", path: "/v1beta/models/test-admin-protocol-model:generateContent", header: "X-Goog-Api-Key",
		request:  `{"contents":[{"role":"user","parts":[{"text":"fixture input"}]}]}`,
		response: `{"candidates":[{"content":{"role":"model","parts":[{"text":"fixture reply"}]},"finishReason":"STOP"}]}`,
		stream:   `{"candidates":[{"content":{"role":"model","parts":[{"text":"fixture reply"}]},"finishReason":"STOP"}]}`,
	},
}

type adminProtocolCall struct {
	model, format, payload, authID string
	stream                         bool
}

type adminProtocolExecutor struct {
	mockServerStreamingCaptureExecutor
	calls chan adminProtocolCall
}

func (e *adminProtocolExecutor) execute(a *auth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (string, error) {
	e.calls <- adminProtocolCall{req.Model, opts.SourceFormat.String(), string(req.Payload), a.ID, opts.Stream}
	for _, fixture := range adminProtocolFixtures {
		if fixture.format == opts.SourceFormat.String() {
			if opts.Stream {
				return fixture.stream, nil
			}
			return fixture.response, nil
		}
	}
	return "", fmt.Errorf("unexpected protocol: %s", opts.SourceFormat)
}

func (e *adminProtocolExecutor) Execute(_ context.Context, a *auth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	payload, errExecute := e.execute(a, req, opts)
	return coreexecutor.Response{Payload: []byte(payload)}, errExecute
}

func (e *adminProtocolExecutor) ExecuteStream(_ context.Context, a *auth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	payload, errExecute := e.execute(a, req, opts)
	if errExecute != nil {
		return nil, errExecute
	}
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(payload)}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func newAdminProtocolServer(t *testing.T) (*Server, adminTestSession, string, string, *adminProtocolExecutor) {
	t.Helper()
	s := newAdminTestServer(t, filepath.Join(t.TempDir(), "config.yaml"))
	current := s.loginAdminTest(t)
	w := s.adminTestRequest(http.MethodPost, "/v8/management/client-keys", `{}`, current, "")
	var key struct{ ID, Key string }
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &key) != nil || key.Key == "" {
		t.Fatalf("create device key: status=%d", w.Code)
	}
	executor := &adminProtocolExecutor{calls: make(chan adminProtocolCall, 16)}
	s.handlers.AuthManager.RegisterExecutor(executor)
	credential := &auth.Auth{ID: t.Name(), Provider: executor.Identifier(), Status: auth.StatusActive}
	if _, errRegister := s.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
		t.Fatalf("register fixture credential: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(credential.ID, credential.Provider, []*registry.ModelInfo{{ID: adminProtocolTestModel}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(credential.ID) })
	return s, current, key.ID, key.Key, executor
}

func assertAdminProtocolCall(t *testing.T, executor *adminProtocolExecutor, format string, stream bool) {
	t.Helper()
	select {
	case call := <-executor.calls:
		if call.model != adminProtocolTestModel || call.format != format || call.stream != stream || call.authID == "" || !strings.Contains(call.payload, "fixture input") {
			t.Fatalf("unexpected forwarded request: %+v", call)
		}
	default:
		t.Fatal("request did not reach the fixture executor")
	}
}

func TestAdminDeviceKeyHTTPProtocols(t *testing.T) {
	s, current, _, key, executor := newAdminProtocolServer(t)
	server := httptest.NewServer(s.engine)
	t.Cleanup(server.Close)
	for _, fixture := range adminProtocolFixtures {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", fixture.format, stream), func(t *testing.T) {
				path, payload := fixture.path, fixture.request
				if stream {
					path = strings.Replace(path, ":generateContent", ":streamGenerateContent", 1)
					payload = strings.Replace(payload, `"stream":false`, `"stream":true`, 1)
				}
				for _, token := range []string{"", "invalid-device-key", key} {
					req, errRequest := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(payload))
					if errRequest != nil {
						t.Fatal(errRequest)
					}
					req.Header.Set("Content-Type", "application/json")
					if token != "" {
						value := token
						if fixture.header == "Authorization" {
							value = "Bearer " + token
						}
						req.Header.Set(fixture.header, value)
					}
					if token != key {
						req.AddCookie(current.cookie)
					}
					response, errDo := server.Client().Do(req)
					if errDo != nil {
						t.Fatal(errDo)
					}
					body, errRead := io.ReadAll(response.Body)
					if errClose := response.Body.Close(); errClose != nil {
						t.Fatal(errClose)
					}
					if errRead != nil {
						t.Fatal(errRead)
					}
					if token != key {
						if response.StatusCode != http.StatusUnauthorized || len(executor.calls) != 0 {
							t.Fatalf("administrator cookie or invalid key reached inference: status=%d calls=%d", response.StatusCode, len(executor.calls))
						}
						continue
					}
					if response.StatusCode != http.StatusOK {
						t.Fatalf("inference status=%d body=%s", response.StatusCode, body)
					}
					assertAdminProtocolCall(t, executor, fixture.format, stream)
					if stream {
						if !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") || !strings.Contains(string(body), "data: ") || !strings.Contains(string(body), "fixture reply") || !strings.Contains(string(body), "\n\n") {
							t.Fatalf("invalid SSE response: headers=%v body=%s", response.Header, body)
						}
					} else {
						var got, want any
						if errJSON := json.Unmarshal(body, &got); errJSON != nil {
							t.Fatalf("invalid JSON response: %v body=%s", errJSON, body)
						}
						if errJSON := json.Unmarshal([]byte(fixture.response), &want); errJSON != nil {
							t.Fatal(errJSON)
						}
						if !reflect.DeepEqual(got, want) {
							t.Fatalf("forwarded response=%s, want=%s", body, fixture.response)
						}
					}
				}
			})
		}
	}
}

func TestAdminDeviceKeyWebsocketProtocol(t *testing.T) {
	s, current, keyID, key, executor := newAdminProtocolServer(t)
	server := httptest.NewServer(s.engine)
	t.Cleanup(server.Close)
	for _, path := range []string{"/v1/responses", "/backend-api/codex/responses"} {
		t.Run(path, func(t *testing.T) {
			url := "ws" + strings.TrimPrefix(server.URL, "http") + path
			for _, token := range []string{"", "invalid-device-key", key} {
				headers := http.Header{}
				if token != "" {
					headers.Set("Authorization", "Bearer "+token)
				}
				if token != key {
					headers.Set("Cookie", current.cookie.String())
				}
				conn, response, errDial := websocket.DefaultDialer.Dial(url, headers)
				if conn != nil {
					t.Cleanup(func() {
						if errClose := conn.Close(); errClose != nil {
							t.Errorf("close websocket: %v", errClose)
						}
					})
				}
				if response != nil && response.Body != nil {
					if errClose := response.Body.Close(); errClose != nil {
						t.Fatal(errClose)
					}
				}
				if token != key {
					if errDial == nil || response == nil || response.StatusCode != http.StatusUnauthorized || len(executor.calls) != 0 {
						t.Fatalf("unauthorized websocket handshake: response=%v err=%v", response, errDial)
					}
					continue
				}
				if errDial != nil || response.StatusCode != http.StatusSwitchingProtocols {
					t.Fatalf("device websocket handshake: response=%v err=%v", response, errDial)
				}
				payload := `{"type":"response.create","model":"test-admin-protocol-model","input":[{"role":"user","content":"fixture input"}]}`
				if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(payload)); errWrite != nil {
					t.Fatal(errWrite)
				}
				messageType, body, errRead := conn.ReadMessage()
				if errRead != nil {
					t.Fatal(errRead)
				}
				if messageType != websocket.TextMessage || gjson.GetBytes(body, "type").String() != "response.completed" || gjson.GetBytes(body, "response.id").String() != "test-response" || !strings.Contains(string(body), "fixture reply") {
					t.Fatalf("invalid forwarded websocket message: %s", body)
				}
				assertAdminProtocolCall(t, executor, "openai-response", true)
			}
		})
	}
	if w := s.adminTestRequest(http.MethodDelete, "/v8/management/client-keys/"+keyID, "", current, ""); w.Code != http.StatusOK {
		t.Fatalf("revoke device key: status=%d", w.Code)
	}
	url := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses"
	conn, response, errDial := websocket.DefaultDialer.Dial(url, http.Header{"Authorization": {"Bearer " + key}})
	if conn != nil {
		if errClose := conn.Close(); errClose != nil {
			t.Error(errClose)
		}
	}
	if response != nil && response.Body != nil {
		if errClose := response.Body.Close(); errClose != nil {
			t.Error(errClose)
		}
	}
	if errDial == nil || response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked key allowed websocket handshake: response=%v err=%v", response, errDial)
	}
}
