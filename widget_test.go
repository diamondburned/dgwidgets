package dgwidgets

import (
	"context"
	"sync"
	"testing"

	"github.com/diamondburned/arikawa/v2/api"
	"github.com/diamondburned/arikawa/v2/discord"
	"github.com/diamondburned/arikawa/v2/gateway"
	"github.com/diamondburned/arikawa/v2/state"
	"github.com/mavolin/dismock/v2/pkg/dismock"
)

func newTestWidget(t *testing.T, ctx context.Context) (*Widget, *state.State) {
	mock, state := dismock.NewState(t)

	expectMsg := discord.Message{
		ID:        1,
		ChannelID: 1,
		Content:   "test",
		Nonce:     "nonce",
		Embeds:    []discord.Embed{{Title: "a"}},
	}

	w := NewWidget(state, expectMsg.ChannelID)
	w.UseContext(ctx) // err doesn't happen before Wait.
	w.SendData = api.SendMessageData{
		Content: expectMsg.Content,
		Nonce:   expectMsg.Nonce,
		Embed:   &expectMsg.Embeds[0],
	}

	// Describe the API calls.
	mock.Me(discord.User{ID: 3})
	mock.SendMessageComplex(w.SendData, expectMsg)
	mock.React(expectMsg.ChannelID, expectMsg.ID, "a")
	mock.React(expectMsg.ChannelID, expectMsg.ID, "b")

	if err := w.BindMessage(); err != nil {
		t.Fatal("failed to bind message:", err)
	}
	if err := w.SendReactions(); err != nil {
		t.Fatal("failed to add reactions:", err)
	}

	return w, state
}

func TestWidget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w, state := newTestWidget(t, ctx)

	recvCh := make(chan string)

	w.Handle("a", func(r *gateway.MessageReactionAddEvent) {
		select {
		case <-ctx.Done():
		case recvCh <- "a":
		}
	})
	w.Handle("b", func(r *gateway.MessageReactionAddEvent) {
		select {
		case <-ctx.Done():
		case recvCh <- "b":
		}
	})

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		w.Wait()
		wg.Done()
	}()

	expectRecv := map[string]int{"a": 2, "b": 2}
	recv := map[string]int{}

	for i := 0; i < 3; i++ {
		for emoji, times := range expectRecv {
			for times != 0 {
				times--

				state.Handler.Call(&gateway.MessageReactionAddEvent{
					MessageID: w.SentMessage.ID,
					ChannelID: w.SentMessage.ChannelID,
					UserID:    4,
					Emoji:     discord.Emoji{Name: emoji},
				})
			}
		}
	}

	for emoji := range recvCh {
		recv[emoji]++

		if equalMap(recv, expectRecv) {
			break
		}
	}

	cancel()
	wg.Wait()
}

func equalMap(got, expect map[string]int) bool {
	if len(got) != len(expect) {
		return false
	}

	for name, total := range got {
		if t, ok := expect[name]; !ok || t != total {
			return false
		}
	}

	return true
}

func TestWidgetSingleRecv(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	w, state := newTestWidget(t, ctx)

	var total int
	w.Handle("a", func(r *gateway.MessageReactionAddEvent) {
		total++
		cancel()
	})
	w.Handle("b", func(r *gateway.MessageReactionAddEvent) {
		total++
		cancel()
	})

	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		w.Wait()
		wg.Done()
	}()

	for i := 0; i < 100; i++ {
		for _, emoji := range []string{"a", "b"} {
			state.Handler.Call(&gateway.MessageReactionAddEvent{
				MessageID: w.SentMessage.ID,
				ChannelID: w.SentMessage.ChannelID,
				UserID:    4,
				Emoji:     discord.Emoji{Name: emoji},
			})
		}
	}

	wg.Wait()

	if total != 1 {
		t.Fatal("handler called more than once")
	}
}

func messageFromData(data api.SendMessageData) discord.Message {
	msg := discord.Message{
		Content: data.Content,
		Nonce:   data.Nonce,
		TTS:     data.TTS,
	}
	if data.Embed != nil {
		msg.Embeds = []discord.Embed{*data.Embed}
	}

	return msg
}
