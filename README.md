# dg-widgets
Make widgets with embeds and reactions. Forked to be compatible with [Arikawa](https://github.com/diamondburned/arikawa).

![img](https://i.imgur.com/viJc9Cm.gif)

# Example usage
```go
// PaginateDemo is a demonstration of the (forked) pagination library.
func (bot *Bot) PaginateDemo(m *gateway.MessageCreateEvent) error {
	p := dgwidgets.NewPaginator(bot.Ctx.Session, m.ChannelID)

	// Add embed pages to paginator
	p.Add(
		discord.Embed{Description: "Page one"},
		discord.Embed{Description: "Page two"},
		discord.Embed{Description: "Page three"},
	)

	// Sets the footers of all added pages to their page numbers.
	p.SetPageFooters()

	// When the paginator is done listening set the colour to yellow.
	p.ColourWhenDone = 0xFFFF00

	// Stop listening for reaction events after 5 minutes.
	p.Widget.Timeout = 5 * time.Minute

	// Add a custom handler for the gun reaction.
	p.Widget.Handle("🔫", func(r *gateway.MessageReactionAddEvent) {
		bot.Ctx.SendMessage(r.ChannelID, "Bang!", nil)
	})

	return p.Spawn()
}
```
