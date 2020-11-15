package dgwidgets

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/diamondburned/arikawa/v2/api"
	"github.com/diamondburned/arikawa/v2/discord"
	"github.com/diamondburned/arikawa/v2/gateway"
	"github.com/diamondburned/arikawa/v2/state"
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

// PromptTimeout is the variable for the duration to wait before the page number
// prompt times out.
var PromptTimeout = 10 * time.Second

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
	// will be called after the page index has been updated. If the returned
	// boolean is true, then the error will be ignored.
	OnPageChange func(PageChange, error) (ignoreErr bool) // optional

	// Loop back to the beginning or end when on the first or last page.
	Loop bool

	DeleteMessageWhenDone   bool
	DeleteReactionsWhenDone bool          // default true
	ColourWhenDone          discord.Color // default red

	// States

	index      int
	cancel     context.CancelFunc
	errorCh    chan error
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
		errorCh:    make(chan error, 1),
		pageChange: make(chan PageChange, 1),
	}

	p.Widget.UseContext(ctx)

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
// call UseContext. Spawn will automatically call Close, so most of the time,
// the main caller does not need to call this.
func (p *Paginator) Close() {
	p.cancel()
}

// Index returns the current zero-indexed page number.
func (p *Paginator) Index() int {
	return p.index
}

func (p *Paginator) sendChange(ch PageChange) {
	select {
	case p.pageChange <- ch:
	case <-p.Widget.Done():
	}
}

func (p *Paginator) sendErr(err error) {
	if err != nil {
		p.errorCh <- err
	}
}

// Spawn spawns the paginator in the givne channel. It blocks until close is
// called or the given context has timed out.
func (p *Paginator) Spawn() (err error) {
	p.Widget.Handle(NavBeginning, func(r *gateway.MessageReactionAddEvent) {
		p.sendChange(FirstPage)
	})
	p.Widget.Handle(NavLeft, func(r *gateway.MessageReactionAddEvent) {
		p.sendChange(PrevPage)
	})
	p.Widget.Handle(NavRight, func(r *gateway.MessageReactionAddEvent) {
		p.sendChange(NextPage)
	})
	p.Widget.Handle(NavEnd, func(r *gateway.MessageReactionAddEvent) {
		p.sendChange(LastPage)
	})
	p.Widget.Handle(NavNumbers, func(r *gateway.MessageReactionAddEvent) {
		ctx, cancel := context.WithTimeout(context.TODO(), PromptTimeout)
		defer cancel()

		const prompt = "enter the page number you would like to open."

		m, err := p.Widget.QueryInput(ctx, prompt, r.UserID)
		if err != nil {
			p.sendErr(err)
			return
		}

		n, err := strconv.Atoi(m.Content)
		if err != nil {
			p.sendErr(err)
			return
		}

		if n < 1 {
			p.sendErr(fmt.Errorf("Input %d is zero or negative.", n))
			return
		}

		p.sendChange(PageChange(n - 1)) // account for zero-indexed
	})

	p.Widget.Embed = *p.Page()

	if err := p.Widget.BindMessage(); err != nil {
		return err
	}

	// Send the reactions asynchronously so we could listen to events right
	// away. The method only reads, so we should be fine.
	go func() { p.sendErr(p.Widget.SendReactions()) }()

	// Always stop the context at the end.
	defer p.Widget.Wait()
	defer p.Close()

	var change PageChange

EventLoop:
	for {
		select {
		case <-p.Widget.Done():
			break EventLoop
		case err = <-p.errorCh:
			break EventLoop

		case change = <-p.pageChange:
		}

		switch change {
		case NextPage:
			p.NextPage()
		case PrevPage:
			p.PreviousPage()
		case FirstPage:
			p.Goto(0)
		case LastPage:
			p.Goto(len(p.Pages) - 1)
		default:
			if !p.Goto(int(change)) {
				err = fmt.Errorf("Page %d is out of bounds.", change)
			}
		}

		if p.OnPageChange != nil && p.OnPageChange(change, err) {
			err = nil
		}

		if err != nil {
			break EventLoop
		}

		p.Update()
	}

	// Copy the needed IDs so we could background jobs properly.
	messageID := p.Widget.Message.ID
	channelID := p.Widget.Message.ChannelID

	// Delete Message when done
	if p.DeleteMessageWhenDone {
		p.Widget.bgClient(func(client *api.Client) error {
			return p.Widget.State.DeleteMessage(channelID, messageID)
		})

		// Don't bother doing the rest of the tasks because the message is gone.
		return
	}

	if p.ColourWhenDone > 0 {
		page := *p.Page() // copy for thread-safety
		page.Color = p.ColourWhenDone

		p.Widget.bgClient(func(client *api.Client) error {
			_, err := client.EditEmbed(channelID, messageID, page)
			return err
		})
	}

	// Delete reactions when done
	if p.DeleteReactionsWhenDone {
		p.Widget.bgClient(func(client *api.Client) error {
			return client.DeleteAllReactions(channelID, messageID)
		})
	}

	return
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
func (p *Paginator) NextPage() {
	if next := p.index + 1; next >= 0 && next < len(p.Pages) {
		p.index = next
		return
	}

	// Set the queue back to the beginning if Loop is enabled.
	if p.Loop {
		p.index = 0
		return
	}

	return
}

// PreviousPage sets the current page index to the previous page.
func (p *Paginator) PreviousPage() {
	if prev := p.index - 1; prev >= 0 && prev < len(p.Pages) {
		p.index = prev
		return
	}

	// Set the queue back to the beginning if Loop is enabled.
	if p.Loop {
		p.index = len(p.Pages) - 1
		return
	}
}

// Goto jumps to the requested page index. It does not update the actual embed.
//
//    index: The index of the page to go to
func (p *Paginator) Goto(index int) bool {
	if index < 0 || index >= len(p.Pages) {
		return false
	}

	p.index = index
	return true
}

// Update updates the message with the current state of the paginator. Although
// it is thread-safe, it does not block until the API request is sent through.
// As such, it will not return an error if the update fails.
func (p *Paginator) Update() {
	page := *p.Page() // copy the page embed
	p.Widget.UpdateEmbedAsync(page, p.errorCh)
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
