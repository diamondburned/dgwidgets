package dgwidgets

import (
	"context"
	"sync"
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

// BackgroundTimeout is the default timeout for several background jobs, such as
// clean ups. If this variable is 0, then clean ups will all be synchronous.
var BackgroundTimeout = 2 * time.Second

// WidgetHandler -- Self explanatory
type WidgetHandler = func(*gateway.MessageReactionAddEvent)

// Widget is a message embed with reactions for buttons. It accepts custom
// handlers for reactions. It is not thread-safe.
type Widget struct {
	State     *state.State
	ChannelID discord.ChannelID // const

	// SendData is the message data to be sent and set into Message.
	SendData api.SendMessageData

	// SentMessage is the message received after sending using BindMessage. The
	// caller should not set this message. It is only valid after sending.
	SentMessage discord.Message

	// Handlers binds emoji names to functions
	Handlers []EmojiHandler

	// Only allow listed users to use reactions. A nil slice allows everyone
	// (default).
	UserWhitelist []discord.UserID

	// OnBackgroundError is the callback that is called on trivial errors. It is
	// optional and most of the time should only be used for debugging.
	OnBackgroundError func(error)
	// BackgroundTimeout is the timeout for several background jobs, such as
	// clean ups. If this variable is 0, then clean ups will all be synchronous.
	BackgroundTimeout time.Duration

	ctx context.Context

	// states
	meID     discord.UserID
	reactCh  chan *gateway.MessageReactionAddEvent
	unbindCh func()
}

// EmojiHandler is an emoji handler.
type EmojiHandler struct {
	Emoji   discord.APIEmoji
	Handler WidgetHandler
}

// NewWidget returns a new widget that listens forever.
func NewWidget(state *state.State, channelID discord.ChannelID) *Widget {
	return &Widget{
		State:     state,
		ChannelID: channelID,

		OnBackgroundError: func(error) {},
		BackgroundTimeout: BackgroundTimeout,

		ctx:     context.Background(),
		reactCh: make(chan *gateway.MessageReactionAddEvent),
	}
}

// UseContext sets the internal context for everything. It only errors out only
// if the Widget has already been spawned.
func (w *Widget) UseContext(ctx context.Context) error {
	if w.IsRunning() {
		return ErrAlreadyRunning
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
	if w.meID == userID {
		return false
	}
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
func (w *Widget) bgClient(wg *sync.WaitGroup, fn func(client *api.Client) error) {
	if w.BackgroundTimeout == 0 {
		if err := fn(w.State.Client); err != nil {
			w.OnBackgroundError(err)
		}

		return
	}

	if wg != nil {
		wg.Add(1)
	}

	go func() {
		ctx, cancel := context.WithTimeout(w.State.Client.Context(), w.BackgroundTimeout)
		defer cancel()

		if err := fn(w.State.Client.WithContext(ctx)); err != nil {
			w.OnBackgroundError(err)
		}

		if wg != nil {
			wg.Done()
		}
	}()
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

// BindMessage sends out a message. It updates the Message inside Widget. If
// BindMessage is called multiple times (in the same goroutine), the latest
// message will be used.
func (w *Widget) BindMessage() error {
	u, err := w.State.Me()
	if err != nil {
		return errors.Wrap(err, "failed to get current user")
	}

	// We need the bot's user ID.
	w.meID = u.ID

	// Create initial message.
	msg, err := w.State.SendMessageComplex(w.ChannelID, w.SendData)
	if err != nil {
		return errors.Wrap(err, "failed to send message")
	}
	w.SentMessage = *msg

	return nil
}

// SendReactions adds handler and the reaction buttons into the message. It
// errors out if BindMessage hasn't been called yet.
func (w *Widget) SendReactions() error {
	if !w.SentMessage.ID.IsValid() {
		return ErrNotRunning
	}

	w.unbindCh = w.State.AddHandler(w.reactCh)

	for _, h := range w.Handlers {
		if err := w.State.React(w.SentMessage.ChannelID, w.SentMessage.ID, h.Emoji); err != nil {
			return errors.Wrap(err, "failed to react to message")
		}
	}

	return nil
}

// Wait spins the widget loop and waits until the widget has expired and then
// removes the handlers. If no contexts are used, then Wait will block forever.
func (w *Widget) Wait() {
	defer w.unbindCh()

	var r *gateway.MessageReactionAddEvent
	for {
		select {
		case <-w.ctx.Done():
			return
		case r = <-w.reactCh:

		}

		// Ignore reactions that don't belong to the message or from the bot or
		// from users that we don't know.
		if r.ChannelID != w.ChannelID || r.MessageID != w.SentMessage.ID ||
			!w.isUserAllowed(r.UserID) {

			continue
		}

		ix := w.handler(r.Emoji.APIString())
		if ix == -1 {
			continue
		}

		w.Handlers[ix].Handler(r)

		// Force check that the context has not been expired after running our
		// callback.
		select {
		case <-w.ctx.Done():
			return
		default:
			continue
		}
	}
}

// Handle adds a handler for the given emoji name. If a handler with the given
// name is already added, then this one replaces it. The handlers are called
// sequentially once Wait is running; cancelling a context in the handler
// guarantees that no future handlers will be called.
func (w *Widget) Handle(emojiName discord.APIEmoji, handler WidgetHandler) {
	h := EmojiHandler{
		Emoji:   emojiName,
		Handler: handler,
	}

	if i := w.handler(emojiName); i != -1 {
		w.Handlers[i] = h
	} else {
		w.Handlers = append(w.Handlers, h)
	}
}

// handler finds an emoji handler from the given emoji name and returns an
// index, or -1 if none is found.
func (w *Widget) handler(emojiName discord.APIEmoji) int {
	for i, handler := range w.Handlers {
		if handler.Emoji == emojiName {
			return i
		}
	}
	return -1
}

// IsRunning returns true if the widget is running. IsRunning is safe to use
// concurrently once Wait is running.
func (w *Widget) IsRunning() bool {
	// Not running if context is timed out.
	select {
	case <-w.ctx.Done():
		return false
	default:
		return w.unbindCh != nil
	}
}

// UpdateMessage updates and edits the sent message. It errors out if the
// message is not sent; change .SendData directly if that's the case.
func (w *Widget) UpdateMessage(data api.EditMessageData) (*discord.Message, error) {
	w.updateMessage(&data)
	return w.State.EditMessageComplex(w.ChannelID, w.SentMessage.ID, data)
}

// UpdateMessageAsync does what UpdateMessage does, but it does not block while
// sending the HTTP request. If BackgroundTimeout is non-zero, then its timeout
// will be used, otherwise no timeout but the current context will be.
//
// errCh is optional. The error returned from EditEmbed will simply be ignored
// if errCh is nil. If no errors are returned, then errCh isn't used.
func (w *Widget) UpdateMessageAsync(errCh chan<- error, data api.EditMessageData) {
	w.updateMessage(&data)

	var ctx context.Context
	var cancel context.CancelFunc

	if w.BackgroundTimeout > 0 {
		ctx, cancel = context.WithTimeout(w.ctx, w.BackgroundTimeout)
	} else {
		ctx = w.ctx
	}

	client := w.State.Client.WithContext(ctx)

	go func() {
		_, err := client.EditMessageComplex(w.ChannelID, w.SentMessage.ID, data)
		if err != nil && errCh != nil {
			select {
			case <-w.ctx.Done():
				// Stop sending an error if the parent context is dead.
			case errCh <- err:
			}
		}

		if cancel != nil {
			cancel()
		}
	}()
}

func (w *Widget) updateMessage(data *api.EditMessageData) {
	if data.Content != nil {
		w.SendData.Content = data.Content.Val
	}
	if data.Embed != nil {
		w.SendData.Embed = data.Embed
	}
	if data.AllowedMentions != nil {
		w.SendData.AllowedMentions = data.AllowedMentions
	}
}
