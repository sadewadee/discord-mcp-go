package config

import "testing"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(map[string]string{"DISCORD_TOKEN": "tok"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "tok" || cfg.Transport != TransportStdio || cfg.Port != DefaultPort || cfg.LogFile != DefaultLogFile || cfg.DefaultGuildID != "" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"DISCORD_GUILD_ID": " 123 ",
		"MCP_TRANSPORT":    "HTTP",
		"PORT":             "9000",
		"LOG_FILE":         "/tmp/x.log",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultGuildID != "123" || cfg.Transport != TransportHTTP || cfg.Port != 9000 || cfg.LogFile != "/tmp/x.log" {
		t.Errorf("unexpected config: %+v", cfg)
	}
}

func TestLoadErrors(t *testing.T) {
	for _, m := range []map[string]string{
		{"MCP_TRANSPORT": "sse"},
		{"PORT": "abc"},
		{"PORT": "0"},
		{"PORT": "70000"},
	} {
		if _, err := Load(env(m)); err == nil {
			t.Errorf("Load(%v) should fail", m)
		}
	}
}
