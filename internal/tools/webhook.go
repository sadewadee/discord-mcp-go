package tools

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/bwmarrin/discordgo"
)

func (t *Toolset) webhookTools() []Tool {
	return []Tool{
		{
			Name:        "create_webhook",
			Description: "Create a new webhook on a specific channel",
			Params:      []Param{req("channelId", "Discord channel ID"), req("name", "Webhook name")},
			Handler:     t.createWebhook,
		},
		{
			Name:        "delete_webhook",
			Description: "Delete a webhook",
			Params:      []Param{req("webhookId", "Discord webhook ID")},
			Handler:     t.deleteWebhook,
		},
		{
			Name:        "list_webhooks",
			Description: "List of webhooks on a specific channel",
			Params:      []Param{req("channelId", "Discord channel ID")},
			Handler:     t.listWebhooks,
		},
		{
			Name:        "send_webhook_message",
			Description: "Send a message via webhook",
			Params:      []Param{req("webhookUrl", "Discord webhook link"), req("message", "Message content")},
			Handler:     t.sendWebhookMessage,
		},
	}
}

// webhookAPIBase is the prefix of webhook URLs.
const webhookAPIBase = "https://discord.com/api/v10/webhooks/"

// webhookURL is the execute URL of a webhook; webhooks without a token (for
// example channel-follower webhooks) have no token segment.
func webhookURL(w *discordgo.Webhook) string {
	if w.Token == "" {
		return webhookAPIBase + w.ID
	}
	return webhookAPIBase + w.ID + "/" + w.Token
}

// webhookFormat renders one webhook on a line.
func webhookFormat(w *discordgo.Webhook) string {
	return fmt.Sprintf("- (ID: %s) **[%s]** ```%s```", w.ID, w.Name, webhookURL(w))
}

// webhookURLRe matches a webhook execute URL and captures its ID and token.
var webhookURLRe = regexp.MustCompile(`^https?://(?:[^\s.]+\.)?discord(?:app)?\.com/api(?:/v\d+)?/webhooks/(\d+)/([\w-]+)(?:/.*)?$`)

// webhookParseURL extracts the ID and token of a webhook URL.
func webhookParseURL(u string) (id, token string, err error) {
	m := webhookURLRe.FindStringSubmatch(u)
	if m == nil {
		return "", "", errors.New("Provided invalid webhook URL")
	}
	return m[1], m[2], nil
}

func (t *Toolset) createWebhook(_ context.Context, a Args) (string, error) {
	channelID, name := a.Get("channelId"), a.Get("name")
	if channelID == "" {
		return "", errors.New("channelId cannot be null")
	}
	if name == "" {
		return "", errors.New("webhook name cannot be null")
	}
	ch := t.channelOfType(channelID, discordgo.ChannelTypeGuildText)
	if ch == nil {
		return "", errChannelNotFound
	}
	w, err := t.s.WebhookCreate(ch.ID, name, "")
	if err != nil {
		return "", err
	}
	return "Created " + name + " webhook: " + webhookURL(w), nil
}

func (t *Toolset) deleteWebhook(_ context.Context, a Args) (string, error) {
	webhookID := a.Get("webhookId")
	if webhookID == "" {
		return "", errors.New("webhookId cannot be null")
	}
	w, err := t.s.Webhook(webhookID)
	if err != nil {
		return "", err
	}
	if w == nil {
		return "", errors.New("Webhook not found by webhookId")
	}
	if err := t.s.WebhookDelete(w.ID); err != nil {
		return "", err
	}
	return "Deleted " + w.Name + " webhook", nil
}

func (t *Toolset) listWebhooks(_ context.Context, a Args) (string, error) {
	channelID := a.Get("channelId")
	if channelID == "" {
		return "", errors.New("channelId cannot be null")
	}
	ch := t.channelOfType(channelID, discordgo.ChannelTypeGuildText)
	if ch == nil {
		return "", errChannelNotFound
	}
	webhooks, err := t.s.ChannelWebhooks(ch.ID)
	if err != nil {
		return "", err
	}
	if len(webhooks) == 0 {
		return "", errors.New("No webhooks found")
	}
	lines := make([]string, len(webhooks))
	for i, w := range webhooks {
		lines[i] = webhookFormat(w)
	}
	return "**Retrieved " + strconv.Itoa(len(lines)) + " messages:** \n" + strings.Join(lines, "\n"), nil
}

func (t *Toolset) sendWebhookMessage(_ context.Context, a Args) (string, error) {
	webhookURLArg, message := a.Get("webhookUrl"), a.Get("message")
	if webhookURLArg == "" {
		return "", errors.New("webhookUrl cannot be null")
	}
	if message == "" {
		return "", errors.New("message cannot be null")
	}
	id, token, err := webhookParseURL(webhookURLArg)
	if err != nil {
		return "", err
	}
	sent, err := t.s.WebhookExecute(id, token, true, &discordgo.WebhookParams{Content: message})
	if err != nil {
		return "", err
	}
	guildID := sent.GuildID
	if guildID == "" {
		if ch := t.cachedChannel(sent.ChannelID); ch != nil {
			guildID = ch.GuildID
		}
	}
	return "Message sent successfully. Message link: " + jumpURL(guildID, sent.ChannelID, sent.ID), nil
}
