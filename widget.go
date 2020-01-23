package dgwidgets

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/diamondburned/arikawa/discord"
	"github.com/diamondburned/arikawa/gateway"
	"github.com/diamondburned/arikawa/session"
)

// error vars
var (
	ErrAlreadyRunning   = errors.New("err: Widget already running")
	ErrIndexOutOfBounds = errors.New("err: Index is out of bounds")
	ErrNilMessage       = errors.New("err: Message is nil")
	ErrNilEmbed         = errors.New("err: embed is nil")
	ErrNotRunning       = errors.New("err: not running")
)

// WidgetHandler ...
type WidgetHandler func(*gateway.MessageReactionAddEvent)

// Widget is a message embed with reactions for buttons.
// Accepts custom handlers for reactions.
type Widget struct {
	sync.Mutex
	Embed     *discord.Embed
	Message   *discord.Message
	Session   *session.Session
	ChannelID discord.Snowflake
	Timeout   time.Duration
	Close     chan struct{}

	// Handlers binds emoji names to functions
	Handlers map[string]WidgetHandler
	// keys stores the handlers keys in the order they were added
	Keys []string

	// Delete reactions after they are added
	DeleteReactions bool
	// Only allow listed users to use reactions.
	UserWhitelist []discord.Snowflake

	running bool
}

// NewWidget returns a pointer to a Widget object
//    ses      : discordgo session
//    channelID: channelID to spawn the widget on
func NewWidget(ses *session.Session,
	channelID discord.Snowflake, embed *discord.Embed) *Widget {

	return &Widget{
		ChannelID:       channelID,
		Session:         ses,
		Keys:            []string{},
		Handlers:        map[string]WidgetHandler{},
		Close:           make(chan struct{}),
		DeleteReactions: true,
		Embed:           embed,
	}
}

// isUserAllowed returns true if the user is allowed
// to use this widget.
func (w *Widget) isUserAllowed(userID discord.Snowflake) bool {
	if w.UserWhitelist == nil || len(w.UserWhitelist) == 0 {
		return true
	}
	for _, user := range w.UserWhitelist {
		if user == userID {
			return true
		}
	}
	return false
}

// Spawn spawns the widget in channel w.ChannelID
func (w *Widget) Spawn() error {
	if w.Running() {
		return ErrAlreadyRunning
	}

	w.running = true
	defer func() {
		w.running = false
	}()

	if w.Embed == nil {
		return ErrNilEmbed
	}

	// Create a context that can be timed out.
	var ctx = context.Background()
	if w.Timeout > 0 {
		tCtx, cancel := context.WithTimeout(ctx, w.Timeout)
		defer cancel()

		ctx = tCtx
	}

	// Create initial message.
	msg, err := w.Session.SendMessage(w.ChannelID, "", w.Embed)
	if err != nil {
		return err
	}
	w.Message = msg

	// Add reaction buttons
	for _, v := range w.Keys {
		w.Session.React(w.Message.ChannelID, w.Message.ID, v)
	}

	remove := w.Session.AddHandler(func(r *gateway.MessageReactionAddEvent) {
		// Ignore reactions sent by bot
		if r.MessageID != w.Message.ID {
			return
		}

		if v, ok := w.Handlers[r.Emoji.Name]; ok {
			if w.isUserAllowed(r.UserID) {
				go v(r)
			}
		}

		if w.DeleteReactions && w.isUserAllowed(r.UserID) {
			go func() {
				time.Sleep(time.Millisecond * 250)
				w.Session.DeleteReaction(
					r.ChannelID,
					r.MessageID,
					r.UserID,
					r.Emoji.Name,
				)
			}()
		}
	})

	defer remove()

	select {
	case <-w.Close:
		return nil
	case <-ctx.Done():
		return nil
	}
}

// Handle adds a handler for the given emoji name
//    emojiName: The unicode value of the emoji
//    handler  : handler function to call when the emoji is clicked
//               func(*Widget, *discordgo.MessageReaction)
func (w *Widget) Handle(emojiName string, handler WidgetHandler) error {
	if _, ok := w.Handlers[emojiName]; !ok {
		w.Keys = append(w.Keys, emojiName)
		w.Handlers[emojiName] = handler
	}

	// if the widget is running, append the added emoji to the message.
	if w.Running() && w.Message != nil {
		return w.Session.React(w.Message.ChannelID, w.Message.ID, emojiName)
	}

	return nil
}

// QueryInput querys the user with ID `id` for input
//    prompt : Question prompt
//    userID : UserID to get message from
//    timeout: How long to wait for the user's response
func (w *Widget) QueryInput(
	prompt string, userID discord.Snowflake,
	timeout time.Duration) (*gateway.MessageCreateEvent, error) {

	msg, err := w.Session.SendMessage(
		w.ChannelID, "<@"+userID.String()+">,  "+prompt, nil)
	if err != nil {
		return nil, err
	}

	defer w.Session.DeleteMessage(msg.ChannelID, msg.ID)

	var recv = make(chan *gateway.MessageCreateEvent)

	remove := w.Session.AddHandler(func(m *gateway.MessageCreateEvent) {
		if m.Author.ID != userID {
			return
		}

		w.Session.DeleteMessage(m.ChannelID, m.ID)
		recv <- m
	})
	defer remove()

	after := time.After(timeout)

	select {
	case m := <-recv:
		return m, nil
	case <-after:
		return nil, errors.New("Timed out")
	}
}

// Running returns w.running
func (w *Widget) Running() bool {
	w.Lock()
	running := w.running
	w.Unlock()
	return running
}

// UpdateEmbed updates the embed object and edits the original message
//    embed: New embed object to replace w.Embed
func (w *Widget) UpdateEmbed(embed *discord.Embed) (*discord.Message, error) {
	if w.Message == nil {
		return nil, ErrNilMessage
	}
	return w.Session.EditMessage(w.ChannelID, w.Message.ID, "", embed, false)
}
