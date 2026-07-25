package runtime

import (
	"testing"

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
