package autoreply

import (
	"github.com/bwmarrin/discordgo"
)

// Discord is what the listener needs from Discord.
type Discord interface {
	// History returns up to limit messages posted before beforeID in the
	// channel, in any order.
	History(channelID, beforeID string, limit int) ([]*discordgo.Message, error)
	Send(channelID string, msg *discordgo.MessageSend) error
	Typing(channelID string) error
	React(channelID, messageID, emoji string) error
	// Parent returns the parent channel ID of a thread or forum post, and ""
	// for any other channel.
	Parent(channelID string) (string, error)
}

// SessionDiscord implements Discord on a discordgo session.
type SessionDiscord struct{ S *discordgo.Session }

func (d SessionDiscord) History(channelID, beforeID string, limit int) ([]*discordgo.Message, error) {
	if limit <= 0 {
		return nil, nil
	}
	return d.S.ChannelMessages(channelID, limit, beforeID, "", "")
}

func (d SessionDiscord) Send(channelID string, msg *discordgo.MessageSend) error {
	_, err := d.S.ChannelMessageSendComplex(channelID, msg)
	return err
}

func (d SessionDiscord) Typing(channelID string) error { return d.S.ChannelTyping(channelID) }

func (d SessionDiscord) React(channelID, messageID, emoji string) error {
	return d.S.MessageReactionAdd(channelID, messageID, emoji)
}

func (d SessionDiscord) Parent(channelID string) (string, error) {
	c, err := d.S.State.Channel(channelID)
	if err != nil || c == nil {
		// Archived threads are not in the cache.
		if c, err = d.S.Channel(channelID); err != nil {
			return "", err
		}
	}
	if c.IsThread() {
		return c.ParentID, nil
	}
	return "", nil
}
