package dgwidgets

import (
	"github.com/diamondburned/arikawa/v2/discord"
)

// EmbedsFromString splits a string into a slice of MessageEmbeds.
//     txt     : text to split
//     chunklen: How long the text in each embed should be
//               (if set to 0 or less, it defaults to 2048)
func EmbedsFromString(txt string, chunklen int) []discord.Embed {
	if chunklen <= 0 {
		chunklen = 2048
	}

	embeds := []discord.Embed{}
	for i := 0; i < int((float64(len(txt))/float64(chunklen))+0.5); i++ {
		start := i * chunklen
		end := start + chunklen
		if end > len(txt) {
			end = len(txt)
		}
		embeds = append(embeds, discord.Embed{
			Description: txt[start:end],
		})
	}
	return embeds
}
