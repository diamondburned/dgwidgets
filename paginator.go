package dgwidgets

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/diamondburned/arikawa/v2/discord"
	"github.com/diamondburned/arikawa/v2/gateway"
	"github.com/diamondburned/arikawa/v2/state"
	"github.com/pkg/errors"
	"golang.org/x/sync/errgroup"
)

// emoji constants
var (
	NavPlus        = "➕"
	NavPlay        = "▶"
	NavPause       = "⏸"
	NavStop        = "⏹"
	NavRight       = "➡"
	NavLeft        = "⬅"
	NavUp          = "⬆"
	NavDown        = "⬇"
	NavEnd         = "⏩"
	NavBeginning   = "⏪"
	NavNumbers     = "🔢"
	NavInformation = "ℹ"
	NavSave        = "💾"
)

// PageChange is the overloaded enum type to both contain page events and the
// actual page number.
type PageChange int

const (
	NextPage PageChange = -iota - 1 // backwards iota
	PrevPage
	FirstPage
	LastPage
)

// Paginator provides a method for creating a navigatable embed. It is not
// thread-safe, unless explicitly said otherwise.
type Paginator struct {
	Widget *Widget

	Pages []discord.Embed

	// OnPageChange is called every page change in the main event loop. Callers
	// could use this callback to update the constant variables. The callback
	// will be called after the page index has been updated.
	OnPageChange func(PageChange, error) // optional

	// Loop back to the beginning or end when on the first or last page.
	Loop bool

	DeleteMessageWhenDone   bool
	DeleteReactionsWhenDone bool          // default true
	ColourWhenDone          discord.Color // default red

	// States

	index      int
	cancel     context.CancelFunc
	pageChange chan PageChange
}

// NewPaginator returns a new Paginator.
//
//    ses      : discordgo session
//    channelID: channelID to spawn the paginator on
func NewPaginator(state *state.State, channelID discord.ChannelID) *Paginator {
	ctx, cancel := context.WithCancel(context.Background())

	p := &Paginator{
		Widget: NewWidget(state, channelID),

		DeleteReactionsWhenDone: true,
		ColourWhenDone:          0xFF0000,

		cancel:     cancel,
		pageChange: make(chan PageChange),
	}

	p.Widget.UseContext(ctx)

	p.Widget.Handle(NavBeginning, func(r *gateway.MessageReactionAddEvent) {
		p.pageChange <- FirstPage
	})
	p.Widget.Handle(NavLeft, func(r *gateway.MessageReactionAddEvent) {
		p.pageChange <- PrevPage
	})
	p.Widget.Handle(NavRight, func(r *gateway.MessageReactionAddEvent) {
		p.pageChange <- NextPage
	})
	p.Widget.Handle(NavEnd, func(r *gateway.MessageReactionAddEvent) {
		p.pageChange <- LastPage
	})
	p.Widget.Handle(NavNumbers, func(r *gateway.MessageReactionAddEvent) {
		ctx, cancel := context.WithTimeout(context.TODO(), 10*time.Second)
		defer cancel()

		const prompt = "enter the page number you would like to open."

		m, err := p.Widget.QueryInput(ctx, prompt, r.UserID)
		if err != nil {
			return
		}

		n, err := strconv.Atoi(m.Content)
		if err != nil || n < 0 {
			return
		}

		p.pageChange <- PageChange(n)
	})

	return p
}

// UseContext sets the Paginator and Widget's contexts. It assumes that only one
// thread will use Paginator, and Widget will only error out when Spawn() is
// running, thus it assumes that no errors will be returned.
func (p *Paginator) UseContext(ctx context.Context) {
	ctx, p.cancel = context.WithCancel(ctx)
	p.Widget.setContext(ctx)
}

// SetTimeout sets the timout of the paginator. For more advanced cancellation
// and timeout control, UseContext should be used instead.
func (p *Paginator) SetTimeout(timeout time.Duration) {
	ctx, cancel := context.WithTimeout(p.Widget.ctx, timeout)
	p.cancel = cancel
	p.Widget.setContext(ctx)
}

// Close stops the Paginator. It is only thread-safe if no other threads will
// call UseContext.
func (p *Paginator) Close() {
	p.cancel()
}

// Index returns the current page number.
func (p *Paginator) Index() int {
	return p.index
}

// Spawn spawns the paginator in the givne channel. It blocks until close is
// called or the given context has timed out.
func (p *Paginator) Spawn() error {
	p.Widget.Embed = *p.Page()

	if err := p.Widget.Start(); err != nil {
		return err
	}

	// Always stop the context at the end.
	defer p.cancel()

	var change PageChange

EventLoop:
	for {
		select {
		case <-p.Widget.Done():
			break EventLoop
		case change = <-p.pageChange:
		}

		var err error

		switch change {
		case NextPage:
			err = p.NextPage()
		case PrevPage:
			err = p.PreviousPage()
		case FirstPage:
			err = p.Goto(0)
		case LastPage:
			err = p.Goto(len(p.Pages) - 1)
		default:
			err = p.Goto(int(change))
		}

		if p.OnPageChange != nil {
			p.OnPageChange(change, err)
		}

		if err != nil {
			p.Update()
		}
	}

	var wg errgroup.Group

	// Delete Message when done
	if p.DeleteMessageWhenDone && p.Widget.IsRunning() {
		wg.Go(func() error {
			err := p.Widget.State.DeleteMessage(
				p.Widget.Message.ChannelID,
				p.Widget.Message.ID,
			)
			return errors.Wrap(err, "failed to delete message")
		})

	} else if p.ColourWhenDone > 0 {
		page := *p.Page() // copy for thread-safety
		page.Color = p.ColourWhenDone

		wg.Go(func() error {
			_, err := p.Widget.UpdateEmbed(page)
			return errors.Wrap(err, "failed to update embed")
		})
	}

	// Delete reactions when done
	if p.DeleteReactionsWhenDone && p.Widget.IsRunning() {
		wg.Go(func() error {
			err := p.Widget.State.DeleteAllReactions(
				p.Widget.ChannelID,
				p.Widget.Message.ID,
			)
			return errors.Wrap(err, "failed to delete all reactions")
		})
	}

	if err := wg.Wait(); err != nil {
		return errors.Wrap(err, "failed to clean up")
	}

	p.Widget.Wait()
	return nil
}

// Add a page to the paginator. It also automatically updates embed footers.
//
//    embeds: embed pages to add.
func (p *Paginator) Add(embeds ...discord.Embed) {
	p.Pages = append(p.Pages, embeds...)
	p.SetPageFooters()
}

// Page returns the page of the current index.
func (p *Paginator) Page() *discord.Embed {
	return &p.Pages[p.index]
}

// NextPage sets the page index to the next page.
func (p *Paginator) NextPage() error {
	if next := p.index + 1; next >= 0 && next < len(p.Pages) {
		p.index = next
		return nil
	}

	// Set the queue back to the beginning if Loop is enabled.
	if p.Loop {
		p.index = 0
		return nil
	}

	return ErrIndexOutOfBounds
}

// PreviousPage sets the current page index to the previous page.
func (p *Paginator) PreviousPage() error {
	if prev := p.index - 1; prev >= 0 && prev < len(p.Pages) {
		p.index = prev
		return nil
	}

	// Set the queue back to the beginning if Loop is enabled.
	if p.Loop {
		p.index = len(p.Pages) - 1
		return nil
	}

	return ErrIndexOutOfBounds
}

// Goto jumps to the requested page index. It does not update the actual embed.
//
//    index: The index of the page to go to
func (p *Paginator) Goto(index int) error {
	if index < 0 || index >= len(p.Pages) {
		return ErrIndexOutOfBounds
	}

	p.index = index
	return nil
}

// Update updates the message with the current state of the paginator. Although
// it is thread-safe, it does not block until the API request is sent through.
// As such, it will not return an error if the update fails.
func (p *Paginator) Update() {
	page := *p.Page() // copy the page embed
	go p.Widget.UpdateEmbed(page)
}

// SetPageFooters sets the footer of each embed to the page number out of the
// total length of the embeds.
func (p *Paginator) SetPageFooters() {
	for i, embed := range p.Pages {
		embed.Footer = &discord.EmbedFooter{
			Text: fmt.Sprintf("#[%d / %d]", i+1, len(p.Pages)),
		}
	}
}
