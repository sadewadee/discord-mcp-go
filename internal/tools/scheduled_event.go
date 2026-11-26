package tools

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
)

var errEventNotFound = errors.New("Scheduled event not found by eventId")

func (t *Toolset) scheduledEventTools() []Tool {
	return []Tool{
		{
			Name:        "create_guild_scheduled_event",
			Description: "Schedule a new event on the server (voice, stage, or external)",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("name", "Name of the event"),
				opt("description", "Description of the event"),
				req("scheduledStartTime", "ISO8601 timestamp for when the event starts"),
				opt("scheduledEndTime", "ISO8601 timestamp for when the event ends (Required for External events)"),
				req("entityType", "Type of event: 1=Stage Instance, 2=Voice, 3=External"),
				opt("channelId", "Channel ID (Required for types 1 and 2)"),
				opt("location", "Location or link (Required for type 3 - External)"),
			},
			Handler: t.createScheduledEvent,
		},
		{
			Name:        "edit_guild_scheduled_event",
			Description: "Modify details of an existing event or change its status (start, complete, cancel)",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("eventId", "ID of the scheduled event"),
				opt("status", "New status: 1=Scheduled, 2=Active (start), 3=Completed, 4=Canceled"),
				opt("name", "New name"),
				opt("description", "New description"),
				opt("scheduledStartTime", "New ISO8601 start time"),
				opt("location", "New location (for External events)"),
			},
			Handler: t.editScheduledEvent,
		},
		{
			Name:        "delete_guild_scheduled_event",
			Description: "Permanently delete a scheduled event",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("eventId", "ID of the scheduled event"),
			},
			Handler: t.deleteScheduledEvent,
		},
		{
			Name:        "list_guild_scheduled_events",
			Description: "List all active and scheduled events on the server",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				opt("withUserCount", "Whether to include interested user count (default true)"),
			},
			Handler: t.listScheduledEvents,
		},
		{
			Name:        "get_guild_scheduled_event_users",
			Description: "Get list of users interested in a scheduled event",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("eventId", "ID of the scheduled event"),
				opt("limit", "Max number of users to return (default 100)"),
				opt("withMember", "Whether to include full member data with roles (default true)"),
			},
			Handler: t.getScheduledEventUsers,
		},
	}
}

// eventTimeLayouts are the accepted ISO-8601 forms: date, time with optional
// seconds and fraction, and a "Z" or "+hh:mm" offset.
var eventTimeLayouts = []string{
	"2006-01-02T15:04:05.999999999Z07:00",
	"2006-01-02T15:04Z07:00",
}

// eventParseTime parses an ISO-8601 timestamp with an offset, such as
// "2025-01-31T18:00:00Z" or "2025-01-31T18:00+07:00".
func eventParseTime(s string) (time.Time, error) {
	for _, layout := range eventTimeLayouts {
		if tm, err := time.Parse(layout, s); err == nil {
			return tm, nil
		}
	}
	return time.Time{}, errors.New("Invalid ISO8601 timestamp: " + s)
}

// eventTypeName names a scheduled event's entity type.
func eventTypeName(ty discordgo.GuildScheduledEventEntityType) string {
	switch ty {
	case discordgo.GuildScheduledEventEntityTypeStageInstance:
		return "STAGE_INSTANCE"
	case discordgo.GuildScheduledEventEntityTypeVoice:
		return "VOICE"
	case discordgo.GuildScheduledEventEntityTypeExternal:
		return "EXTERNAL"
	default:
		return "UNKNOWN"
	}
}

// eventStatusName names a scheduled event's status.
func eventStatusName(st discordgo.GuildScheduledEventStatus) string {
	switch st {
	case discordgo.GuildScheduledEventStatusScheduled:
		return "SCHEDULED"
	case discordgo.GuildScheduledEventStatusActive:
		return "ACTIVE"
	case discordgo.GuildScheduledEventStatusCompleted:
		return "COMPLETED"
	case discordgo.GuildScheduledEventStatusCanceled:
		return "CANCELED"
	default:
		return "UNKNOWN"
	}
}

// formatScheduledEvent renders an event in detail. channel is the event's
// cached channel, or nil.
func formatScheduledEvent(e *discordgo.GuildScheduledEvent, channel *discordgo.Channel) string {
	var b strings.Builder
	b.WriteString("**" + e.Name + "** (ID: " + e.ID + ")\n")
	b.WriteString("  • Type: " + eventTypeName(e.EntityType) + "\n")
	b.WriteString("  • Status: " + eventStatusName(e.Status) + "\n")
	b.WriteString("  • Start: " + formatTimestamp(e.ScheduledStartTime))
	if e.ScheduledEndTime != nil {
		b.WriteString("\n  • End: " + formatTimestamp(*e.ScheduledEndTime))
	}
	if channel != nil {
		b.WriteString("\n  • Channel: " + channel.Name + " (ID: " + channel.ID + ")")
	}
	if e.EntityMetadata.Location != "" {
		b.WriteString("\n  • Location: " + e.EntityMetadata.Location)
	}
	if e.Description != "" {
		b.WriteString("\n  • Description: " + e.Description)
	}
	b.WriteString("\n  • Interested: " + strconv.Itoa(e.UserCount) + " users")
	return b.String()
}

// formatScheduledEventListItem renders an event as a list entry.
func formatScheduledEventListItem(e *discordgo.GuildScheduledEvent, includeUserCount bool) string {
	var b strings.Builder
	b.WriteString("- **" + e.Name + "** (ID: " + e.ID + ")\n")
	b.WriteString("  • Type: " + eventTypeName(e.EntityType) + " | Status: " + eventStatusName(e.Status) + "\n")
	b.WriteString("  • Start: " + formatTimestamp(e.ScheduledStartTime))
	if e.ScheduledEndTime != nil {
		b.WriteString(" | End: " + formatTimestamp(*e.ScheduledEndTime))
	}
	if includeUserCount {
		b.WriteString("\n  • Interested: " + strconv.Itoa(e.UserCount) + " users")
	}
	return b.String()
}

// eventChannel returns the event's cached channel, or nil.
func (t *Toolset) eventChannel(e *discordgo.GuildScheduledEvent) *discordgo.Channel {
	if e.ChannelID == "" {
		return nil
	}
	return t.cachedChannel(e.ChannelID)
}

// eventByID fetches a scheduled event of guild g.
func (t *Toolset) eventByID(g *discordgo.Guild, eventID string) (*discordgo.GuildScheduledEvent, error) {
	if eventID == "" {
		return nil, errors.New("eventId cannot be null")
	}
	e, err := t.s.GuildScheduledEvent(g.ID, eventID, true)
	if err != nil {
		var rest *discordgo.RESTError
		if errors.As(err, &rest) && rest.Response != nil &&
			(rest.Response.StatusCode == http.StatusNotFound || rest.Response.StatusCode == http.StatusBadRequest) {
			return nil, errEventNotFound
		}
		return nil, err
	}
	if e == nil {
		return nil, errEventNotFound
	}
	return e, nil
}

func (t *Toolset) createScheduledEvent(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	name := a.Get("name")
	if name == "" {
		return "", errors.New("name cannot be null")
	}
	if a.Get("entityType") == "" {
		return "", errors.New("entityType cannot be null")
	}
	entityType, err := modParseInt32(a.Get("entityType"))
	if err != nil {
		return "", err
	}
	if a.Get("scheduledStartTime") == "" {
		return "", errors.New("scheduledStartTime cannot be null")
	}
	start, err := eventParseTime(a.Get("scheduledStartTime"))
	if err != nil {
		return "", err
	}

	params := &discordgo.GuildScheduledEventParams{
		Name:               name,
		ScheduledStartTime: &start,
		PrivacyLevel:       discordgo.GuildScheduledEventPrivacyLevelGuildOnly,
	}
	endRaw := a.Get("scheduledEndTime")
	switch entityType {
	case 1, 2:
		channelID := a.Get("channelId")
		if channelID == "" {
			return "", errors.New("channelId is required for Stage and Voice events")
		}
		ch := t.cachedChannel(channelID)
		if ch == nil || ch.GuildID != g.ID {
			return "", errors.New("Channel not found by channelId")
		}
		// The event type follows the channel: a stage channel hosts a stage
		// event and a voice channel a voice event.
		switch ch.Type {
		case discordgo.ChannelTypeGuildStageVoice:
			params.EntityType = discordgo.GuildScheduledEventEntityTypeStageInstance
		case discordgo.ChannelTypeGuildVoice:
			params.EntityType = discordgo.GuildScheduledEventEntityTypeVoice
		default:
			return "", errors.New("Invalid parameter: Can only set location to Voice and Stage Channels!")
		}
		params.ChannelID = ch.ID
	case 3:
		location := a.Get("location")
		if location == "" {
			return "", errors.New("location is required for External events")
		}
		if endRaw == "" {
			return "", errors.New("scheduledEndTime is required for External events")
		}
		end, err := eventParseTime(endRaw)
		if err != nil {
			return "", err
		}
		params.EntityType = discordgo.GuildScheduledEventEntityTypeExternal
		params.EntityMetadata = &discordgo.GuildScheduledEventEntityMetadata{Location: location}
		params.ScheduledEndTime = &end
	default:
		return "", errors.New("entityType must be 1 (Stage), 2 (Voice), or 3 (External)")
	}

	if d := a.Get("description"); d != "" {
		params.Description = d
	}
	if entityType != 3 && endRaw != "" {
		end, err := eventParseTime(endRaw)
		if err != nil {
			return "", err
		}
		params.ScheduledEndTime = &end
	}

	e, err := t.s.GuildScheduledEventCreate(g.ID, params)
	if err != nil {
		return "", err
	}
	// The create response carries no interested-user count; -1 marks it as unknown.
	e.UserCount = -1
	return "Created scheduled event:\n" + formatScheduledEvent(e, t.eventChannel(e)), nil
}

// eventStatusFromArg maps the status argument (1-4) to an event status.
func eventStatusFromArg(raw string) (discordgo.GuildScheduledEventStatus, error) {
	n, err := modParseInt32(raw)
	if err != nil {
		return 0, err
	}
	switch n {
	case 1:
		return discordgo.GuildScheduledEventStatusScheduled, nil
	case 2:
		return discordgo.GuildScheduledEventStatusActive, nil
	case 3:
		return discordgo.GuildScheduledEventStatusCompleted, nil
	case 4:
		return discordgo.GuildScheduledEventStatusCanceled, nil
	default:
		return 0, errors.New("status must be 1 (Scheduled), 2 (Active), 3 (Completed), or 4 (Canceled)")
	}
}

func (t *Toolset) editScheduledEvent(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	e, err := t.eventByID(g, a.Get("eventId"))
	if err != nil {
		return "", err
	}

	body := map[string]any{}
	if v := a.Get("name"); v != "" {
		body["name"] = v
	}
	if v := a.Get("description"); v != "" {
		body["description"] = v
	}
	if v := a.Get("scheduledStartTime"); v != "" {
		start, err := eventParseTime(v)
		if err != nil {
			return "", err
		}
		body["scheduled_start_time"] = start
	}
	if v := a.Get("location"); v != "" {
		// Setting a location turns the event into an external event.
		body["entity_type"] = discordgo.GuildScheduledEventEntityTypeExternal
		body["entity_metadata"] = map[string]string{"location": v}
		body["channel_id"] = nil
	}
	if v := a.Get("status"); v != "" {
		status, err := eventStatusFromArg(v)
		if err != nil {
			return "", err
		}
		body["status"] = status
	}

	updated := e
	if len(body) > 0 {
		endpoint := discordgo.EndpointGuildScheduledEvent(g.ID, e.ID)
		resp, err := t.s.RequestWithBucketID("PATCH", endpoint, body, endpoint)
		if err != nil {
			return "", err
		}
		var ev discordgo.GuildScheduledEvent
		if err := discordgo.Unmarshal(resp, &ev); err == nil && ev.ID != "" {
			updated = &ev
		}
	}
	return "Successfully updated scheduled event: " + updated.Name + " (ID: " + updated.ID + ")", nil
}

func (t *Toolset) deleteScheduledEvent(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	e, err := t.eventByID(g, a.Get("eventId"))
	if err != nil {
		return "", err
	}
	name := e.Name
	if err := t.s.GuildScheduledEventDelete(g.ID, e.ID); err != nil {
		return "", err
	}
	return "Successfully deleted scheduled event: " + name + " (ID: " + a.Get("eventId") + ")", nil
}

// eventDefaultTrue is true for an empty argument, otherwise true only for
// "true" in any letter case.
func eventDefaultTrue(v string) bool {
	return v == "" || modParseBool(v)
}

// sortScheduledEvents orders events by start time, then by ID.
func sortScheduledEvents(events []*discordgo.GuildScheduledEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if !a.ScheduledStartTime.Equal(b.ScheduledStartTime) {
			return a.ScheduledStartTime.Before(b.ScheduledStartTime)
		}
		return idLess(a.ID, b.ID)
	})
}

func (t *Toolset) listScheduledEvents(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	events, err := t.s.GuildScheduledEvents(g.ID, true)
	if err != nil {
		return "", err
	}
	if len(events) == 0 {
		return "No scheduled events found on this server.", nil
	}
	sortScheduledEvents(events)
	includeUserCount := eventDefaultTrue(a.Get("withUserCount"))
	lines := make([]string, 0, len(events))
	for _, e := range events {
		lines = append(lines, formatScheduledEventListItem(e, includeUserCount))
	}
	return fmt.Sprintf("Retrieved %d scheduled events:\n%s", len(events), strings.Join(lines, "\n")), nil
}

func (t *Toolset) getScheduledEventUsers(_ context.Context, a Args) (string, error) {
	g, err := t.guild(a.Get("guildId"), "guildId cannot be null")
	if err != nil {
		return "", err
	}
	e, err := t.eventByID(g, a.Get("eventId"))
	if err != nil {
		return "", err
	}
	maxResults := 100
	if v := a.Get("limit"); v != "" {
		if maxResults, err = modParseInt32(v); err != nil {
			return "", err
		}
		if maxResults < 0 {
			return "", errors.New(strconv.Itoa(maxResults))
		}
	}
	includeMember := eventDefaultTrue(a.Get("withMember"))

	var members []*discordgo.Member
	after := ""
	for len(members) < maxResults {
		page, err := t.s.GuildScheduledEventUsers(g.ID, e.ID, 100, true, "", after)
		if err != nil {
			return "", err
		}
		if len(page) == 0 {
			break
		}
		sort.SliceStable(page, func(i, j int) bool { return idLess(eventUserID(page[i]), eventUserID(page[j])) })
		for _, u := range page {
			if u.Member == nil {
				continue
			}
			if u.Member.User == nil {
				u.Member.User = u.User
			}
			if u.Member.User == nil {
				continue
			}
			members = append(members, u.Member)
			if len(members) >= maxResults {
				break
			}
		}
		after = eventUserID(page[len(page)-1])
		if len(page) < 100 || after == "" {
			break
		}
	}

	if len(members) == 0 {
		return "No interested users found for event: " + e.Name, nil
	}
	lines := make([]string, 0, len(members))
	for _, m := range members {
		var b strings.Builder
		b.WriteString("- **" + m.User.Username + "** (ID: " + m.User.ID + ")")
		if includeMember {
			var roles []string
			for _, r := range t.memberRoles(g, m) {
				roles = append(roles, r.Name+" ("+r.ID+")")
			}
			if len(roles) > 0 {
				b.WriteString("\n  • Roles: " + strings.Join(roles, ", "))
			}
			if m.Nick != "" {
				b.WriteString("\n  • Nickname: " + m.Nick)
			}
		}
		lines = append(lines, b.String())
	}
	return fmt.Sprintf("Retrieved %d interested users for event **%s**:\n%s", len(members), e.Name, strings.Join(lines, "\n")), nil
}

// eventUserID is the user ID of an interested-user entry.
func eventUserID(u *discordgo.GuildScheduledEventUser) string {
	if u.User != nil {
		return u.User.ID
	}
	if u.Member != nil && u.Member.User != nil {
		return u.Member.User.ID
	}
	return ""
}
