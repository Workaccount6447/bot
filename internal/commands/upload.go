package commands

import (
	"context"
	"fmt"
	"strings"
	"time"

	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/database"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/storage"
	"github.com/celestix/gotgproto/types"
	"github.com/dustin/go-humanize"
	"github.com/gotd/td/tg"
)

func (m *command) LoadUpload(dispatcher dispatcher.Dispatcher) {
	log := m.log.Named("upload")
	defer log.Sugar().Info("Loaded")
	dispatcher.AddHandler(handlers.NewMessage(nil, m.sendLink))
}

func supportedMediaFilter(msg *types.Message) (bool, error) {
	if msg.Media == nil {
		return false, dispatcher.EndGroups
	}
	switch msg.Media.(type) {
	case *tg.MessageMediaDocument, *tg.MessageMediaPhoto:
		return true, nil
	}
	return false, dispatcher.EndGroups
}

// storeFile forwards the user's message into the right storage channel and
// records it in the DB. batchID is "" for single uploads.
//
//	user has own channel  -> their channel, never expires
//	otherwise             -> owner channel (LOG_CHANNEL), expires after FILE_EXPIRY_HOURS
func (m *command) storeFile(ctx *ext.Context, chatID int64, srcMsgID int, batchID string) (*database.FileLink, error) {
	bg := context.Background()
	db := database.GetDB()

	var chID, chHash int64
	custom := false
	if user, err := db.GetUser(bg, chatID); err == nil && user.CustomChannelID != 0 {
		chID, chHash, custom = user.CustomChannelID, user.CustomChannelHash, true
	} else {
		chID = config.ValueOf.LogChannelID
	}

	storedID, stored, err := utils.ForwardToChannel(bg, ctx.Raw, ctx.PeerStorage, chatID, srcMsgID, chID, chHash)
	if err != nil {
		if custom {
			return nil, fmt.Errorf("couldn't save to your channel (%v). Make sure I'm still an admin there, or use /removechannel", err)
		}
		return nil, err
	}
	file, err := utils.FileFromMedia(stored.Media)
	if err != nil {
		return nil, err
	}

	link := &database.FileLink{
		LinkID:            utils.NewLinkID(),
		BatchID:           batchID,
		UserID:            chatID,
		ChannelID:         chID,
		ChannelAccessHash: chHash,
		Custom:            custom,
		MessageID:         storedID,
		FileName:          utils.DisplayName(file),
		FileSize:          file.FileSize,
		MimeType:          file.MimeType,
		CreatedAt:         now(),
	}
	if !custom {
		if d := config.ValueOf.FileExpiry(); d > 0 {
			t := now().Add(d)
			link.ExpiresAt = &t
		}
		// Owner log caption (also deleted on expiry)
		caption := fmt.Sprintf("👤 User ID: %d\n📂 File: %s\n📦 Size: %s\n🎭 Mime: %s\n🔗 Stream: %s\n⏳ Expires: %s",
			chatID, link.FileName, humanize.IBytes(uint64(link.FileSize)), link.MimeType,
			utils.StreamURL(link.LinkID), expiresText(link))
		link.LogMsgID = utils.PostLogCaption(bg, ctx.Raw, ctx.PeerStorage, chID, chHash, storedID, caption)
	}
	if err := db.InsertLink(bg, link); err != nil {
		// don't leave an orphan file behind
		_ = utils.DeleteChannelMessages(bg, ctx.Raw, ctx.PeerStorage, chID, chHash, []int{storedID, link.LogMsgID})
		return nil, fmt.Errorf("database error: %w", err)
	}
	db.IncrLinks(bg, chatID, 1)
	return link, nil
}

func expiresText(l *database.FileLink) string {
	if l.ExpiresAt == nil {
		return "never"
	}
	return l.ExpiresAt.UTC().Format(time.RFC1123)
}

func (m *command) sendLink(ctx *ext.Context, u *ext.Update) error {
	chatID := u.EffectiveChat().GetID()
	if ctx.PeerStorage.GetPeerById(chatID).Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}
	if !guard(ctx, u, chatID) {
		return dispatcher.EndGroups
	}

	// A forwarded post while the user is in the /setchannel flow = channel selection.
	if m.handleChannelForward(ctx, u, chatID) {
		return dispatcher.EndGroups
	}

	supported, err := supportedMediaFilter(u.EffectiveMessage)
	if err != nil {
		return err
	}
	if !supported {
		ctx.Reply(u, ext.ReplyTextString("Sorry, this message type is unsupported."), nil)
		return dispatcher.EndGroups
	}
	if !database.IsEnabled() {
		ctx.Reply(u, ext.ReplyTextString("❌ Database is not available right now."), nil)
		return dispatcher.EndGroups
	}

	// ---- multi-link mode: collect silently, answer on /done
	if sess := batches.get(chatID); sess != nil {
		return m.addToBatch(ctx, u, chatID, sess)
	}

	link, err := m.storeFile(ctx, chatID, u.EffectiveMessage.ID, "")
	if err != nil {
		utils.Logger.Sugar().Errorf("store file failed: %v", err)
		ctx.Reply(u, ext.ReplyTextString("❌ "+err.Error()), nil)
		return dispatcher.EndGroups
	}

	botUsername := ctx.Self.Username
	opts := &ext.ReplyOpts{ReplyToMessageId: u.EffectiveMessage.ID}
	if !strings.Contains(config.ValueOf.StreamHost, "http://localhost") {
		opts.Markup = singleLinkMarkup(botUsername, link)
	}
	sent, err := ctx.Reply(u, ext.ReplyTextStyledTextArray(singleLinkParts(botUsername, link)), opts)
	if err != nil {
		utils.Logger.Sugar().Error(err)
		ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("Error - %s", err.Error())), nil)
		return dispatcher.EndGroups
	}
	// remember WHICH message shows the links so the cleaner can edit it on expiry
	if sent != nil {
		_ = database.GetDB().SetNotify(context.Background(), link.LinkID, chatID, sent.ID, userHash(ctx, chatID))
	}
	return dispatcher.EndGroups
}
