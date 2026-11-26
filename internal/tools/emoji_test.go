package tools

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

func TestEmojiNormalizeName(t *testing.T) {
	tests := []struct {
		in, want, err string
	}{
		{in: "party_parrot", want: "party_parrot"},
		{in: "party parrot", want: "party_parrot"},
		{in: "a \t b", want: "a_b"},
		{in: "", err: "Emoji name cannot be null or empty"},
		{in: "a", err: "Emoji name must be between 2 and 32 characters"},
		{in: strings.Repeat("x", 33), err: "Emoji name must be between 2 and 32 characters"},
		{in: strings.Repeat("x", 32), want: strings.Repeat("x", 32)},
		{in: "bad-name", err: "Emoji name can only contain letters, numbers, and underscores"},
		{in: "émoji", err: "Emoji name can only contain letters, numbers, and underscores"},
	}
	for _, tt := range tests {
		got, err := emojiNormalizeName(tt.in)
		if tt.err != "" {
			if err == nil || err.Error() != tt.err {
				t.Errorf("emojiNormalizeName(%q) error = %v, want %q", tt.in, err, tt.err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("emojiNormalizeName(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestEmojiDecodeDataURI(t *testing.T) {
	tests := []struct {
		in   string
		want []byte
		err  string
	}{
		{in: "data:image/png;base64,aGVsbG8=", want: []byte("hello")},
		{in: "aGVsbG8=", want: []byte("hello")},
		{in: "aGVsbG8", want: []byte("hello")},
		{in: "data:image/png;base64", err: "Invalid Data URI format. Expected: data:image/png;base64,<data>"},
		{in: "not base64!", err: "Invalid base64 image data"},
	}
	for _, tt := range tests {
		got, err := emojiDecodeDataURI(tt.in)
		if tt.err != "" {
			if err == nil || !strings.HasPrefix(err.Error(), tt.err) {
				t.Errorf("emojiDecodeDataURI(%q) error = %v, want prefix %q", tt.in, err, tt.err)
			}
			continue
		}
		if err != nil || !bytes.Equal(got, tt.want) {
			t.Errorf("emojiDecodeDataURI(%q) = %q, %v; want %q", tt.in, got, err, tt.want)
		}
	}
}

func TestEmojiResolveImageArguments(t *testing.T) {
	ctx := context.Background()
	if _, err := emojiResolveImage(ctx, "", ""); err == nil || err.Error() != "Either 'image' (base64) or 'imageUrl' (direct link) must be provided" {
		t.Errorf("no image: %v", err)
	}
	if _, err := emojiResolveImage(ctx, "aGVsbG8=", "https://example.com/a.png"); err == nil || err.Error() != "Provide only one of 'image' or 'imageUrl', not both" {
		t.Errorf("both: %v", err)
	}
}

func TestEmojiIsPublicAddr(t *testing.T) {
	tests := []struct {
		ip     string
		public bool
	}{
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"2606:4700:4700::1111", true},
		{"127.0.0.1", false},
		{"127.8.9.10", false},
		{"0.0.0.0", false},
		{"10.1.2.3", false},
		{"172.16.0.1", false},
		{"172.31.255.255", false},
		{"172.32.0.1", true},
		{"192.168.1.1", false},
		{"169.254.169.254", false},
		{"100.64.0.1", false},
		{"224.0.0.1", false},
		{"255.255.255.255", false},
		{"::1", false},
		{"::", false},
		{"fe80::1", false},
		{"fc00::1", false},
		{"fd12:3456::1", false},
		{"fec0::1", false},
		{"ff02::1", false},
		{"::ffff:127.0.0.1", false},
		{"::ffff:10.0.0.1", false},
		{"::ffff:8.8.8.8", true},
		{"64:ff9b::a9fe:a9fe", false},
		{"::7f00:1", false},
		{"2002:7f00:1::", false},
		{"2001:0:4136:e378:8000:63bf:3fff:fdd2", false},
		{"2001:4860:4860::8888", true},
	}
	for _, tt := range tests {
		if got := emojiIsPublicAddr(netip.MustParseAddr(tt.ip)); got != tt.public {
			t.Errorf("emojiIsPublicAddr(%s) = %v, want %v", tt.ip, got, tt.public)
		}
	}
}

func TestEmojiDialControl(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:443", "[::1]:443", "10.0.0.5:443", "169.254.169.254:80"} {
		if err := emojiDialControl("tcp", addr, nil); !errors.Is(err, errEmojiDisallowedAddress) {
			t.Errorf("emojiDialControl(%s) = %v, want disallowed", addr, err)
		}
	}
	if err := emojiDialControl("tcp", "8.8.8.8:443", nil); err != nil {
		t.Errorf("emojiDialControl(public) = %v", err)
	}
}

func TestEmojiDownloadImageRejects(t *testing.T) {
	orig := emojiLookupHost
	defer func() { emojiLookupHost = orig }()
	emojiLookupHost = func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "internal.example":
			return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.1")}, nil
		default:
			return nil, errors.New("no such host")
		}
	}

	ctx := context.Background()
	tests := []struct{ url, want string }{
		{"http://example.com/a.png", "Image URL must use the https scheme"},
		{"ftp://example.com/a.png", "Image URL must use the https scheme"},
		{"https:///a.png", "Image URL must include a host"},
		{"https://127.0.0.1/a.png", "Image URL resolves to a disallowed (internal) address"},
		{"https://[::1]/a.png", "Image URL resolves to a disallowed (internal) address"},
		{"https://internal.example/a.png", "Image URL resolves to a disallowed (internal) address"},
		{"https://missing.example/a.png", "Cannot resolve image host: missing.example"},
	}
	for _, tt := range tests {
		_, err := emojiDownloadImage(ctx, tt.url)
		if err == nil || err.Error() != tt.want {
			t.Errorf("emojiDownloadImage(%q) error = %v, want %q", tt.url, err, tt.want)
		}
	}
	if _, err := emojiDownloadImage(ctx, "https://exa mple.com/%zz"); err == nil || !strings.HasPrefix(err.Error(), "Invalid image URL: ") {
		t.Errorf("malformed URL error = %v", err)
	}
}

func TestEmojiDataURI(t *testing.T) {
	png := []byte("\x89PNG\r\n\x1a\n0000")
	if got := emojiDataURI(png); !strings.HasPrefix(got, "data:image/png;base64,") {
		t.Errorf("png data URI = %q", got)
	}
	gif := []byte("GIF89a000000")
	if got := emojiDataURI(gif); !strings.HasPrefix(got, "data:image/gif;base64,") {
		t.Errorf("gif data URI = %q", got)
	}
}
