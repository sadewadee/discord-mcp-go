package tools

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
)

const (
	emojiMaxBytes       = 256 * 1024
	emojiConnectTimeout = 5 * time.Second
	emojiReadTimeout    = 10 * time.Second
)

func (t *Toolset) emojiTools() []Tool {
	return []Tool{
		{
			Name:        "list_emojis",
			Description: "List all custom emojis on the server",
			Params:      []Param{opt("guildId", "Discord server ID")},
			Handler:     t.listEmojis,
		},
		{
			Name:        "get_emoji_details",
			Description: "Get detailed information about a specific custom emoji",
			Params:      []Param{opt("guildId", "Discord server ID"), req("emojiId", "ID of the emoji")},
			Handler:     t.getEmojiDetails,
		},
		{
			Name:        "create_emoji",
			Description: "Upload a new custom emoji to the server. Provide image as base64 (Data URI or raw) OR a direct image URL. Max 256KB",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("name", "Emoji name (2-32 chars, alphanumeric and underscores only)"),
				opt("image", "Image as base64 Data URI (e.g. data:image/png;base64,...) or raw base64 string"),
				opt("imageUrl", "Direct URL to the image file (alternative to base64 image)"),
				opt("roles", "Comma-separated role IDs to restrict emoji usage (empty = everyone)"),
			},
			Handler: t.createEmoji,
		},
		{
			Name:        "edit_emoji",
			Description: "Edit an existing emoji's name or role restrictions",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("emojiId", "ID of the emoji to edit"),
				opt("name", "New name for the emoji"),
				opt("roles", "Comma-separated role IDs to restrict usage (empty string = unrestrict for everyone)"),
			},
			Handler: t.editEmoji,
		},
		{
			Name:        "delete_emoji",
			Description: "Permanently delete a custom emoji from the server",
			Params:      []Param{opt("guildId", "Discord server ID"), req("emojiId", "ID of the emoji to delete")},
			Handler:     t.deleteEmoji,
		},
	}
}

// emojiImageURL is the CDN URL of a custom emoji's image.
func emojiImageURL(e *discordgo.Emoji) string {
	if e.Animated {
		return discordgo.EndpointEmojiAnimated(e.ID)
	}
	return discordgo.EndpointEmoji(e.ID)
}

// emojiRoles resolves an emoji's role restrictions against the cache,
// skipping roles that are not cached.
func (t *Toolset) emojiRoles(g *discordgo.Guild, e *discordgo.Emoji) []*discordgo.Role {
	var out []*discordgo.Role
	for _, id := range e.Roles {
		if r := t.cachedRole(g, id); r != nil {
			out = append(out, r)
		}
	}
	return out
}

func (t *Toolset) listEmojis(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	emojis, err := t.s.GuildEmojis(g.ID)
	if err != nil {
		return "", err
	}
	if len(emojis) == 0 {
		return "No custom emojis found on this server.", nil
	}
	lines := make([]string, 0, len(emojis))
	for _, e := range emojis {
		line := "- **" + e.Name + "** (ID: " + e.ID + ") | Animated: " + boolText(e.Animated)
		if roles := t.emojiRoles(g, e); len(roles) > 0 {
			names := make([]string, len(roles))
			for i, r := range roles {
				names[i] = r.Name
			}
			line += " | Roles: " + strings.Join(names, ", ")
		}
		lines = append(lines, line)
	}
	return "Retrieved " + strconv.Itoa(len(emojis)) + " custom emojis:\n" + strings.Join(lines, "\n"), nil
}

func (t *Toolset) getEmojiDetails(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	emojiID := a.Get("emojiId")
	if emojiID == "" {
		return "", errors.New("emojiId cannot be null")
	}
	e, err := t.s.GuildEmoji(g.ID, emojiID)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	b.WriteString("Emoji: **" + e.Name + "** (ID: " + e.ID + ")\n")
	b.WriteString("  • Animated: " + boolText(e.Animated) + "\n")
	b.WriteString("  • Managed: " + boolText(e.Managed) + "\n")
	b.WriteString("  • Available: " + boolText(e.Available) + "\n")
	b.WriteString("  • Image URL: " + emojiImageURL(e))
	if roles := t.emojiRoles(g, e); len(roles) > 0 {
		parts := make([]string, len(roles))
		for i, r := range roles {
			parts[i] = r.Name + " (ID: " + r.ID + ")"
		}
		b.WriteString("\n  • Restricted to roles: " + strings.Join(parts, ", "))
	}
	return b.String(), nil
}

func (t *Toolset) createEmoji(ctx context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	name, err := emojiNormalizeName(a.Get("name"))
	if err != nil {
		return "", err
	}
	data, err := emojiResolveImage(ctx, a.Get("image"), a.Get("imageUrl"))
	if err != nil {
		return "", err
	}
	if len(data) > emojiMaxBytes {
		return "", fmt.Errorf("Image exceeds 256 KB limit (%d KB). Please compress or resize the image.", len(data)/1024)
	}
	roles, err := t.emojiParseRoles(g, a.Get("roles"), true)
	if err != nil {
		return "", err
	}
	e, err := t.s.GuildEmojiCreate(g.ID, &discordgo.EmojiParams{
		Name:  name,
		Image: emojiDataURI(data),
		Roles: roles,
	})
	if err != nil {
		return "", err
	}
	return "Created emoji **" + e.Name + "** (ID: " + e.ID + ")" +
		"\n  • Animated: " + boolText(e.Animated) +
		"\n  • Image URL: " + emojiImageURL(e), nil
}

func (t *Toolset) editEmoji(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	emojiID := a.Get("emojiId")
	if emojiID == "" {
		return "", errors.New("emojiId cannot be null")
	}
	if _, err := t.s.GuildEmoji(g.ID, emojiID); err != nil {
		return "", err
	}

	patch := map[string]any{}
	if name := a.Get("name"); name != "" {
		normalized, err := emojiNormalizeName(name)
		if err != nil {
			return "", err
		}
		patch["name"] = normalized
	}
	if a.Has("roles") {
		roles, err := t.emojiParseRoles(g, a.Get("roles"), false)
		if err != nil {
			return "", err
		}
		if roles == nil {
			roles = []string{} // an empty list makes the emoji usable by everyone
		}
		patch["roles"] = roles
	}
	if len(patch) > 0 {
		endpoint := discordgo.EndpointGuildEmoji(g.ID, emojiID)
		if _, err := t.s.RequestWithBucketID(http.MethodPatch, endpoint, patch, discordgo.EndpointGuildEmojis(g.ID)); err != nil {
			return "", err
		}
	}

	updated, err := t.s.GuildEmoji(g.ID, emojiID)
	if err != nil {
		return "", err
	}
	return "Updated emoji **" + updated.Name + "** (ID: " + updated.ID + ")", nil
}

func (t *Toolset) deleteEmoji(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	emojiID := a.Get("emojiId")
	if emojiID == "" {
		return "", errors.New("emojiId cannot be null")
	}
	e, err := t.s.GuildEmoji(g.ID, emojiID)
	if err != nil {
		return "", err
	}
	if err := t.s.GuildEmojiDelete(g.ID, emojiID); err != nil {
		return "", err
	}
	return "Deleted emoji **" + e.Name + "** (ID: " + emojiID + ")", nil
}

// emojiParseRoles turns a comma-separated list of role IDs into the IDs of
// cached roles. When skipEmpty is set, empty entries are ignored; otherwise
// every entry must name a role. An empty list returns nil.
func (t *Toolset) emojiParseRoles(g *discordgo.Guild, roles string, skipEmpty bool) ([]string, error) {
	if roles == "" {
		return nil, nil
	}
	var ids []string
	for _, id := range strings.Split(roles, ",") {
		id = strings.TrimSpace(id)
		if id == "" && skipEmpty {
			continue
		}
		r := t.cachedRole(g, id)
		if r == nil {
			return nil, errors.New("Role not found: " + id)
		}
		ids = append(ids, r.ID)
	}
	return ids, nil
}

var (
	emojiWhitespaceRe = regexp.MustCompile(`\s+`)
	emojiNameRe       = regexp.MustCompile(`^[a-zA-Z0-9_]+$`)
)

// emojiNormalizeName replaces whitespace runs with underscores and checks the
// name is 2-32 letters, digits or underscores.
func emojiNormalizeName(name string) (string, error) {
	if name == "" {
		return "", errors.New("Emoji name cannot be null or empty")
	}
	name = emojiWhitespaceRe.ReplaceAllString(name, "_")
	if n := len([]rune(name)); n < 2 || n > 32 {
		return "", errors.New("Emoji name must be between 2 and 32 characters")
	}
	if !emojiNameRe.MatchString(name) {
		return "", errors.New("Emoji name can only contain letters, numbers, and underscores")
	}
	return name, nil
}

// emojiResolveImage returns the image bytes from exactly one of image
// (base64 or data URI) and imageURL (an https link).
func emojiResolveImage(ctx context.Context, image, imageURL string) ([]byte, error) {
	hasImage, hasURL := image != "", imageURL != ""
	if !hasImage && !hasURL {
		return nil, errors.New("Either 'image' (base64) or 'imageUrl' (direct link) must be provided")
	}
	if hasImage && hasURL {
		return nil, errors.New("Provide only one of 'image' or 'imageUrl', not both")
	}
	if hasURL {
		return emojiDownloadImage(ctx, imageURL)
	}
	return emojiDecodeDataURI(image)
}

// emojiDecodeDataURI decodes "data:<type>;base64,<data>" or raw base64.
// Padding is optional.
func emojiDecodeDataURI(dataURI string) ([]byte, error) {
	data := dataURI
	if strings.HasPrefix(dataURI, "data:") {
		i := strings.Index(dataURI, ",")
		if i < 0 {
			return nil, errors.New("Invalid Data URI format. Expected: data:image/png;base64,<data>")
		}
		data = dataURI[i+1:]
	}
	if b, err := base64.StdEncoding.DecodeString(data); err == nil {
		return b, nil
	}
	b, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(data, "="))
	if err != nil {
		return nil, fmt.Errorf("Invalid base64 image data: %v", err)
	}
	return b, nil
}

// emojiDataURI encodes image bytes as a data URI, detecting the image type.
func emojiDataURI(data []byte) string {
	contentType := http.DetectContentType(data)
	switch contentType {
	case "image/png", "image/gif", "image/jpeg", "image/webp":
	default:
		contentType = "image/png"
	}
	return "data:" + contentType + ";base64," + base64.StdEncoding.EncodeToString(data)
}

// errEmojiDisallowedAddress is returned when an image host is not public.
var errEmojiDisallowedAddress = errors.New("Image URL resolves to a disallowed (internal) address")

// emojiLookupHost resolves a host name; tests replace it.
var emojiLookupHost = func(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// emojiDialControl runs just before each outgoing connection and refuses
// any address that is not public. Checking here, rather than only before
// the request, means a DNS answer that changes between the check and the
// connection cannot reach an internal address.
func emojiDialControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return errEmojiDisallowedAddress
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !emojiIsPublicAddr(ip) {
		return errEmojiDisallowedAddress
	}
	return nil
}

// emojiHTTPClient downloads emoji images. It connects directly, never
// through a proxy (a proxy would resolve the host itself and defeat the
// address check), and does not follow redirects, since a redirect could
// point at an internal address.
var emojiHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: emojiConnectTimeout, Control: emojiDialControl}).DialContext,
		TLSHandshakeTimeout:   emojiConnectTimeout,
		ResponseHeaderTimeout: emojiReadTimeout,
		MaxIdleConns:          2,
		IdleConnTimeout:       30 * time.Second,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	Timeout:       emojiConnectTimeout + 2*emojiReadTimeout,
}

// emojiDownloadImage fetches an https image URL whose host resolves only to
// public addresses, reading at most emojiMaxBytes.
func emojiDownloadImage(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, errors.New("Invalid image URL: " + emojiURLErrorText(err))
	}
	if !strings.EqualFold(u.Scheme, "https") {
		return nil, errors.New("Image URL must use the https scheme")
	}
	host := u.Hostname()
	if host == "" {
		return nil, errors.New("Image URL must include a host")
	}
	if err := emojiAssertHostIsPublic(ctx, host); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, errors.New("Invalid image URL: " + emojiURLErrorText(err))
	}
	resp, err := emojiHTTPClient.Do(req)
	if err != nil {
		if errors.Is(err, errEmojiDisallowedAddress) {
			return nil, errEmojiDisallowedAddress
		}
		return nil, errors.New("Failed to download image from URL: " + emojiURLErrorText(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		hint := ""
		if resp.StatusCode/100 == 3 {
			hint = " Provide a direct image link (redirects are not followed)."
		}
		return nil, fmt.Errorf("Image URL returned HTTP %d.%s", resp.StatusCode, hint)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, emojiMaxBytes+1))
	if err != nil {
		return nil, errors.New("Failed to download image from URL: " + emojiURLErrorText(err))
	}
	if len(data) > emojiMaxBytes {
		return nil, errors.New("Image exceeds 256 KB limit. Please compress or resize the image.")
	}
	return data, nil
}

// emojiURLErrorText drops the `<method> "<url>":` prefix of net/url errors.
func emojiURLErrorText(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err.Error()
	}
	return err.Error()
}

// emojiAssertHostIsPublic resolves host and fails unless every address it
// resolves to is public.
func emojiAssertHostIsPublic(ctx context.Context, host string) error {
	addrs := []netip.Addr{}
	if ip, err := netip.ParseAddr(host); err == nil {
		addrs = append(addrs, ip)
	} else {
		addrs, err = emojiLookupHost(ctx, host)
		if err != nil || len(addrs) == 0 {
			return errors.New("Cannot resolve image host: " + host)
		}
	}
	for _, ip := range addrs {
		if !emojiIsPublicAddr(ip) {
			return errEmojiDisallowedAddress
		}
	}
	return nil
}

// emojiBlockedPrefixes are ranges that are never public and that the netip
// predicates do not cover: "this network", carrier-grade NAT, IETF protocol
// assignments, documentation, benchmarking, reserved, NAT64, deprecated
// IPv6 site-local ranges, and the IPv6 ranges that embed an IPv4 address
// (IPv4-compatible, 6to4 and Teredo), which could otherwise tunnel to an
// internal IPv4 host.
var emojiBlockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("fec0::/10"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("2001::/32"),
}

// emojiIsPublicAddr reports whether ip is a public unicast address: not
// loopback, unspecified, link-local, private (including the IPv6
// unique-local range fc00::/7), multicast, or in emojiBlockedPrefixes.
// IPv4-mapped IPv6 addresses are judged by their IPv4 address.
func emojiIsPublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	if !ip.IsValid() || ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() ||
		ip.IsMulticast() {
		return false
	}
	for _, p := range emojiBlockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
