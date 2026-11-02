package utils

import (
	"EverythingSuckz/fsb/internal/types"
	"context"
	"errors"
	"fmt"
	"math/rand"
	"mime"
	"strings"
	"time"

	"github.com/celestix/gotgproto/storage"
	"github.com/gotd/td/tg"
)

var ErrFileGone = errors.New("file is no longer available")

func Contains[T comparable](s []T, e T) bool {
	for _, v := range s {
		if v == e {
			return true
		}
	}
	return false
}

// ---- channels -----------------------------------------------------------------

// ResolveChannel returns an InputChannel the bot can use. Order: peer storage →
// stored access hash → ChannelsGetChannels (works when the bot is a member).
func ResolveChannel(ctx context.Context, api *tg.Client, ps *storage.PeerStorage, channelID, accessHash int64) (*tg.InputChannel, error) {
	if peer, ok := ps.GetInputPeerById(channelID).(*tg.InputPeerChannel); ok {
		return &tg.InputChannel{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash}, nil
	}
	if accessHash != 0 {
		return &tg.InputChannel{ChannelID: channelID, AccessHash: accessHash}, nil
	}
	return FetchChannel(ctx, api, ps, channelID)
}

func FetchChannel(ctx context.Context, api *tg.Client, ps *storage.PeerStorage, channelID int64) (*tg.InputChannel, error) {
	res, err := api.ChannelsGetChannels(ctx, []tg.InputChannelClass{&tg.InputChannel{ChannelID: channelID}})
	if err != nil {
		return nil, err
	}
	if len(res.GetChats()) == 0 {
		return nil, errors.New("channel not found — is the bot added to it?")
	}
	ch, ok := res.GetChats()[0].(*tg.Channel)
	if !ok {
		return nil, errors.New("that chat is not a channel")
	}
	ps.AddPeer(ch.GetID(), ch.AccessHash, storage.TypeChannel, "")
	return ch.AsInput(), nil
}

// BotCanPost reports whether the bot is creator/admin with post rights in channel.
func BotCanPost(ctx context.Context, api *tg.Client, channel *tg.InputChannel) (bool, error) {
	res, err := api.ChannelsGetParticipant(ctx, &tg.ChannelsGetParticipantRequest{
		Channel:     channel,
		Participant: &tg.InputPeerSelf{},
	})
	if err != nil {
		return false, err
	}
	switch p := res.Participant.(type) {
	case *tg.ChannelParticipantCreator:
		return true, nil
	case *tg.ChannelParticipantAdmin:
		return p.AdminRights.PostMessages, nil
	}
	return false, nil
}

// GetTGMessage fetches one message from any channel.
func GetTGMessage(ctx context.Context, api *tg.Client, ps *storage.PeerStorage, channelID, accessHash int64, messageID int) (*tg.Message, error) {
	channel, err := ResolveChannel(ctx, api, ps, channelID, accessHash)
	if err != nil {
		return nil, err
	}
	res, err := api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
		Channel: channel,
		ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: messageID}},
	})
	if err != nil {
		return nil, err
	}
	var msgs []tg.MessageClass
	switch r := res.(type) {
	case *tg.MessagesChannelMessages:
		msgs = r.Messages
	case *tg.MessagesMessages:
		msgs = r.Messages
	case *tg.MessagesMessagesSlice:
		msgs = r.Messages
	}
	if len(msgs) == 0 {
		return nil, ErrFileGone
	}
	m, ok := msgs[0].(*tg.Message)
	if !ok {
		return nil, ErrFileGone
	}
	return m, nil
}

// ForwardToChannel copies a user's message into a storage channel. It returns the
// new channel message.
func ForwardToChannel(ctx context.Context, api *tg.Client, ps *storage.PeerStorage, fromChatID int64, msgID int, channelID, accessHash int64) (int, *tg.Message, error) {
	fromPeer := ps.GetInputPeerById(fromChatID)
	if fromPeer.Zero() {
		return 0, nil, fmt.Errorf("fromChatId: %d is not a valid peer", fromChatID)
	}
	ch, err := ResolveChannel(ctx, api, ps, channelID, accessHash)
	if err != nil {
		return 0, nil, err
	}
	upd, err := api.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{
		RandomID: []int64{rand.Int63()},
		FromPeer: fromPeer,
		ID:       []int{msgID},
		ToPeer:   &tg.InputPeerChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
		// keep the file but drop the "forwarded from" header
		DropAuthor: true,
	})
	if err != nil {
		return 0, nil, err
	}
	updates, ok := upd.(*tg.Updates)
	if !ok {
		return 0, nil, errors.New("unexpected update type from Telegram")
	}
	// Find the new channel message in the updates (don't rely on fixed positions).
	for _, u := range updates.Updates {
		if nm, ok := u.(*tg.UpdateNewChannelMessage); ok {
			if m, ok := nm.Message.(*tg.Message); ok {
				return m.ID, m, nil
			}
		}
	}
	return 0, nil, errors.New("Telegram did not return the stored message")
}

// SendToChannel posts a text reply under a stored message (owner-channel log caption).
func PostLogCaption(ctx context.Context, api *tg.Client, ps *storage.PeerStorage, channelID, accessHash int64, replyTo int, text string) int {
	ch, err := ResolveChannel(ctx, api, ps, channelID, accessHash)
	if err != nil {
		return 0
	}
	upd, err := api.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
		Peer:      &tg.InputPeerChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
		Message:   text,
		ReplyTo:   &tg.InputReplyToMessage{ReplyToMsgID: replyTo},
		NoWebpage: true,
		RandomID:  rand.Int63(),
	})
	if err != nil {
		return 0
	}
	if u, ok := upd.(*tg.Updates); ok {
		for _, x := range u.Updates {
			if nm, ok := x.(*tg.UpdateNewChannelMessage); ok {
				if m, ok := nm.Message.(*tg.Message); ok {
					return m.ID
				}
			}
		}
	}
	return 0
}

// DeleteChannelMessages deletes messages (100 per request limit handled).
func DeleteChannelMessages(ctx context.Context, api *tg.Client, ps *storage.PeerStorage, channelID, accessHash int64, ids []int) error {
	if len(ids) == 0 {
		return nil
	}
	ch, err := ResolveChannel(ctx, api, ps, channelID, accessHash)
	if err != nil {
		return err
	}
	for i := 0; i < len(ids); i += 100 {
		j := i + 100
		if j > len(ids) {
			j = len(ids)
		}
		if _, err := api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{Channel: ch, ID: ids[i:j]}); err != nil {
			return err
		}
	}
	return nil
}

// ---- media ----------------------------------------------------------------------

func FileFromMedia(media tg.MessageMediaClass) (*types.File, error) {
	switch media := media.(type) {
	case *tg.MessageMediaDocument:
		document, ok := media.Document.AsNotEmpty()
		if !ok {
			return nil, ErrFileGone
		}
		var fileName string
		for _, a := range document.Attributes {
			if n, ok := a.(*tg.DocumentAttributeFilename); ok {
				fileName = n.FileName
				break
			}
		}
		return &types.File{
			Location: document.AsInputDocumentFileLocation(),
			FileSize: document.Size, FileName: fileName,
			MimeType: document.MimeType, ID: document.ID,
		}, nil
	case *tg.MessageMediaPhoto:
		photo, ok := media.Photo.AsNotEmpty()
		if !ok {
			return nil, ErrFileGone
		}
		var bestType string
		var bestBytes int64
		for _, s := range photo.Sizes {
			var b int64
			var t string
			switch v := s.(type) {
			case *tg.PhotoSize:
				b, t = int64(v.Size), v.Type
			case *tg.PhotoSizeProgressive:
				for _, x := range v.Sizes {
					if int64(x) > b {
						b = int64(x)
					}
				}
				t = v.Type
			default:
				continue
			}
			if b >= bestBytes {
				bestBytes, bestType = b, t
			}
		}
		if bestType == "" {
			return nil, errors.New("photo has no downloadable size")
		}
		return &types.File{
			Location: &tg.InputPhotoFileLocation{ID: photo.GetID(), AccessHash: photo.GetAccessHash(),
				FileReference: photo.GetFileReference(), ThumbSize: bestType},
			FileSize: bestBytes, FileName: fmt.Sprintf("photo_%d.jpg", photo.GetID()),
			MimeType: "image/jpeg", ID: photo.GetID(),
		}, nil
	}
	return nil, errors.New("unsupported media type")
}

var mimeExt = map[string]string{
	"video/mp4": ".mp4", "video/x-matroska": ".mkv", "video/webm": ".webm", "video/quicktime": ".mov",
	"audio/mpeg": ".mp3", "audio/ogg": ".ogg", "audio/mp4": ".m4a", "audio/x-wav": ".wav", "audio/flac": ".flac",
	"image/jpeg": ".jpg", "image/png": ".png", "image/webp": ".webp", "image/gif": ".gif",
	"application/pdf": ".pdf", "application/zip": ".zip",
	"application/vnd.android.package-archive": ".apk",
}

func DisplayName(f *types.File) string {
	name := strings.TrimSpace(f.FileName)
	if name != "" {
		return name
	}
	ext := mimeExt[strings.ToLower(strings.SplitN(f.MimeType, ";", 2)[0])]
	if ext == "" {
		if exts, _ := mime.ExtensionsByType(f.MimeType); len(exts) > 0 {
			ext = exts[0]
		}
	}
	return fmt.Sprintf("file_%d%s", f.ID, ext)
}

func IsStreamable(mimeType string) bool {
	return strings.Contains(mimeType, "video") || strings.Contains(mimeType, "audio") || strings.Contains(mimeType, "pdf")
}

// HumanLeft: "5h 59m", "42m", "<1m".
func HumanLeft(until time.Time) string {
	d := time.Until(until)
	if d <= 0 {
		return "expired"
	}
	h, m := int(d.Hours()), int(d.Minutes())%60
	switch {
	case h >= 48:
		return fmt.Sprintf("%dd %dh", h/24, h%24)
	case h > 0:
		return fmt.Sprintf("%dh %dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm", m)
	}
	return "<1m"
}

func HumanDuration(d time.Duration) string {
	h := d.Hours()
	if h >= 1 && h == float64(int(h)) {
		return fmt.Sprintf("%dh", int(h))
	}
	if h >= 1 {
		return fmt.Sprintf("%.1fh", h)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}
