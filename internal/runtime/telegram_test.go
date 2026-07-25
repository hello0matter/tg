package runtime

import (
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

func TestPeerID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		peer tg.PeerClass
		want int64
	}{
		{peer: &tg.PeerUser{UserID: 42}, want: 42},
		{peer: &tg.PeerChat{ChatID: 42}, want: -42},
		{peer: &tg.PeerChannel{ChannelID: 42}, want: -1_000_000_000_042},
	}
	for _, tc := range cases {
		if got := peerID(tc.peer); got != tc.want {
			t.Fatalf("peerID(%T) = %d, want %d", tc.peer, got, tc.want)
		}
	}
}

func TestFormatButtons(t *testing.T) {
	t.Parallel()
	markup := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{
		&tg.KeyboardButtonURL{Text: "官网", URL: "https://example.com"},
		&tg.KeyboardButtonCallback{Text: "查询", Data: []byte("lookup")},
	}}}}
	if got := formatButtons(markup, "urls_only"); got != "官网 https://example.com" {
		t.Fatalf("urls_only = %q", got)
	}
	if got := formatButtons(markup, "as_text"); got != "官网 https://example.com\n[按钮] 查询" {
		t.Fatalf("as_text = %q", got)
	}
	if got := formatButtons(markup, "drop"); got != "" {
		t.Fatalf("drop = %q", got)
	}
}

func TestFloodWait(t *testing.T) {
	t.Parallel()
	if got := floodWait(errors.New("rpc: FLOOD_WAIT_12")); got != 13*time.Second {
		t.Fatalf("floodWait = %v", got)
	}
}

func TestReusableMedia(t *testing.T) {
	t.Parallel()
	photo := &tg.Photo{ID: 1, AccessHash: 2, FileReference: []byte{3}}
	if _, err := reusableMedia(&tg.MessageMediaPhoto{Photo: photo}, "new caption"); err != nil {
		t.Fatal(err)
	}
	if _, err := reusableMedia(&tg.MessageMediaPoll{}, "poll"); errUnsupportedMedia == nil || err == nil {
		t.Fatal("poll should not be reusable as copied media")
	}
}

func TestSentMessageIDs(t *testing.T) {
	t.Parallel()
	updates := &tg.Updates{Updates: []tg.UpdateClass{
		&tg.UpdateMessageID{ID: 12},
		&tg.UpdateMessageID{ID: 11},
	}}
	ids := sentMessageIDs(updates)
	if len(ids) != 2 || ids[0] != 11 || ids[1] != 12 {
		t.Fatalf("unexpected IDs: %v", ids)
	}
}
