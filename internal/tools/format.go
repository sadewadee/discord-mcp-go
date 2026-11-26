package tools

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

// discordEpochMs is the first millisecond of 2015, the Discord snowflake epoch.
const discordEpochMs = 1420070400000

// snowflakeTime returns the creation time encoded in a Discord ID, in UTC.
// It returns the zero time for an invalid ID.
func snowflakeTime(id string) time.Time {
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.UnixMilli(int64(n>>22) + discordEpochMs).UTC()
}

var snowflakeRe = regexp.MustCompile(`^\d{17,20}$`)

// isSnowflake reports whether s looks like a Discord ID (17 to 20 digits).
func isSnowflake(s string) bool { return snowflakeRe.MatchString(s) }

// formatTimestamp renders t in ISO-8601 offset form: "2024-05-01T12:34:56.789Z".
// Seconds are omitted when they and the fraction are zero ("12:34Z"), and the
// fraction uses 3, 6 or 9 digits, whichever is the shortest exact form. A UTC
// time ends in "Z"; any other offset is written as "+hh:mm".
func formatTimestamp(t time.Time) string {
	var b strings.Builder
	b.WriteString(formatDate(t))
	b.WriteByte('T')
	fmt.Fprintf(&b, "%02d:%02d", t.Hour(), t.Minute())
	sec, nanos := t.Second(), t.Nanosecond()
	if sec > 0 || nanos > 0 {
		fmt.Fprintf(&b, ":%02d", sec)
		switch {
		case nanos == 0:
		case nanos%1_000_000 == 0:
			fmt.Fprintf(&b, ".%03d", nanos/1_000_000)
		case nanos%1_000 == 0:
			fmt.Fprintf(&b, ".%06d", nanos/1_000)
		default:
			fmt.Fprintf(&b, ".%09d", nanos)
		}
	}
	_, offset := t.Zone()
	if offset == 0 {
		b.WriteByte('Z')
	} else {
		sign := '+'
		if offset < 0 {
			sign = '-'
			offset = -offset
		}
		fmt.Fprintf(&b, "%c%02d:%02d", sign, offset/3600, (offset%3600)/60)
		if s := offset % 60; s != 0 {
			fmt.Fprintf(&b, ":%02d", s)
		}
	}
	return b.String()
}

// formatDate renders the date part of t as yyyy-mm-dd.
func formatDate(t time.Time) string {
	return fmt.Sprintf("%04d-%02d-%02d", t.Year(), int(t.Month()), t.Day())
}

// formatFileSize renders a byte count as "512 B", "1.5 KB", "2.0 MB" or "1.1 GB".
func formatFileSize(bytes int) string {
	switch {
	case bytes < 1024:
		return fmt.Sprintf("%d B", bytes)
	case bytes < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024.0)
	case bytes < 1024*1024*1024:
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024.0*1024))
	default:
		return fmt.Sprintf("%.1f GB", float64(bytes)/(1024.0*1024*1024))
	}
}

// orNone returns "none" for an empty or blank value.
func orNone(s string) string {
	if strings.TrimSpace(s) == "" {
		return "none"
	}
	return s
}

// isBlank reports whether s is empty or only whitespace.
func isBlank(s string) bool { return strings.TrimSpace(s) == "" }

// userEffectiveName is the user's global display name, or the username.
func userEffectiveName(u *discordgo.User) string {
	if u == nil {
		return ""
	}
	if u.GlobalName != "" {
		return u.GlobalName
	}
	return u.Username
}

// memberEffectiveName is the nickname, then global name, then username.
func memberEffectiveName(m *discordgo.Member) string {
	if m == nil {
		return ""
	}
	if m.Nick != "" {
		return m.Nick
	}
	return userEffectiveName(m.User)
}

// discriminator returns the user's discriminator as four digits. Users on the
// new username system have discriminator "0", shown as "0000".
func discriminator(u *discordgo.User) string {
	if u == nil {
		return "0000"
	}
	n, err := strconv.Atoi(u.Discriminator)
	if err != nil {
		return "0000"
	}
	return fmt.Sprintf("%04d", n)
}

// boolText renders a bool as "true" or "false".
func boolText(b bool) string { return strconv.FormatBool(b) }
