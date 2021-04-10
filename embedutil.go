package dgwidgets

import (
	"math"

	"github.com/diamondburned/arikawa/v2/discord"
	"github.com/diamondburned/arikawa/v2/gateway"
	"github.com/diamondburned/arikawa/v2/state"
	"github.com/pkg/errors"
)

// EmbedsFromString splits a string into a slice of Embeds.
//
//     txt     : text to split
//     chunklen: How long the text in each embed should be
//               (if set to 0 or less, it defaults to 2000).
func EmbedsFromString(txt string, chunkLen int) []discord.Embed {
	if chunkLen <= 0 {
		chunkLen = 2000
	}

	runes := []rune(txt)

	embeds := []discord.Embed{}
	chunks := int(math.Ceil(float64(len(runes)) / float64(chunkLen)))

	for i := 0; i < chunks; i++ {
		start := i * chunkLen
		end := start + chunkLen
		if end > len(txt) {
			end = len(txt)
		}

		embeds = append(embeds, discord.Embed{
			Description: string(runes[start:end]),
		})
	}
	return embeds
}

// QueryInput queries the user with userID for input. Both the returned message
// along with the prompt will have already been deleted. The context inside
// state will be used for timeout.
func QueryInput(
	state *state.State,
	chID discord.ChannelID,
	userID discord.UserID,
	prompt string, mention bool) (reply *gateway.MessageCreateEvent, err error) {

	ctx := state.Context()

	if mention {
		prompt = userID.Mention() + ", " + prompt
	}

	msg, err := state.SendText(chID, prompt)
	if err != nil {
		return nil, errors.Wrap(err, "failed to send message")
	}

	recv := make(chan *gateway.MessageCreateEvent)

	cancel := state.AddHandler(recv)
	defer cancel()

WaitLoop:
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()

		case m := <-recv:
			if m.Author.ID != userID {
				continue
			}

			cancel()
			reply = m
			break WaitLoop
		}
	}

	messageIDs := [2]discord.MessageID{msg.ID, reply.ID}

	if err := state.DeleteMessages(chID, messageIDs[:]); err != nil {
		// We couldn't invoke this somehow. Try to delete each ID individually.
		for _, id := range messageIDs {
			if err = state.DeleteMessage(chID, id); err != nil {
				break
			}
		}

		if err != nil {
			return reply, errors.Wrap(err, "failed to clean up messages")
		}
	}

	return reply, nil
}
