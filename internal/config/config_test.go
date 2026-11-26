package config

import (
	"errors"
	"io/fs"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

const (
	pipe     = fs.ModeNamedPipe
	terminal = fs.ModeDevice | fs.ModeCharDevice
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{"DISCORD_TOKEN": "tok"}), pipe, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "tok" || cfg.Transport != TransportStdio || cfg.LogFile != DefaultLogFile || cfg.DefaultGuildID != "" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestDetectTransport(t *testing.T) {
	tests := []struct {
		name string
		mode fs.FileMode
		err  error
		want Transport
	}{
		{"pipe from an MCP client", pipe, nil, TransportStdio},
		{"socket", fs.ModeSocket, nil, TransportStdio},
		{"redirected file", 0, nil, TransportStdio},
		{"terminal", terminal, nil, TransportHTTP},
		{"/dev/null", fs.ModeDevice | fs.ModeCharDevice, nil, TransportHTTP},
		{"closed stdin", 0, errors.New("bad file descriptor"), TransportHTTP},
	}
	for _, tt := range tests {
		got, reason := DetectTransport(tt.mode, tt.err)
		if got != tt.want || reason == "" {
			t.Errorf("%s: got %q (%q), want %q", tt.name, got, reason, tt.want)
		}
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"DISCORD_GUILD_ID": " 123 ",
		"MCP_TRANSPORT":    "HTTP",
		"LOG_FILE":         "/tmp/x.log",
	}), pipe, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultGuildID != "123" || cfg.Transport != TransportHTTP || cfg.LogFile != "/tmp/x.log" {
		t.Errorf("unexpected config: %+v", cfg)
	}

	cfg, err = Load(env(map[string]string{"MCP_TRANSPORT": "stdio"}), terminal, nil)
	if err != nil || cfg.Transport != TransportStdio {
		t.Errorf("stdio override: %+v %v", cfg, err)
	}

	cfg, err = Load(env(map[string]string{"MCP_TRANSPORT": "auto"}), terminal, nil)
	if err != nil || cfg.Transport != TransportHTTP {
		t.Errorf("auto: %+v %v", cfg, err)
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load(env(map[string]string{"MCP_TRANSPORT": "sse"}), pipe, nil); err == nil {
		t.Error("unknown MCP_TRANSPORT should fail")
	}
}
