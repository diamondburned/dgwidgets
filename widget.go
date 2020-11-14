package dgwidgets

import (
	"context"
	"fmt"

	"github.com/diamondburned/arikawa/v2/discord"
	"github.com/diamondburned/arikawa/v2/gateway"
	"github.com/diamondburned/arikawa/v2/state"
	"github.com/pkg/errors"
)

// error vars
var (
	ErrAlreadyRunning   = errors.New("err: Widget already running")
	ErrIndexOutOfBounds = errors.New("err: Index is out of bounds")
	ErrNotRunning       = errors.New("err: not running")
)

type WidgetHandler = func(*gateway.MessageReactionAddEvent)

// Widget is a message embed with reactions for buttons. It accepts custom
// handlers for reactions. It is not thread-safe.
type Widget struct {
	State     *state.State
	ChannelID discord.ChannelID // const

	Embed   discord.Embed
	Message discord.Message

	// Handlers binds emoji names to functions
	Handlers map[string]WidgetHandler
	// keys stores the handlers keys in the order they were added
	Keys []string

	// Delete reactions after they are added
	DeleteReactions bool
	// Only allow listed users to use reactions.
	UserWhitelist []discord.UserID

	ctx    context.Context
	unbind func()

	running bool
}

// NewWidget returns a new widget.
func NewWidget(state *state.State, channelID discord.ChannelID) *Widget {
	return &Widget{
		State:     state,
		ChannelID: channelID,

		Keys:            []string{},
		Handlers:        map[string]WidgetHandler{},
		DeleteReactions: true,

		ctx: context.Background(),
	}
}

// UseContext sets the internal context for everything. It errors out only if
// the Widget has already been spawned.
func (w *Widget) UseContext(ctx context.Context) error {
	if w.IsRunning() {
		return ErrAlreadyRunning
	}

	// Verify that the context is not expired.
	select {
	case <-w.ctx.Done():
		return w.ctx.Err()
	default:
		break
	}

	w.ctx = ctx
	w.State = w.State.WithContext(w.ctx)

	return nil
}

// Done returns the internal context channel.
func (w *Widget) Done() <-chan struct{} {
	return w.ctx.Done()
}

// isUserAllowed returns true if the user is allowed to use this widget.
func (w *Widget) isUserAllowed(userID discord.UserID) bool {
	for _, user := range w.UserWhitelist {
		if user == userID {
			return true
		}
	}
	return false
}

// Spawn spawns the widget in the given channel and blocks until the context has
// expired.
func (w *Widget) Spawn() error {
	if err := w.Start(); err != nil {
		return err
	}
	w.Wait()
	return nil
}

// Starts sends out embeds and reactions but does not wait for timeout.
func (w *Widget) Start() error {
	if w.running {
		return ErrAlreadyRunning
	}

	w.running = true

	// We need the bot's user ID.
	u, err := w.State.Me()
	if err != nil {
		return err
	}

	w.unbind = w.State.AddHandler(func(r *gateway.MessageReactionAddEvent) {
		// Ignore reactions that don't belong to the message
		if r.MessageID != w.Message.ID {
			return
		}

		// Ignore reactions from the bot, as we'll end up removing our own
		// reactions below.
		if r.UserID == u.ID {
			return
		}

		if v, ok := w.Handlers[r.Emoji.Name]; ok {
			if w.isUserAllowed(r.UserID) {
				v(r)
			}
		}

		if w.DeleteReactions && w.isUserAllowed(r.UserID) {
			w.State.DeleteUserReaction(
				r.ChannelID,
				r.MessageID,
				r.UserID,
				r.Emoji.Name,
			)
		}
	})

	// Create initial message.
	msg, err := w.State.SendEmbed(w.ChannelID, w.Embed)
	if err != nil {
		return err
	}
	w.Message = *msg

	// Add reaction buttons
	for _, v := range w.Keys {
		if err := w.State.React(w.Message.ChannelID, w.Message.ID, v); err != nil {
			return errors.Wrap(err, "failed to react to message")
		}
	}

	return nil
}

// Wait waits until the widget has expired.
func (w *Widget) Wait() {
	<-w.ctx.Done()
}

// Handle adds a handler for the given emoji name.
//
//    emojiName: The unicode value of the emoji
//    handler  : handler function to call when the emoji is clicked
func (w *Widget) Handle(emojiName string, handler WidgetHandler) error {
	if _, ok := w.Handlers[emojiName]; !ok {
		w.Keys = append(w.Keys, emojiName)
		w.Handlers[emojiName] = handler
	}

	// if the widget is running, append the added emoji to the message.
	if w.running {
		return w.State.React(w.Message.ChannelID, w.Message.ID, emojiName)
	}

	return nil
}

// QueryInput querys the user with userID for input. Both the returned message
// along with the prompt will have already been deleted. The given ctx will be
// used for timeout.
func (w *Widget) QueryInput(
	ctx context.Context,
	prompt string,
	userID discord.UserID) (reply *gateway.MessageCreateEvent, err error) {

	state := w.State.WithContext(ctx)

	prompt = fmt.Sprintf("<@%d>, %s", userID, prompt)

	msg, err := state.SendText(w.ChannelID, prompt)
	if err != nil {
		return nil, err
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

	messageIDs := []discord.MessageID{msg.ID, reply.ID}

	if err := state.DeleteMessages(w.ChannelID, messageIDs); err != nil {
		return reply, errors.Wrap(err, "failed to clean up messages")
	}

	return reply, nil
}

// IsRunning returns true if the widget is running.
func (w *Widget) IsRunning() bool {
	return w.running
}

// UpdateEmbed updates the embed object and edits the original message.
//
//    embed: New embed object to replace the internal embed
func (w *Widget) UpdateEmbed(embed discord.Embed) (*discord.Message, error) {
	w.Embed = embed
	return w.State.EditEmbed(w.ChannelID, w.Message.ID, embed)
}
