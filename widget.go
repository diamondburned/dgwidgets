package dgwidgets

import (
	"context"
	"fmt"
	"time"

	"github.com/diamondburned/arikawa/v2/api"
	"github.com/diamondburned/arikawa/v2/discord"
	"github.com/diamondburned/arikawa/v2/gateway"
	"github.com/diamondburned/arikawa/v2/state"
	"github.com/pkg/errors"
)

// error vars
var (
	ErrAlreadyRunning = errors.New("widget already running")
	ErrNotRunning     = errors.New("not running")
)

// BackgroundTimeout is the timeout for several background jobs, such as clean
// ups. If this variable is 0, then clean ups will all be synchronous.
var BackgroundTimeout = 2 * time.Second

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
	// Only allow listed users to use reactions. A nil slice allows everyone
	// (default).
	UserWhitelist []discord.UserID

	// OnBackgroundError is the callback that is called on trivial errors. It is
	// optional and most of the time should only be used for debugging.
	OnBackgroundError func(error)

	ctx    context.Context
	unbind func()

	running bool
}

// NewWidget returns a new widget.
func NewWidget(state *state.State, channelID discord.ChannelID) *Widget {
	return &Widget{
		State:     state,
		ChannelID: channelID,

		Keys:              []string{},
		Handlers:          map[string]WidgetHandler{},
		DeleteReactions:   true,
		OnBackgroundError: func(error) {},

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

	w.setContext(ctx)
	return nil
}

func (w *Widget) setContext(ctx context.Context) {
	w.ctx = ctx
	w.State = w.State.WithContext(w.ctx)
}

// Done returns the internal context channel.
func (w *Widget) Done() <-chan struct{} {
	return w.ctx.Done()
}

// isUserAllowed returns true if the user is allowed to use this widget.
func (w *Widget) isUserAllowed(userID discord.UserID) bool {
	if w.UserWhitelist == nil {
		return true
	}
	for _, user := range w.UserWhitelist {
		if user == userID {
			return true
		}
	}
	return false
}

// bgClient uses the BackgroundTimeout variable to determine whether or not to
// run trivial functions inside a goroutine.
func (w *Widget) bgClient(fn func(client *api.Client) error) {
	ctx, cancel := context.WithTimeout(context.Background(), BackgroundTimeout)

	call := func() {
		err := fn(w.State.Client.WithContext(ctx))
		cancel()

		if err != nil {
			w.OnBackgroundError(err)
		}
	}

	if BackgroundTimeout == 0 {
		call()
	} else {
		go call()
	}
}

// Spawn spawns the widget in the given channel and blocks until the context has
// expired.
func (w *Widget) Spawn() error {
	if err := w.BindMessage(); err != nil {
		return errors.Wrap(err, "failed to start widget")
	}
	if err := w.SendReactions(); err != nil {
		return errors.Wrap(err, "failed to add reactions")
	}
	w.Wait()
	return nil
}

// BindMessage adds handlers and sends out embeds but does not wait for timeout.
func (w *Widget) BindMessage() error {
	if w.running {
		return ErrAlreadyRunning
	}

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

		if !w.isUserAllowed(r.UserID) {
			return
		}

		fn, ok := w.Handlers[r.Emoji.Name]
		if !ok {
			return
		}

		fn(r)

		if w.DeleteReactions {
			w.bgClient(func(client *api.Client) error {
				return client.DeleteUserReaction(
					r.ChannelID,
					r.MessageID,
					r.UserID,
					r.Emoji.Name,
				)
			})
		}
	})

	// Create initial message.
	msg, err := w.State.SendEmbed(w.ChannelID, w.Embed)
	if err != nil {
		w.unbind() // clean up because fatal
		return err
	}
	w.Message = *msg

	// We're only ever running if everything went right.
	w.running = true

	return nil
}

// AddReactions adds the reaction buttons into the message.
func (w *Widget) SendReactions() error {
	if !w.running {
		return ErrNotRunning
	}

	for _, v := range w.Keys {
		if err := w.State.React(w.Message.ChannelID, w.Message.ID, v); err != nil {
			return errors.Wrap(err, "failed to react to message")
		}
	}

	return nil
}

// Wait waits until the widget has expired and then removes the handlers.
func (w *Widget) Wait() {
	<-w.ctx.Done()
	w.unbind()
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
	// Not running if context is timed out.
	select {
	case <-w.ctx.Done():
		w.running = false
		return false
	default:
		return w.running
	}
}

// UpdateEmbed updates the embed object and edits the original message.
//
//    embed: New embed object to replace the internal embed
func (w *Widget) UpdateEmbed(embed discord.Embed) (*discord.Message, error) {
	w.Embed = embed
	return w.State.EditEmbed(w.ChannelID, w.Message.ID, embed)
}

// UpdateEmbedAsync does what UpdateEmbed does, but it does not block while
// sending the HTTP request. This is mostly made for internal use; one should
// use UpdateEmbed instead.
//
// errCh is optional. The error returned from EditEmbed will simply be ignored
// if errCh is nil. If no errors are returned, then errCh isn't used.
func (w *Widget) UpdateEmbedAsync(embed discord.Embed, errCh chan<- error) {
	w.Embed = embed
	go func() {
		_, err := w.State.EditEmbed(w.ChannelID, w.Message.ID, embed)
		if err != nil && errCh != nil {
			errCh <- err
		}
	}()
}
