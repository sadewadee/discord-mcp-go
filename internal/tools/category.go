package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/bwmarrin/discordgo"
)

func (t *Toolset) categoryTools() []Tool {
	return []Tool{
		{
			Name:        "create_category",
			Description: "Create a new category for channels",
			Params:      []Param{opt("guildId", "Discord server ID"), req("name", "Discord category name")},
			Handler:     t.createCategory,
		},
		{
			Name:        "delete_category",
			Description: "Delete a category",
			Params:      []Param{opt("guildId", "Discord server ID"), req("categoryId", "Discord category ID")},
			Handler:     t.deleteCategory,
		},
		{
			Name:        "edit_category",
			Description: "Edit a category (rename or move position)",
			Params: []Param{
				opt("guildId", "Discord server ID"),
				req("categoryId", "Discord category ID"),
				opt("name", "New category name"),
				opt("position", "New position in channel list"),
				opt("reason", "Reason for audit log"),
			},
			Handler: t.editCategory,
		},
		{
			Name:        "find_category",
			Description: "Find a category ID using name and server ID",
			Params:      []Param{opt("guildId", "Discord server ID"), req("categoryName", "Discord category name")},
			Handler:     t.findCategory,
		},
		{
			Name:        "list_channels_in_category",
			Description: "List of channels in a specific category",
			Params:      []Param{opt("guildId", "Discord server ID"), req("categoryId", "Discord category ID")},
			Handler:     t.listChannelsInCategory,
		},
	}
}

// categoryGuild checks that a guild ID is available, then that the named
// argument is set, then looks the guild up. Category tools validate in that
// order.
func (t *Toolset) categoryGuild(a Args, required string) (*discordgo.Guild, error) {
	if t.resolveGuildID(a.Get("guildId")) == "" {
		return nil, errors.New("guildId cannot be null")
	}
	if err := requireArgs(a, required); err != nil {
		return nil, err
	}
	return t.guild(a.Get("guildId"), "guildId cannot be null")
}

// categoryByArg looks up the category named by the categoryId argument.
func (t *Toolset) categoryByArg(a Args) (*discordgo.Guild, *discordgo.Channel, error) {
	g, err := t.categoryGuild(a, "categoryId")
	if err != nil {
		return nil, nil, err
	}
	cat := t.guildCategory(g, a.Get("categoryId"))
	if cat == nil {
		return nil, nil, errCategoryNotFound
	}
	return g, cat, nil
}

func (t *Toolset) createCategory(_ context.Context, a Args) (string, error) {
	g, err := t.categoryGuild(a, "name")
	if err != nil {
		return "", err
	}
	cat, err := t.s.GuildChannelCreate(g.ID, a.Get("name"), discordgo.ChannelTypeGuildCategory)
	if err != nil {
		return "", err
	}
	return "Created new category: " + cat.Name, nil
}

func (t *Toolset) deleteCategory(_ context.Context, a Args) (string, error) {
	_, cat, err := t.categoryByArg(a)
	if err != nil {
		return "", err
	}
	if _, err := t.s.ChannelDelete(cat.ID); err != nil {
		return "", err
	}
	return "Deleted category: " + cat.Name, nil
}

func (t *Toolset) editCategory(_ context.Context, a Args) (string, error) {
	_, cat, err := t.categoryByArg(a)
	if err != nil {
		return "", err
	}
	if a.Get("name") == "" && a.Get("position") == "" {
		return "", errors.New("At least one of 'name' or 'position' must be provided")
	}
	fields := map[string]any{}
	if name := a.Get("name"); name != "" {
		fields["name"] = name
	}
	if err := optionalChannelInt(a, "position", "position", fields); err != nil {
		return "", err
	}
	if err := t.patchChannel(cat.ID, fields, a.Get("reason")); err != nil {
		return "", err
	}
	return "Updated category: " + cat.Name + " (ID: " + cat.ID + ")", nil
}

func (t *Toolset) findCategory(_ context.Context, a Args) (string, error) {
	g, err := t.categoryGuild(a, "categoryName")
	if err != nil {
		return "", err
	}
	name := a.Get("categoryName")
	var found []*discordgo.Channel
	for _, c := range t.guildChannels(g, discordgo.ChannelTypeGuildCategory) {
		if strings.EqualFold(c.Name, name) {
			found = append(found, c)
		}
	}
	if len(found) == 0 {
		return "", errors.New("Category " + name + " not found")
	}
	if len(found) > 1 {
		list := make([]string, len(found))
		for i, c := range found {
			list[i] = "**" + c.Name + "** - `" + c.ID + "`"
		}
		return "", fmt.Errorf("Multiple channels found with name %s.\nList: %s.\nPlease specify the channel ID.", name, strings.Join(list, ", "))
	}
	return "Retrieved category: " + found[0].Name + ", with ID: " + found[0].ID, nil
}

func (t *Toolset) listChannelsInCategory(_ context.Context, a Args) (string, error) {
	g, cat, err := t.categoryByArg(a)
	if err != nil {
		return "", err
	}
	channels := t.categoryChildren(g, cat.ID)
	if len(channels) == 0 {
		return "", errors.New("Category not contains any channels")
	}
	return channelList(channels), nil
}
