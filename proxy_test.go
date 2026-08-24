package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestBridge(t *testing.T, upstream *httptest.Server) *Bridge {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	store, err := NewConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := store.Get()
	for _, profile := range []*Profile{&cfg.Profiles.Chat, &cfg.Profiles.Image, &cfg.Profiles.Video} {
		profile.BaseURL = upstream.URL + "/v1"
		profile.APIKey = "test-key"
	}
	cfg.Profiles.Chat.Model = "mapped-chat"
	cfg.Profiles.Image.Model = "mapped-image"
	cfg.Profiles.Video.Model = "mapped-video"
	if err := store.Save(cfg); err != nil {
		t.Fatal(err)
	}
	return NewBridge(store, log.New(io.Discard, "", 0))
}

func TestChatAndImageMapping(t *testing.T) {
	type call struct{ path, model string }
	var calls []call
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		calls = append(calls, call{r.URL.Path, payload["model"].(string)})
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	defer upstream.Close()
	bridge := newTestBridge(t, upstream)

	for _, item := range []struct{ path string }{{"/v1/chat/completions"}, {"/v1/images"}} {
		req := httptest.NewRequest(http.MethodPost, item.path, strings.NewReader(`{"model":"luoshui","prompt":"test"}`))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		bridge.ServeHTTP(response, req)
		if response.Code != 200 {
			t.Fatalf("%s status = %d: %s", item.path, response.Code, response.Body.String())
		}
	}
	if calls[0] != (call{"/v1/chat/completions", "mapped-chat"}) {
		t.Fatalf("chat call = %#v", calls[0])
	}
	if calls[1] != (call{"/v1/images/generations", "mapped-image"}) {
		t.Fatalf("image call = %#v", calls[1])
	}
}

func TestVideoCreateAndPoll(t *testing.T) {
	var paths []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		writeJSON(w, 200, map[string]string{"id": "task_test", "status": "in_progress"})
	}))
	defer upstream.Close()
	bridge := newTestBridge(t, upstream)
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"model":"grok"}`)),
		httptest.NewRequest(http.MethodGet, "/v1/videos/task_test", nil),
	} {
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		bridge.ServeHTTP(response, request)
		if response.Code != 200 {
			t.Fatalf("status = %d: %s", response.Code, response.Body.String())
		}
	}
	if strings.Join(paths, ",") != "/v1/videos,/v1/videos/task_test" {
		t.Fatalf("paths = %v", paths)
	}
}

func TestUploadProxy(t *testing.T) {
	var gotUploadKey, gotContentType, gotBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUploadKey = r.Header.Get("X-Upload-Key")
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		writeJSON(w, http.StatusOK, map[string]string{"url": "https://cdn.example/image.png"})
	}))
	defer upstream.Close()
	bridge := newTestBridge(t, upstream)
	cfg := bridge.store.Get()
	cfg.Upload = UploadConfig{Enabled: true, URL: upstream.URL + "/upload", APIKey: "upload-key"}
	if err := bridge.store.Save(cfg); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("multipart-body"))
	req.Header.Set("Content-Type", "multipart/form-data; boundary=test")
	response := httptest.NewRecorder()
	bridge.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if gotUploadKey != "upload-key" {
		t.Fatalf("upload key = %q", gotUploadKey)
	}
	if gotContentType != "multipart/form-data; boundary=test" || gotBody != "multipart-body" {
		t.Fatalf("content type = %q, body = %q", gotContentType, gotBody)
	}
}

func TestGPT5RemovesLegacyMaxTokens(t *testing.T) {
	profile := Profile{Model: "gpt-5.5"}
	body := rewriteJSON([]byte(`{"model":"luoshui","max_tokens":8000,"temperature":0.7}`), profile, "chat", RequestOptions{})
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if _, exists := payload["max_tokens"]; exists {
		t.Fatalf("legacy max_tokens was not removed: %s", body)
	}
	if payload["model"] != "gpt-5.5" {
		t.Fatalf("model = %v", payload["model"])
	}
}

func TestNumericVideoSecondsBecomeString(t *testing.T) {
	body := rewriteJSON([]byte(`{"model":"grok","seconds":10}`), Profile{Model: "sd2-c6"}, "video", RequestOptions{})
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["seconds"] != "10" {
		t.Fatalf("seconds = %#v", payload["seconds"])
	}
}

func TestVideoReferenceAliasesAreNormalizedToImages(t *testing.T) {
	body := rewriteJSON([]byte(`{
		"reference_images":["https://cdn.example/one.png", "https://cdn.example/two.png"],
		"image_urls":["https://cdn.example/two.png", {"url":"https://cdn.example/three.png"}],
		"first_frame_url":"https://cdn.example/one.png"
	}`), Profile{Model: "sd-2-c4"}, "video", RequestOptions{})
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	images, ok := payload["images"].([]interface{})
	if !ok || len(images) != 3 {
		t.Fatalf("images = %#v; body = %s", payload["images"], body)
	}
	if images[0] != "https://cdn.example/one.png" || images[1] != "https://cdn.example/two.png" || images[2] != "https://cdn.example/three.png" {
		t.Fatalf("images = %#v", images)
	}
	if _, preserved := payload["reference_images"]; !preserved {
		t.Fatal("reference_images alias was not preserved")
	}
}

func TestVideoSubmissionLogCorrelatesTaskAndReferences(t *testing.T) {
	var upstreamPayload map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&upstreamPayload)
		writeJSON(w, http.StatusOK, map[string]string{"id": "task_correlated", "status": "queued"})
	}))
	defer upstream.Close()

	var logs strings.Builder
	bridge := newTestBridge(t, upstream)
	bridge.logger = log.New(&logs, "", 0)
	request := httptest.NewRequest(http.MethodPost, "/v1/videos", strings.NewReader(`{"reference_images":["https://cdn.example/one.png","https://cdn.example/two.png"]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	bridge.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	images, _ := upstreamPayload["images"].([]interface{})
	if len(images) != 2 {
		t.Fatalf("upstream images = %#v", upstreamPayload["images"])
	}
	if !strings.Contains(logs.String(), "upstream_task=task_correlated references=2") {
		t.Fatalf("logs do not correlate task and references: %s", logs.String())
	}
}

func TestUploadResponseProvidesAllLuoshuiURLAliases(t *testing.T) {
	body, uploadURL := normalizeUploadResponse([]byte(`{"ok":true,"url":"https://cdn.example/image.png"}`))
	if uploadURL != "https://cdn.example/image.png" {
		t.Fatalf("upload URL = %q", uploadURL)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"url", "image_url", "upload_url", "public_url"} {
		if payload[field] != uploadURL {
			t.Fatalf("%s = %#v", field, payload[field])
		}
	}
	data, _ := payload["data"].(map[string]interface{})
	if data["url"] != uploadURL {
		t.Fatalf("data.url = %#v", data["url"])
	}
}

func TestVideoStatusSummarySupportsNestedProgress(t *testing.T) {
	status, progress, detail := videoStatusSummary([]byte(`{"data":{"status":"processing","progress":0.42,"message":"rendering"}}`))
	if status != "processing" || progress != "42%" || detail != "rendering" {
		t.Fatalf("status=%q progress=%q detail=%q", status, progress, detail)
	}
	if got := videoStage(status, progress, detail); got != "正在生成视频" {
		t.Fatalf("stage=%q", got)
	}
}

func TestVideoStatusSummaryReportsFailure(t *testing.T) {
	status, progress, detail := videoStatusSummary([]byte(`{"status":"failed","error":"reference image rejected"}`))
	if status != "failed" || progress != "" || detail != "reference image rejected" {
		t.Fatalf("status=%q progress=%q detail=%q", status, progress, detail)
	}
	if got := videoStage(status, progress, detail); got != "失败" {
		t.Fatalf("stage=%q", got)
	}
}

func TestCompletedVideoURLsPointBackToBridge(t *testing.T) {
	body := rewriteVideoResponseURLs(
		[]byte(`{"status":"completed","url":"https://upstream.example/content","video_url":"https://upstream.example/content","metadata":{"url":"https://origin.example/content"}}`),
		"http://127.0.0.1:5400/v1/videos/task_test/content",
	)
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	want := "http://127.0.0.1:5400/v1/videos/task_test/content"
	if payload["url"] != want || payload["video_url"] != want {
		t.Fatalf("video URLs = %s", body)
	}
	metadata, _ := payload["metadata"].(map[string]interface{})
	if metadata["url"] != want {
		t.Fatalf("metadata URL = %s", body)
	}
}

func TestGeminiImageProtocolTranslation(t *testing.T) {
	request := translateGeminiImageRequest([]byte(`{"contents":[{"parts":[{"text":"draw a tree"}]}],"generationConfig":{"imageConfig":{"aspectRatio":"16:9"}}}`), Profile{Model: "gpt-image-2"}, RequestOptions{})
	var payload map[string]interface{}
	if err := json.Unmarshal(request, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["prompt"] != "draw a tree" || payload["size"] != "1536x1024" || payload["model"] != "gpt-image-2" {
		t.Fatalf("translated request = %s", request)
	}
	response := translateOpenAIImageResponse([]byte(`{"data":[{"b64_json":"aGVsbG8="}]}`))
	if !strings.Contains(string(response), `"inlineData"`) || !strings.Contains(string(response), "aGVsbG8=") {
		t.Fatalf("translated response = %s", response)
	}
}

func TestGeminiImageProtocolPreservesMultipleReferences(t *testing.T) {
	body := []byte(`{"contents":[{"parts":[{"text":"compose a storyboard shot"},{"inlineData":{"mimeType":"image/png","data":"Zmlyc3Q="}},{"inlineData":{"mimeType":"image/jpeg","data":"c2Vjb25k"}},{"fileData":{"fileUri":"https://cdn.example/ref-third.png"}}]}]}`)
	translated := translateGeminiImageRequest(body, Profile{Model: "gpt-image-2"}, RequestOptions{})
	var payload map[string]interface{}
	if err := json.Unmarshal(translated, &payload); err != nil {
		t.Fatal(err)
	}
	refs, ok := payload["images"].([]interface{})
	if !ok || len(refs) != 3 {
		t.Fatalf("references = %#v, translated = %s", payload["images"], translated)
	}
	if refs[0] != "data:image/png;base64,Zmlyc3Q=" || refs[1] != "data:image/jpeg;base64,c2Vjb25k" || refs[2] != "https://cdn.example/ref-third.png" {
		t.Fatalf("references = %#v", refs)
	}
	if payload["prompt"] != "compose a storyboard shot" {
		t.Fatalf("prompt = %#v", payload["prompt"])
	}
	if got := geminiReferenceCount(translated); got != 3 {
		t.Fatalf("reference count = %d", got)
	}
}

func TestGPTImage2UsesChatProtocolAndReturnsGeminiImage(t *testing.T) {
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("generated-image"))
	}))
	defer imageServer.Close()

	var gotPath string
	var gotPayload map[string]interface{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotPayload)
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"choices": []interface{}{map[string]interface{}{
				"message": map[string]interface{}{
					"content": "![result](" + imageServer.URL + "/image.png)",
				},
			}},
		})
	}))
	defer upstream.Close()

	bridge := newTestBridge(t, upstream)
	cfg := bridge.store.Get()
	cfg.Profiles.Image.Model = "gpt-image-2"
	if err := bridge.store.Save(cfg); err != nil {
		t.Fatal(err)
	}

	requestBody := `{"contents":[{"parts":[{"text":"draw a lighthouse"},{"fileData":{"fileUri":"https://cdn.example/ref.png"}}]}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-3.1-flash-image-preview:generateContent", strings.NewReader(requestBody))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	bridge.ServeHTTP(response, req)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("path = %q", gotPath)
	}
	messages, _ := gotPayload["messages"].([]interface{})
	message, _ := messages[0].(map[string]interface{})
	parts, _ := message["content"].([]interface{})
	if len(parts) != 2 {
		t.Fatalf("chat content = %#v", message["content"])
	}
	if !strings.Contains(response.Body.String(), base64.StdEncoding.EncodeToString([]byte("generated-image"))) {
		t.Fatalf("response = %s", response.Body.String())
	}
}

func TestChatImageMarkdownURLExtraction(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"![test](https://cdn.example/image.png?x=1&y=2)\nmetadata"}}]}`)
	if got := extractChatImageURL(body); got != "https://cdn.example/image.png?x=1&y=2" {
		t.Fatalf("url = %q", got)
	}
}

func TestChatImageWithoutReferencesUsesStringContent(t *testing.T) {
	body := translateOpenAIImageRequestToChat([]byte(`{"prompt":"draw a lighthouse"}`), Profile{Model: "gpt-image-2"})
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatal(err)
	}
	messages, _ := payload["messages"].([]interface{})
	message, _ := messages[0].(map[string]interface{})
	if message["content"] != "draw a lighthouse" {
		t.Fatalf("content = %#v", message["content"])
	}
}

func TestVideoDurationSelectionPriority(t *testing.T) {
	forced15 := rewriteJSON([]byte(`{"seconds":30}`), Profile{Model: "video"}, "video", RequestOptions{VideoDuration: "15"})
	forced := rewriteJSON([]byte(`{"seconds":15}`), Profile{Model: "video"}, "video", RequestOptions{VideoDuration: "30"})
	forcedDuration := rewriteJSON([]byte(`{"duration":15}`), Profile{Model: "video"}, "video", RequestOptions{VideoDuration: "30"})
	missingDuration := rewriteJSON([]byte(`{"prompt":"test"}`), Profile{Model: "video"}, "video", RequestOptions{VideoDuration: "15"})
	var forced15Payload, forcedPayload, durationPayload, missingPayload map[string]interface{}
	_ = json.Unmarshal(forced15, &forced15Payload)
	_ = json.Unmarshal(forced, &forcedPayload)
	_ = json.Unmarshal(forcedDuration, &durationPayload)
	_ = json.Unmarshal(missingDuration, &missingPayload)
	if forced15Payload["seconds"] != "15" {
		t.Fatalf("forced 15 duration = %#v", forced15Payload["seconds"])
	}
	if forcedPayload["seconds"] != "30" {
		t.Fatalf("forced duration = %#v", forcedPayload["seconds"])
	}
	if durationPayload["duration"] != float64(30) {
		t.Fatalf("forced duration field = %#v", durationPayload["duration"])
	}
	if missingPayload["seconds"] != "15" {
		t.Fatalf("missing duration was not supplied = %#v", missingPayload["seconds"])
	}
}

func TestStructuredAspectRatioOptions(t *testing.T) {
	image := rewriteJSON([]byte(`{"size":"1024x1024"}`), Profile{}, "image", RequestOptions{ImageAspectRatio: "16:9"})
	video := rewriteJSON([]byte(`{"aspect_ratio":"16:9"}`), Profile{}, "video", RequestOptions{VideoAspectRatio: "9:16"})
	if !strings.Contains(string(image), `"size":"1536x1024"`) {
		t.Fatalf("image options = %s", image)
	}
	if !strings.Contains(string(video), `"aspect_ratio":"9:16"`) {
		t.Fatalf("video options = %s", video)
	}
}

func TestImageURLIsEmbedded(t *testing.T) {
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("fake-image"))
	}))
	defer imageServer.Close()
	body := []byte(`{"data":[{"url":"` + imageServer.URL + `"}]}`)
	embedded := embedOpenAIImageURL(body, imageServer.Client())
	translated := translateOpenAIImageResponse(embedded)
	if !strings.Contains(string(translated), base64.StdEncoding.EncodeToString([]byte("fake-image"))) {
		t.Fatalf("translated response = %s", translated)
	}
}

func TestConfigFilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	store, err := NewConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(store.Get()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

type deniedAuthorization struct{}

func (deniedAuthorization) Check() error { return fmt.Errorf("test license denied") }

func TestUnauthorizedRequestsAreRejected(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("unauthorized request reached upstream")
	}))
	defer upstream.Close()
	bridge := newTestBridge(t, upstream)
	bridge.auth = deniedAuthorization{}

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test"}`))
	response := httptest.NewRecorder()
	bridge.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d: %s", response.Code, response.Body.String())
	}
}

func TestFetchModelsReportsAuthenticationError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer invalid-key" {
			t.Fatalf("authorization = %q", got)
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid token"}}`))
	}))
	defer server.Close()

	_, err := fetchModels(Profile{BaseURL: server.URL, APIKey: "  invalid-key  "}, time.Second)
	if !IsAuthenticationError(err) {
		t.Fatalf("expected authentication error, got %v", err)
	}
}

func TestValidateLaunchProfile(t *testing.T) {
	valid := Profile{Enabled: true, BaseURL: "https://api.example.com", APIKey: "key", Model: "model"}
	if err := validateLaunchProfile(valid); err != nil {
		t.Fatal(err)
	}
	valid.APIKey = ""
	if err := validateLaunchProfile(valid); err == nil {
		t.Fatal("empty API key should fail validation")
	}
}

func TestProbeChatProfileUsesConfiguredModelAndTrimmedKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer valid-key" {
			t.Fatalf("authorization = %q", got)
		}
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["model"] != "reasoning-model" {
			t.Fatalf("model = %#v", payload["model"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"OK"}}]}`))
	}))
	defer server.Close()
	profile := Profile{BaseURL: server.URL, APIKey: " valid-key ", Model: "reasoning-model"}
	if err := probeChatProfile(profile, time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestProbeChatProfileReports401(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid token"}}`))
	}))
	defer server.Close()
	err := probeChatProfile(Profile{BaseURL: server.URL, APIKey: "bad", Model: "model"}, time.Second)
	if !IsAuthenticationError(err) {
		t.Fatalf("expected authentication error, got %v", err)
	}
}

func TestAggregateChatStream(t *testing.T) {
	stream := strings.Join([]string{
		`data: {"id":"chat-1","object":"chat.completion.chunk","created":123,"model":"reasoning","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`,
		`data: {"id":"chat-1","object":"chat.completion.chunk","created":123,"model":"reasoning","choices":[{"index":0,"delta":{"reasoning_content":"think ","content":"hello"},"finish_reason":null}]}`,
		`data: {"id":"chat-1","object":"chat.completion.chunk","created":123,"model":"reasoning","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}]}`,
		`data: {"id":"chat-1","object":"chat.completion.chunk","created":123,"model":"reasoning","choices":[],"usage":{"total_tokens":10}}`,
		`data: [DONE]`,
	}, "\n\n")
	body, err := aggregateChatStream(strings.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	choices := result["choices"].([]interface{})
	choice := choices[0].(map[string]interface{})
	message := choice["message"].(map[string]interface{})
	if message["content"] != "hello world" || message["reasoning_content"] != "think " {
		t.Fatalf("message = %#v", message)
	}
}

func TestBridgeAggregatesUpstreamChatStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["stream"] != true {
			t.Fatalf("stream = %#v", payload["stream"])
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"id\":\"chat-1\",\"created\":123,\"model\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer upstream.Close()
	bridge := newTestBridge(t, upstream)
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	bridge.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("status=%d content-type=%q body=%s", response.Code, response.Header().Get("Content-Type"), response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"content":"OK"`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}
