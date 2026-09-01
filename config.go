package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const (
	defaultUpstreamURL  = "https://huiju.v888.art"
	defaultUploadURL    = "https://huiju.v888.art/upload"
	defaultUploadAPIKey = "huiju-upload-2026"
)

var allowCustomURLsForTests bool

type ListenConfig struct {
	Host  string `json:"host"`
	Ports []int  `json:"ports"`
}

type Profile struct {
	Enabled bool   `json:"enabled"`
	BaseURL string `json:"base_url"`
	APIKey  string `json:"api_key"`
	Model   string `json:"model"`
}

type Profiles struct {
	Chat  Profile `json:"chat"`
	Image Profile `json:"image"`
	Video Profile `json:"video"`
}

type UploadConfig struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"`
	APIKey  string `json:"api_key"`
}

type RequestOptions struct {
	ImageAspectRatio string `json:"image_aspect_ratio"`
	VideoAspectRatio string `json:"video_aspect_ratio"`
	VideoDuration    string `json:"video_duration"`
}

type Compatibility struct {
	RewriteImagesPath    bool `json:"rewrite_images_path"`
	RequestTimeoutSecond int  `json:"request_timeout_seconds"`
	LogRequestBody       bool `json:"log_request_body"`
	StreamChatUpstream   bool `json:"stream_chat_upstream"`
}

type LuoshuiIntegration struct {
	AutoConfigure bool `json:"auto_configure"`
}

type Config struct {
	Listen        ListenConfig       `json:"listen"`
	Profiles      Profiles           `json:"profiles"`
	Upload        UploadConfig       `json:"upload"`
	Options       RequestOptions     `json:"options"`
	Compatibility Compatibility      `json:"compatibility"`
	Luoshui       LuoshuiIntegration `json:"luoshui"`
	Update        UpdateConfig       `json:"update"`
}

type ConfigStore struct {
	path string
	mu   sync.RWMutex
	cfg  Config
}

func defaultConfig() Config {
	profile := func(model string) Profile {
		return Profile{Enabled: true, BaseURL: defaultUpstreamURL, Model: model}
	}
	return Config{
		Listen: ListenConfig{Host: "127.0.0.1", Ports: []int{5400, 8000}},
		Profiles: Profiles{
			Chat:  profile("your-chat-model"),
			Image: profile("your-image-model"),
			Video: profile("your-video-model"),
		},
		Upload:        UploadConfig{Enabled: true, URL: defaultUploadURL, APIKey: defaultUploadAPIKey},
		Options:       RequestOptions{ImageAspectRatio: "follow", VideoAspectRatio: "follow", VideoDuration: "15"},
		Compatibility: Compatibility{RewriteImagesPath: true, RequestTimeoutSecond: 600, StreamChatUpstream: true},
		Luoshui:       LuoshuiIntegration{AutoConfigure: true},
		Update:        UpdateConfig{Enabled: true, ManifestURL: defaultUpdateManifestURL, CheckOnStart: true},
	}
}

func NewConfigStore(path string) (*ConfigStore, error) {
	store := &ConfigStore{path: path}
	if err := store.Load(); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		store.cfg = defaultConfig()
		if err := store.Save(store.cfg); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (s *ConfigStore) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) == nil {
		if _, exists := raw["luoshui"]; !exists {
			cfg.Luoshui.AutoConfigure = true
		}
		var compatibility map[string]json.RawMessage
		if json.Unmarshal(raw["compatibility"], &compatibility) == nil {
			if _, exists := compatibility["stream_chat_upstream"]; !exists {
				cfg.Compatibility.StreamChatUpstream = true
			}
		}
	}
	normalizeConfig(&cfg)
	s.cfg = cfg
	return nil
}

func normalizeConfig(cfg *Config) {
	if !allowCustomURLsForTests {
		cfg.Profiles.Chat.BaseURL = defaultUpstreamURL
		cfg.Profiles.Image.BaseURL = defaultUpstreamURL
		cfg.Profiles.Video.BaseURL = defaultUpstreamURL
		cfg.Upload.URL = defaultUploadURL
	}
	if cfg.Listen.Host == "" {
		cfg.Listen.Host = "127.0.0.1"
	}
	if len(cfg.Listen.Ports) == 0 {
		cfg.Listen.Ports = []int{5400, 8000}
	}
	if cfg.Compatibility.RequestTimeoutSecond <= 0 {
		cfg.Compatibility.RequestTimeoutSecond = 600
	}
	if cfg.Options.ImageAspectRatio == "" {
		cfg.Options.ImageAspectRatio = "follow"
	}
	if cfg.Options.VideoAspectRatio == "" {
		cfg.Options.VideoAspectRatio = "follow"
	}
	if cfg.Options.VideoDuration == "" || cfg.Options.VideoDuration == "follow" {
		cfg.Options.VideoDuration = "15"
	}
	if cfg.Update.ManifestURL == "" {
		cfg.Update.ManifestURL = defaultUpdateManifestURL
	}
	missingUploadConfig := cfg.Upload.APIKey == ""
	if cfg.Upload.APIKey == "" {
		cfg.Upload.APIKey = defaultUploadAPIKey
	}
	if missingUploadConfig {
		cfg.Upload.Enabled = true
	}
}

func (s *ConfigStore) Get() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, _ := json.Marshal(s.cfg)
	var clone Config
	_ = json.Unmarshal(data, &clone)
	return clone
}

func (s *ConfigStore) Save(cfg Config) error {
	normalizeConfig(&cfg)
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return err
	}
	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()
	return nil
}
