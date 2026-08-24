package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Bridge struct {
	store           *ConfigStore
	logger          *log.Logger
	auth            interface{ Check() error }
	mu              sync.Mutex
	servers         []*http.Server
	running         bool
	requestSequence atomic.Uint64
}

func NewBridge(store *ConfigStore, logger *log.Logger, auth ...interface{ Check() error }) *Bridge {
	bridge := &Bridge{store: store, logger: logger}
	if len(auth) > 0 {
		bridge.auth = auth[0]
	}
	return bridge
}

func requestKind(path string) string {
	path = strings.ToLower(path)
	switch {
	case strings.HasPrefix(path, "/v1beta/models/") && strings.Contains(path, ":generatecontent"):
		return "image"
	case strings.HasPrefix(path, "/v1/videos"), strings.HasPrefix(path, "/v1/video"):
		return "video"
	case strings.HasPrefix(path, "/v1/images"), strings.HasPrefix(path, "/images"):
		return "image"
	case strings.HasPrefix(path, "/v1/chat/completions"), strings.HasPrefix(path, "/chat/completions"):
		return "chat"
	case strings.HasPrefix(path, "/v1/models"):
		return "chat"
	default:
		return ""
	}
}

func isGeminiImageRequest(path string) bool {
	path = strings.ToLower(path)
	return strings.HasPrefix(path, "/v1beta/models/") && strings.Contains(path, ":generatecontent")
}

func translateGeminiImageRequest(body []byte, profile Profile, options RequestOptions) []byte {
	var payload map[string]interface{}
	if json.Unmarshal(body, &payload) != nil {
		return body
	}
	var prompts []string
	var references []string
	if contents, ok := payload["contents"].([]interface{}); ok {
		for _, content := range contents {
			contentMap, _ := content.(map[string]interface{})
			parts, _ := contentMap["parts"].([]interface{})
			for _, part := range parts {
				partMap, _ := part.(map[string]interface{})
				if value, ok := partMap["text"].(string); ok && strings.TrimSpace(value) != "" {
					prompts = append(prompts, strings.TrimSpace(value))
				}
				if inline, ok := partMap["inlineData"].(map[string]interface{}); ok {
					mimeType, _ := inline["mimeType"].(string)
					data, _ := inline["data"].(string)
					if strings.TrimSpace(data) != "" {
						if strings.TrimSpace(mimeType) == "" {
							mimeType = "application/octet-stream"
						}
						references = append(references, "data:"+mimeType+";base64,"+data)
					}
				}
				if file, ok := partMap["fileData"].(map[string]interface{}); ok {
					if uri, ok := file["fileUri"].(string); ok && strings.TrimSpace(uri) != "" {
						references = append(references, strings.TrimSpace(uri))
					}
				}
			}
		}
	}
	size := "1024x1024"
	if generation, ok := payload["generationConfig"].(map[string]interface{}); ok {
		if imageConfig, ok := generation["imageConfig"].(map[string]interface{}); ok {
			switch imageConfig["aspectRatio"] {
			case "9:16", "3:4":
				size = "1024x1536"
			case "16:9", "4:3":
				size = "1536x1024"
			}
		}
	}
	if options.ImageAspectRatio != "" && options.ImageAspectRatio != "follow" {
		size = imageSizeForRatio(options.ImageAspectRatio)
	}
	result := map[string]interface{}{
		"model":  strings.TrimSpace(profile.Model),
		"prompt": strings.Join(prompts, "\n"),
		"size":   size,
		"n":      1,
	}
	if len(references) > 0 {
		result["images"] = references
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return body
	}
	return encoded
}

func geminiReferenceCount(body []byte) int {
	var payload map[string]interface{}
	if json.Unmarshal(body, &payload) != nil {
		return 0
	}
	items, _ := payload["images"].([]interface{})
	return len(items)
}

func usesChatImageProtocol(profile Profile) bool {
	model := strings.ToLower(strings.TrimSpace(profile.Model))
	return strings.HasPrefix(model, "gpt-image-2")
}

func translateOpenAIImageRequestToChat(body []byte, profile Profile) []byte {
	var payload map[string]interface{}
	if json.Unmarshal(body, &payload) != nil {
		return body
	}
	prompt, _ := payload["prompt"].(string)
	parts := []interface{}{map[string]interface{}{
		"type": "text",
		"text": prompt,
	}}
	hasReferences := false
	if references, ok := payload["images"].([]interface{}); ok {
		for _, item := range references {
			imageURL, _ := item.(string)
			if strings.TrimSpace(imageURL) == "" {
				continue
			}
			hasReferences = true
			parts = append(parts, map[string]interface{}{
				"type": "image_url",
				"image_url": map[string]interface{}{
					"url": strings.TrimSpace(imageURL),
				},
			})
		}
	}
	var content interface{} = prompt
	if hasReferences {
		content = parts
	}
	result := map[string]interface{}{
		"model": strings.TrimSpace(profile.Model),
		"messages": []interface{}{map[string]interface{}{
			"role":    "user",
			"content": content,
		}},
		"stream": false,
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return body
	}
	return encoded
}

var markdownImagePattern = regexp.MustCompile(`!\[[^\]]*\]\((https?://[^\s)]+(?:\)[^\s)]*)?)\)`)
var plainImageURLPattern = regexp.MustCompile(`https?://[^\s"'<>]+`)

func extractChatImageURL(body []byte) string {
	var payload struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if json.Unmarshal(body, &payload) != nil || len(payload.Choices) == 0 {
		return ""
	}
	content := payload.Choices[0].Message.Content
	if matches := markdownImagePattern.FindStringSubmatch(content); len(matches) > 1 {
		return matches[1]
	}
	for _, candidate := range plainImageURLPattern.FindAllString(content, -1) {
		lower := strings.ToLower(candidate)
		if strings.Contains(lower, ".png") || strings.Contains(lower, ".jpg") || strings.Contains(lower, ".jpeg") || strings.Contains(lower, ".webp") {
			return candidate
		}
	}
	return ""
}

func translateChatImageResponse(body []byte) []byte {
	imageURL := extractChatImageURL(body)
	if imageURL == "" {
		return body
	}
	encoded, err := json.Marshal(map[string]interface{}{
		"data": []interface{}{map[string]interface{}{"url": imageURL}},
	})
	if err != nil {
		return body
	}
	return encoded
}

func translateOpenAIImageResponse(body []byte) []byte {
	var payload struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil || len(payload.Data) == 0 || payload.Data[0].B64JSON == "" {
		return body
	}
	result := map[string]interface{}{
		"candidates": []interface{}{map[string]interface{}{
			"content": map[string]interface{}{
				"role": "model",
				"parts": []interface{}{map[string]interface{}{
					"inlineData": map[string]interface{}{"mimeType": "image/png", "data": payload.Data[0].B64JSON},
				}},
			},
		}},
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return body
	}
	return encoded
}

func embedOpenAIImageURL(body []byte, client *http.Client) []byte {
	var payload struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
			URL     string `json:"url"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &payload) != nil || len(payload.Data) == 0 || payload.Data[0].B64JSON != "" || payload.Data[0].URL == "" {
		return body
	}
	response, err := client.Get(payload.Data[0].URL)
	if err != nil {
		return body
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return body
	}
	imageBytes, err := io.ReadAll(response.Body)
	if err != nil || len(imageBytes) == 0 {
		return body
	}
	encoded, err := json.Marshal(map[string]interface{}{
		"data": []interface{}{map[string]interface{}{"b64_json": base64.StdEncoding.EncodeToString(imageBytes)}},
	})
	if err != nil {
		return body
	}
	return encoded
}

func upstreamURL(baseURL, requestPath, rawQuery string) string {
	base := strings.TrimRight(baseURL, "/")
	path := requestPath
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if strings.HasSuffix(strings.ToLower(base), "/v1") && strings.HasPrefix(strings.ToLower(path), "/v1/") {
		path = path[3:]
	}
	if rawQuery != "" {
		return base + path + "?" + rawQuery
	}
	return base + path
}

func profileFor(cfg Config, kind string) Profile {
	switch kind {
	case "image":
		return cfg.Profiles.Image
	case "video":
		return cfg.Profiles.Video
	default:
		return cfg.Profiles.Chat
	}
}

func imageSizeForRatio(ratio string) string {
	switch ratio {
	case "9:16", "3:4":
		return "1024x1536"
	case "16:9", "4:3":
		return "1536x1024"
	default:
		return "1024x1024"
	}
}

func rewriteJSON(body []byte, profile Profile, kind string, options RequestOptions) []byte {
	if len(body) == 0 {
		return body
	}
	var payload map[string]interface{}
	if json.Unmarshal(body, &payload) != nil {
		return body
	}
	if strings.TrimSpace(profile.Model) != "" {
		payload["model"] = strings.TrimSpace(profile.Model)
	}
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(profile.Model)), "gpt-5.") {
		delete(payload, "max_tokens")
	}
	if seconds, exists := payload["seconds"]; exists {
		if _, isString := seconds.(string); !isString {
			payload["seconds"] = fmt.Sprint(seconds)
		}
	}
	if kind == "image" && options.ImageAspectRatio != "" && options.ImageAspectRatio != "follow" {
		payload["size"] = imageSizeForRatio(options.ImageAspectRatio)
	}
	if kind == "video" {
		references, _ := collectVideoReferences(payload)
		if len(references) > 0 {
			// NewAPI uses images to decide between textGenerate and imageGenerate.
			// Luoshui video channels use several different aliases, so always add
			// the canonical field while retaining the original fields.
			payload["images"] = references
		}
		if options.VideoAspectRatio != "" && options.VideoAspectRatio != "follow" {
			payload["aspect_ratio"] = options.VideoAspectRatio
		}
		// “15 秒”代表跟随洛水；只有选择 30 秒时外置软件强制覆盖。
		if options.VideoDuration == "30" {
			if _, usesDuration := payload["duration"]; usesDuration {
				payload["duration"] = 30
			} else {
				payload["seconds"] = "30"
			}
		}
	}
	result, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return result
}

var videoReferenceFields = []string{
	"images",
	"reference_images",
	"image_urls",
	"image_url",
	"reference_image",
	"input_reference",
	"first_frame_image",
	"first_frame_url",
	"material_image_urls",
}

func collectVideoReferences(payload map[string]interface{}) ([]string, []string) {
	seen := make(map[string]bool)
	references := make([]string, 0)
	fields := make([]string, 0)
	for _, field := range videoReferenceFields {
		value, exists := payload[field]
		if !exists {
			continue
		}
		before := len(references)
		appendReferenceURLs(value, &references, seen)
		if len(references) > before {
			fields = append(fields, field)
		}
	}
	return references, fields
}

func appendReferenceURLs(value interface{}, references *[]string, seen map[string]bool) {
	switch typed := value.(type) {
	case string:
		candidate := strings.TrimSpace(typed)
		if candidate != "" && !seen[candidate] {
			seen[candidate] = true
			*references = append(*references, candidate)
		}
	case []interface{}:
		for _, item := range typed {
			appendReferenceURLs(item, references, seen)
		}
	case map[string]interface{}:
		for _, key := range []string{"url", "image_url", "file_url", "uri"} {
			if nested, exists := typed[key]; exists {
				appendReferenceURLs(nested, references, seen)
			}
		}
	}
}

type videoRequestDiagnostic struct {
	ID         string
	Model      string
	References []string
	Fields     []string
}

func inspectVideoRequest(body []byte, sequence uint64) videoRequestDiagnostic {
	diagnostic := videoRequestDiagnostic{ID: fmt.Sprintf("video-%06d", sequence)}
	var payload map[string]interface{}
	if json.Unmarshal(body, &payload) != nil {
		return diagnostic
	}
	diagnostic.Model, _ = payload["model"].(string)
	diagnostic.References, diagnostic.Fields = collectVideoReferences(payload)
	return diagnostic
}

func safeReferenceLabels(references []string) string {
	labels := make([]string, 0, len(references))
	for _, reference := range references {
		if strings.HasPrefix(reference, "data:") {
			labels = append(labels, "data-url")
			continue
		}
		parsed, err := url.Parse(reference)
		if err != nil || parsed.Host == "" {
			labels = append(labels, "local-or-unparsed")
			continue
		}
		parts := strings.Split(strings.TrimRight(parsed.Path, "/"), "/")
		name := parts[len(parts)-1]
		if name == "" {
			name = "(root)"
		}
		labels = append(labels, parsed.Host+"/"+name)
	}
	return strings.Join(labels, ",")
}

func extractTaskID(body []byte) string {
	var payload map[string]interface{}
	if json.Unmarshal(body, &payload) != nil {
		return ""
	}
	for _, key := range []string{"id", "task_id", "taskId"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	if data, ok := payload["data"].(map[string]interface{}); ok {
		for _, key := range []string{"id", "task_id", "taskId"} {
			if value, ok := data[key].(string); ok && strings.TrimSpace(value) != "" {
				return strings.TrimSpace(value)
			}
		}
	}
	return ""
}

func extractUploadURL(payload map[string]interface{}) string {
	for _, key := range []string{"url", "image_url", "upload_url", "public_url"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	if data, ok := payload["data"].(map[string]interface{}); ok {
		return extractUploadURL(data)
	}
	return ""
}

func normalizeUploadResponse(body []byte) ([]byte, string) {
	var payload map[string]interface{}
	if json.Unmarshal(body, &payload) != nil {
		return body, ""
	}
	uploadURL := extractUploadURL(payload)
	if uploadURL == "" {
		return body, ""
	}
	payload["url"] = uploadURL
	payload["image_url"] = uploadURL
	payload["upload_url"] = uploadURL
	payload["public_url"] = uploadURL
	data, ok := payload["data"].(map[string]interface{})
	if !ok {
		data = make(map[string]interface{})
		payload["data"] = data
	}
	data["url"] = uploadURL
	encoded, err := json.Marshal(payload)
	if err != nil {
		return body, uploadURL
	}
	return encoded, uploadURL
}

func videoStatusSummary(body []byte) (status string, progress string, detail string) {
	var payload map[string]interface{}
	if json.Unmarshal(body, &payload) != nil {
		return "响应不是 JSON", "", ""
	}
	status, progress, detail = videoStatusFields(payload)
	if data, ok := payload["data"].(map[string]interface{}); ok {
		nestedStatus, nestedProgress, nestedDetail := videoStatusFields(data)
		if status == "" {
			status = nestedStatus
		}
		if progress == "" {
			progress = nestedProgress
		}
		if detail == "" {
			detail = nestedDetail
		}
	}
	return status, progress, detail
}

func videoStatusFields(payload map[string]interface{}) (status string, progress string, detail string) {
	for _, key := range []string{"status", "state", "task_status"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			status = strings.TrimSpace(value)
			break
		}
	}
	for _, key := range []string{"progress", "percent", "percentage"} {
		if value, exists := payload[key]; exists {
			progress = formatProgress(value)
			if progress != "" {
				break
			}
		}
	}
	for _, key := range []string{"message", "error", "detail", "failure_reason"} {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			detail = strings.TrimSpace(value)
			break
		}
	}
	return status, progress, detail
}

func formatProgress(value interface{}) string {
	switch typed := value.(type) {
	case float64:
		if typed <= 1 {
			typed *= 100
		}
		return fmt.Sprintf("%.0f%%", typed)
	case string:
		text := strings.TrimSpace(typed)
		if text == "" {
			return ""
		}
		if strings.HasSuffix(text, "%") {
			return text
		}
		return text + "%"
	default:
		return ""
	}
}

func videoStage(status, progress, detail string) string {
	lower := strings.ToLower(status)
	switch {
	case strings.Contains(lower, "fail"), strings.Contains(lower, "error"), strings.Contains(lower, "cancel"):
		return "失败"
	case strings.Contains(lower, "complete"), strings.Contains(lower, "success"), strings.Contains(lower, "succeed"):
		return "生成完成"
	case strings.Contains(lower, "queue"), strings.Contains(lower, "pending"), strings.Contains(lower, "submit"):
		return "排队中"
	default:
		if progress != "" || status != "" || detail != "" {
			return "正在生成视频"
		}
		return "状态未知"
	}
}

func forceUpstreamChatStream(body []byte) ([]byte, bool) {
	var payload map[string]interface{}
	if json.Unmarshal(body, &payload) != nil {
		return body, false
	}
	if streaming, _ := payload["stream"].(bool); streaming {
		return body, false
	}
	payload["stream"] = true
	payload["stream_options"] = map[string]interface{}{"include_usage": true}
	result, err := json.Marshal(payload)
	if err != nil {
		return body, false
	}
	return result, true
}

type streamChoice struct {
	Index int `json:"index"`
	Delta struct {
		Role             string `json:"role"`
		Content          string `json:"content"`
		ReasoningContent string `json:"reasoning_content"`
	} `json:"delta"`
	FinishReason interface{} `json:"finish_reason"`
}

func aggregateChatStream(reader io.Reader) ([]byte, error) {
	type accumulatedChoice struct {
		Role             string
		Content          strings.Builder
		ReasoningContent strings.Builder
		FinishReason     interface{}
	}
	choices := map[int]*accumulatedChoice{}
	var id, model string
	var created interface{}
	var usage interface{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk struct {
			ID      string          `json:"id"`
			Created interface{}     `json:"created"`
			Model   string          `json:"model"`
			Choices []streamChoice  `json:"choices"`
			Usage   interface{}     `json:"usage"`
			Error   json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return nil, fmt.Errorf("解析上游流式响应失败: %w", err)
		}
		if len(chunk.Error) > 0 && string(chunk.Error) != "null" {
			return nil, fmt.Errorf("上游流式响应错误: %s", chunk.Error)
		}
		if chunk.ID != "" {
			id = chunk.ID
		}
		if chunk.Model != "" {
			model = chunk.Model
		}
		if chunk.Created != nil {
			created = chunk.Created
		}
		if chunk.Usage != nil {
			usage = chunk.Usage
		}
		for _, item := range chunk.Choices {
			choice := choices[item.Index]
			if choice == nil {
				choice = &accumulatedChoice{Role: "assistant"}
				choices[item.Index] = choice
			}
			if item.Delta.Role != "" {
				choice.Role = item.Delta.Role
			}
			choice.Content.WriteString(item.Delta.Content)
			choice.ReasoningContent.WriteString(item.Delta.ReasoningContent)
			if item.FinishReason != nil {
				choice.FinishReason = item.FinishReason
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(choices) == 0 {
		return nil, fmt.Errorf("上游流式响应未返回 choices")
	}
	indexes := make([]int, 0, len(choices))
	for index := range choices {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	resultChoices := make([]map[string]interface{}, 0, len(indexes))
	for _, index := range indexes {
		choice := choices[index]
		message := map[string]interface{}{"role": choice.Role, "content": choice.Content.String()}
		if choice.ReasoningContent.Len() > 0 {
			message["reasoning_content"] = choice.ReasoningContent.String()
		}
		resultChoices = append(resultChoices, map[string]interface{}{
			"index": index, "message": message, "finish_reason": choice.FinishReason,
		})
	}
	result := map[string]interface{}{
		"id": id, "object": "chat.completion", "created": created,
		"model": model, "choices": resultChoices,
	}
	if usage != nil {
		result["usage"] = usage
	}
	return json.Marshal(result)
}

func rewriteVideoResponseURLs(body []byte, localURL string) []byte {
	var payload map[string]interface{}
	if json.Unmarshal(body, &payload) != nil {
		return body
	}
	if _, exists := payload["url"]; !exists {
		if _, exists = payload["video_url"]; !exists {
			return body
		}
	}
	payload["url"] = localURL
	payload["video_url"] = localURL
	if metadata, ok := payload["metadata"].(map[string]interface{}); ok {
		metadata["url"] = localURL
	}
	result, err := json.Marshal(payload)
	if err != nil {
		return body
	}
	return result
}

func writeJSON(w http.ResponseWriter, status int, value interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func (b *Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.URL.Path == "/health" {
		cfg := b.store.Get()
		licensed := b.auth == nil || b.auth.Check() == nil
		writeJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "licensed": licensed, "name": "huiju-api-bridge-go", "ports": cfg.Listen.Ports})
		return
	}
	if b.auth != nil {
		if err := b.auth.Check(); err != nil {
			writeJSON(w, http.StatusForbidden, map[string]interface{}{"error": map[string]string{"message": "authorization required: " + err.Error()}})
			return
		}
	}
	if r.URL.Path == "/upload" {
		b.proxyUpload(w, r)
		return
	}
	b.proxy(w, r)
}

func (b *Bridge) proxyUpload(w http.ResponseWriter, r *http.Request) {
	cfg := b.store.Get()
	if !cfg.Upload.Enabled {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{"error": map[string]string{"message": "upload proxy is disabled"}})
		return
	}
	target := strings.TrimSpace(cfg.Upload.URL)
	if target == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{"error": map[string]string{"message": "upload url is not configured"}})
		return
	}
	b.logger.Printf("UPLOAD 开始：正在上传参考图片到图床 method=%s target=%s content_length=%d", r.Method, target, r.ContentLength)
	request, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"error": map[string]string{"message": err.Error()}})
		return
	}
	request.ContentLength = r.ContentLength
	if value := r.Header.Get("Content-Type"); value != "" {
		request.Header.Set("Content-Type", value)
	}
	if value := r.Header.Get("Accept"); value != "" {
		request.Header.Set("Accept", value)
	}
	if cfg.Upload.APIKey != "" {
		request.Header.Set("X-Upload-Key", cfg.Upload.APIKey)
	}
	client := &http.Client{Timeout: time.Duration(cfg.Compatibility.RequestTimeoutSecond) * time.Second}
	response, err := client.Do(request)
	if err != nil {
		b.logger.Printf("UPLOAD 失败：图床请求错误：%v", err)
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"error": map[string]string{"message": err.Error(), "type": "bridge_error"}})
		return
	}
	defer response.Body.Close()
	b.logger.Printf("UPLOAD 图床响应：HTTP %d", response.StatusCode)
	responseBody, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"error": map[string]string{"message": readErr.Error()}})
		return
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		var uploadURL string
		responseBody, uploadURL = normalizeUploadResponse(responseBody)
		if uploadURL == "" {
			b.logger.Printf("UPLOAD 失败：图床返回成功，但没有可用的公网图片 URL")
		} else {
			b.logger.Printf("UPLOAD 成功：参考图片已上传图床 url=%s，正在把 URL 返回给洛水", safeReferenceLabels([]string{uploadURL}))
		}
	} else {
		b.logger.Printf("UPLOAD 失败：图床拒绝上传 HTTP %d response=%.500s", response.StatusCode, responseBody)
	}
	for key, values := range response.Header {
		if strings.EqualFold(key, "Content-Length") || strings.EqualFold(key, "Transfer-Encoding") {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(responseBody)
}

func (b *Bridge) proxy(w http.ResponseWriter, r *http.Request) {
	kind := requestKind(r.URL.Path)
	if kind == "" {
		writeJSON(w, http.StatusNotFound, map[string]interface{}{"error": map[string]string{"message": "unsupported Luoshui path: " + r.URL.Path}})
		return
	}
	cfg := b.store.Get()
	profile := profileFor(cfg, kind)
	if !profile.Enabled {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{"error": map[string]string{"message": kind + " profile is disabled"}})
		return
	}
	if strings.TrimSpace(profile.BaseURL) == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{"error": map[string]string{"message": kind + " base_url is not configured"}})
		return
	}
	geminiImage := isGeminiImageRequest(r.URL.Path)
	chatImage := kind == "image" && usesChatImageProtocol(profile)
	path := r.URL.Path
	if geminiImage {
		path = "/v1/images/generations"
	}
	if kind == "image" && path == "/v1/images" && cfg.Compatibility.RewriteImagesPath {
		path = "/v1/images/generations"
	}
	target := upstreamURL(profile.BaseURL, path, r.URL.RawQuery)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]interface{}{"error": map[string]string{"message": err.Error()}})
		return
	}
	aggregateUpstreamChat := false
	var videoDiagnostic videoRequestDiagnostic
	if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		if geminiImage {
			body = translateGeminiImageRequest(body, profile, cfg.Options)
			b.logger.Printf("IMAGE Gemini references: %d", geminiReferenceCount(body))
		} else {
			body = rewriteJSON(body, profile, kind, cfg.Options)
		}
		if chatImage {
			body = translateOpenAIImageRequestToChat(body, profile)
			path = "/v1/chat/completions"
			target = upstreamURL(profile.BaseURL, path, r.URL.RawQuery)
			b.logger.Printf("IMAGE protocol: chat/completions")
		}
		if kind == "chat" && cfg.Compatibility.StreamChatUpstream {
			body, aggregateUpstreamChat = forceUpstreamChatStream(body)
			if aggregateUpstreamChat {
				b.logger.Printf("CHAT protocol: upstream stream aggregation")
			}
		}
		if kind == "video" && r.Method == http.MethodPost {
			videoDiagnostic = inspectVideoRequest(body, b.requestSequence.Add(1))
			b.logger.Printf("VIDEO 收到请求：request=%s model=%s，识别参考图片=%d，字段=%s，urls=%s",
				videoDiagnostic.ID, videoDiagnostic.Model, len(videoDiagnostic.References),
				strings.Join(videoDiagnostic.Fields, ","), safeReferenceLabels(videoDiagnostic.References))
		}
	}
	b.logger.Printf("%s %s %s -> %s", strings.ToUpper(kind), r.Method, r.URL.RequestURI(), target)
	if cfg.Compatibility.LogRequestBody && len(body) > 0 {
		b.logger.Printf("request body: %.10000s", body)
	}
	request, err := http.NewRequestWithContext(r.Context(), r.Method, target, bytes.NewReader(body))
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"error": map[string]string{"message": err.Error()}})
		return
	}
	if value := r.Header.Get("Content-Type"); value != "" {
		request.Header.Set("Content-Type", value)
	}
	request.Header.Set("Accept", r.Header.Get("Accept"))
	if profile.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(profile.APIKey))
	}
	client := &http.Client{Timeout: time.Duration(cfg.Compatibility.RequestTimeoutSecond) * time.Second}
	response, err := client.Do(request)
	if err != nil {
		b.logger.Printf("proxy error: %v", err)
		writeJSON(w, http.StatusBadGateway, map[string]interface{}{"error": map[string]string{"message": err.Error(), "type": "bridge_error"}})
		return
	}
	defer response.Body.Close()
	b.logger.Printf("%s upstream response: HTTP %d", strings.ToUpper(kind), response.StatusCode)
	for key, values := range response.Header {
		if strings.EqualFold(key, "Content-Length") || strings.EqualFold(key, "Transfer-Encoding") {
			continue
		}
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	if geminiImage || chatImage {
		responseBody, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			writeJSON(w, http.StatusBadGateway, map[string]interface{}{"error": map[string]string{"message": readErr.Error()}})
			return
		}
		if response.StatusCode >= 200 && response.StatusCode < 300 {
			if chatImage {
				responseBody = translateChatImageResponse(responseBody)
			}
			responseBody = embedOpenAIImageURL(responseBody, client)
			if geminiImage {
				responseBody = translateOpenAIImageResponse(responseBody)
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
		} else {
			b.logger.Printf("IMAGE upstream error: %.2000s", responseBody)
		}
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(responseBody)
		return
	}
	if kind == "video" {
		responseBody, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			writeJSON(w, http.StatusBadGateway, map[string]interface{}{"error": map[string]string{"message": readErr.Error()}})
			return
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			if r.Method == http.MethodPost {
				b.logger.Printf("VIDEO 失败：上游拒绝创建视频任务 HTTP %d response=%.1000s", response.StatusCode, responseBody)
			} else {
				b.logger.Printf("VIDEO 失败：查询任务状态 HTTP %d response=%.1000s", response.StatusCode, responseBody)
			}
		} else if !strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/content") {
			if videoDiagnostic.ID != "" {
				b.logger.Printf("VIDEO 提交成功：request=%s upstream_task=%s references=%d，洛水开始轮询视频状态",
					videoDiagnostic.ID, extractTaskID(responseBody), len(videoDiagnostic.References))
			} else {
				taskID := extractTaskID(responseBody)
				if taskID == "" {
					parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
					if len(parts) > 0 {
						taskID = parts[len(parts)-1]
					}
				}
				status, progress, detail := videoStatusSummary(responseBody)
				b.logger.Printf("VIDEO 状态查询：task=%s stage=%s status=%s progress=%s detail=%s",
					taskID, videoStage(status, progress, detail), status, progress, detail)
			}
			localURL := "http://" + r.Host + strings.TrimRight(r.URL.Path, "/") + "/content"
			responseBody = rewriteVideoResponseURLs(responseBody, localURL)
		} else if strings.HasSuffix(strings.TrimRight(r.URL.Path, "/"), "/content") {
			if response.StatusCode >= 200 && response.StatusCode < 300 {
				b.logger.Printf("VIDEO 成功：视频文件已生成，正在把内容返回给洛水")
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(responseBody)
		return
	}
	if kind == "chat" && aggregateUpstreamChat && response.StatusCode >= 200 && response.StatusCode < 300 && strings.Contains(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		responseBody, aggregateErr := aggregateChatStream(response.Body)
		if aggregateErr != nil {
			b.logger.Printf("CHAT stream aggregation error: %v", aggregateErr)
			writeJSON(w, http.StatusBadGateway, map[string]interface{}{"error": map[string]string{"message": aggregateErr.Error(), "type": "bridge_stream_error"}})
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Del("Content-Encoding")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(responseBody)
		return
	}
	if kind == "chat" && (response.StatusCode < 200 || response.StatusCode >= 300) {
		responseBody, readErr := io.ReadAll(response.Body)
		if readErr != nil {
			writeJSON(w, http.StatusBadGateway, map[string]interface{}{"error": map[string]string{"message": readErr.Error()}})
			return
		}
		b.logger.Printf("CHAT upstream error: %.2000s", responseBody)
		w.WriteHeader(response.StatusCode)
		_, _ = w.Write(responseBody)
		return
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, response.Body)
}

func (b *Bridge) Start() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.running {
		return nil
	}
	if b.auth != nil {
		if err := b.auth.Check(); err != nil {
			return fmt.Errorf("授权校验失败: %w", err)
		}
	}
	cfg := b.store.Get()
	listeners := make([]net.Listener, 0, len(cfg.Listen.Ports))
	for _, port := range cfg.Listen.Ports {
		listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", cfg.Listen.Host, port))
		if err != nil {
			for _, item := range listeners {
				_ = item.Close()
			}
			return err
		}
		listeners = append(listeners, listener)
	}
	for index, listener := range listeners {
		server := &http.Server{Handler: b, ReadHeaderTimeout: 30 * time.Second}
		b.servers = append(b.servers, server)
		port := cfg.Listen.Ports[index]
		b.logger.Printf("listening on http://%s:%d", cfg.Listen.Host, port)
		go func(s *http.Server, l net.Listener) {
			if err := s.Serve(l); err != nil && err != http.ErrServerClosed {
				b.logger.Printf("server error: %v", err)
			}
		}(server, listener)
	}
	b.running = true
	return nil
}

func (b *Bridge) Stop() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.running {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var firstErr error
	for _, server := range b.servers {
		if err := server.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	b.servers = nil
	b.running = false
	b.logger.Printf("bridge stopped")
	return firstErr
}

func (b *Bridge) Running() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.running
}

func fetchModels(profile Profile, timeout time.Duration) ([]string, error) {
	if strings.TrimSpace(profile.BaseURL) == "" {
		return nil, fmt.Errorf("Base URL 未配置")
	}
	target := upstreamURL(profile.BaseURL, "/v1/models", "")
	request, err := http.NewRequest(http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if profile.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(profile.APIKey))
	}
	response, err := (&http.Client{Timeout: timeout}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, &UpstreamHTTPError{StatusCode: response.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	var root struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&root); err != nil {
		return nil, err
	}
	models := make([]string, 0, len(root.Data))
	for _, item := range root.Data {
		if item.ID != "" {
			models = append(models, item.ID)
		}
	}
	return models, nil
}

func probeChatProfile(profile Profile, timeout time.Duration) error {
	if strings.TrimSpace(profile.BaseURL) == "" {
		return fmt.Errorf("Base URL 未配置")
	}
	payload, err := json.Marshal(map[string]interface{}{
		"model": strings.TrimSpace(profile.Model),
		"messages": []map[string]string{{
			"role": "user", "content": "Reply with OK.",
		}},
	})
	if err != nil {
		return err
	}
	target := upstreamURL(profile.BaseURL, "/v1/chat/completions", "")
	request, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if profile.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(profile.APIKey))
	}
	response, err := (&http.Client{Timeout: timeout}).Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 8192))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &UpstreamHTTPError{StatusCode: response.StatusCode, Body: strings.TrimSpace(string(body))}
	}
	var root struct {
		Choices []json.RawMessage `json:"choices"`
		Error   json.RawMessage   `json:"error"`
	}
	if err := json.Unmarshal(body, &root); err != nil {
		return fmt.Errorf("推理预检响应不是有效 JSON: %w", err)
	}
	if len(root.Error) > 0 && string(root.Error) != "null" {
		return fmt.Errorf("推理预检返回错误: %s", root.Error)
	}
	if len(root.Choices) == 0 {
		return fmt.Errorf("推理预检未返回 choices: %.1000s", body)
	}
	return nil
}

type UpstreamHTTPError struct {
	StatusCode int
	Body       string
}

func (e *UpstreamHTTPError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
}

func IsAuthenticationError(err error) bool {
	var upstreamErr *UpstreamHTTPError
	return errors.As(err, &upstreamErr) && upstreamErr.StatusCode == http.StatusUnauthorized
}

func validateBaseURL(value string) error {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(value))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return fmt.Errorf("Base URL 必须是完整的 http/https 地址")
	}
	return nil
}
