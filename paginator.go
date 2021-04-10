package dgwidgets

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/diamondburned/arikawa/v2/api"
	"github.com/diamondburned/arikawa/v2/discord"
	"github.com/diamondburned/arikawa/v2/gateway"
	"github.com/diamondburned/arikawa/v2/state"
)

// Emoji variables.
var (
	NavPlus        discord.APIEmoji = "➕"
	NavPlay        discord.APIEmoji = "▶"
	NavPause       discord.APIEmoji = "⏸"
	NavStop        discord.APIEmoji = "⏹"
	NavRight       discord.APIEmoji = "➡"
	NavLeft        discord.APIEmoji = "⬅"
	NavUp          discord.APIEmoji = "⬆"
	NavDown        discord.APIEmoji = "⬇"
	NavEnd         discord.APIEmoji = "⏩"
	NavBeginning   discord.APIEmoji = "⏪"
	NavNumbers     discord.APIEmoji = "🔢"
	NavInformation discord.APIEmoji = "ℹ"
	NavSave        discord.APIEmoji = "💾"
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

	index         int
	bgWait        sync.WaitGroup
	errorCh       chan error
	pageChange    chan PageChange
	timeoutCancel context.CancelFunc
}

// NewPaginator returns a new Paginator.
//
//    ses      : discordgo session
//    channelID: channelID to spawn the paginator on
func NewPaginator(state *state.State, channelID discord.ChannelID) *Paginator {
	return &Paginator{
		Widget: NewWidget(state, channelID),

		DeleteReactionsWhenDone: true,
		ColourWhenDone:          0xFF0000,

		errorCh:    make(chan error, 1),
		pageChange: make(chan PageChange, 1),
	}
}

// UseContext sets the Paginator and Widget's contexts. It assumes that only one
// thread will use Paginator, and Widget will only error out when Spawn() is
// running, thus it assumes that no errors will be returned.
func (p *Paginator) UseContext(ctx context.Context) {
	if p.timeoutCancel != nil {
		p.timeoutCancel()
		p.timeoutCancel = nil
	}

	p.Widget.setContext(ctx)
}

// SetTimeout sets the timeout of the paginator. The returned function is used
// for stopping Paginator; it is optional to call the function. For more
// advanced cancellation and timeout control, UseContext should be used instead.
func (p *Paginator) SetTimeout(timeout time.Duration) context.CancelFunc {
	ctx, cancel := context.WithTimeout(p.Widget.ctx, timeout)
	p.UseContext(ctx)
	p.timeoutCancel = cancel
	return cancel
}

// Index returns the current zero-indexed page number.
func (p *Paginator) Index() int {
	return p.index
}

func (p *Paginator) sendChange(ch PageChange) {
	select {
	case <-p.Widget.Done():
	case p.pageChange <- ch:
	}
}

func (p *Paginator) sendErr(err error) {
	if err != nil {
		select {
		case <-p.Widget.Done():
		case p.errorCh <- err:
		}
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
		state := p.Widget.State.WithContext(ctx)

		const prompt = "enter the page number you would like to open."

		m, err := QueryInput(state, p.Widget.ChannelID, r.UserID, prompt, true)
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
			p.sendErr(fmt.Errorf("input %d is zero or negative", n))
			return
		}

		p.sendChange(PageChange(n - 1)) // account for zero-indexed
	})

	p.Widget.SendData.Embed = p.Page()

	p.bgWait.Add(1)
	go func() {
		p.sendErr(p.Widget.Spawn())
		p.bgWait.Done()
	}()

	defer p.bgWait.Wait()

	// Ensure the context is cleaned up once the time runs out.
	if p.timeoutCancel != nil {
		defer p.timeoutCancel()
	}

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
				err = fmt.Errorf("page %d is out of bounds", change)
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
	messageID := p.Widget.SentMessage.ID
	channelID := p.Widget.SentMessage.ChannelID

	if p.DeleteMessageWhenDone {
		p.Widget.bgClient(&p.bgWait, func(client *api.Client) error {
			return client.DeleteMessage(channelID, messageID)
		})

		// Don't bother doing the rest of the tasks because the message is gone.
		return
	}

	if p.ColourWhenDone > 0 {
		page := *p.Page() // copy for thread-safety
		page.Color = p.ColourWhenDone

		p.Widget.bgClient(&p.bgWait, func(client *api.Client) error {
			_, err := client.EditEmbed(channelID, messageID, page)
			return err
		})
	}

	// Delete reactions when done
	if p.DeleteReactionsWhenDone {
		p.Widget.bgClient(&p.bgWait, func(client *api.Client) error {
			return client.DeleteAllReactions(channelID, messageID)
		})
	}

	return
}

// Add a page to the paginator and automatically updates embed footers.
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
	p.Widget.UpdateMessageAsync(p.errorCh, api.EditMessageData{
		Embed: p.Page(),
	})
}

// SetPageFooters sets the footer of each embed to the page number out of the
// total length of the embeds.
func (p *Paginator) SetPageFooters() {
	for i := range p.Pages {
		p.Pages[i].Footer = &discord.EmbedFooter{
			Text: fmt.Sprintf("#[%d / %d]", i+1, len(p.Pages)),
		}
	}
}
