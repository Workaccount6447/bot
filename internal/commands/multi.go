package commands

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/database"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/dustin/go-humanize"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
)

// ---- multi-link ("batch") mode -------------------------------------------------
//
//   /multi   start collecting files
//   (send up to MAX_BATCH files — nothing is answered one by one)
//   /done    ONE message with every link
//   /cancel  abort (files already stored are removed)

type batchSession struct {
	id        string
	links     []*database.FileLink
	statusMsg int
	lastSeen  time.Time
}

type batchStore struct {
	mu sync.Mutex
	m  map[int64]*batchSession
}

var batches = &batchStore{m: map[int64]*batchSession{}}

func (b *batchStore) get(userID int64) *batchSession {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.m[userID]
	if s != nil && time.Since(s.lastSeen) > time.Duration(config.ValueOf.BatchTimeoutMins)*time.Minute {
		delete(b.m, userID) // idle too long; already-stored files expire on their own
		return nil
	}
	if s != nil {
		s.lastSeen = time.Now()
	}
	return s
}

func (b *batchStore) start(userID int64) *batchSession {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := &batchSession{id: utils.NewLinkID(), lastSeen: time.Now()}
	b.m[userID] = s
	return s
}

func (b *batchStore) end(userID int64) {
	b.mu.Lock()
	delete(b.m, userID)
	b.mu.Unlock()
}

func (m *command) LoadMulti(d dispatcher.Dispatcher) {
	log := m.log.Named("multi")
	defer log.Sugar().Info("Loaded")
	d.AddHandler(handlers.NewCommand("multi", m.multiStart))
	d.AddHandler(handlers.NewCommand("done", m.multiDone))
	d.AddHandler(handlers.NewCommand("cancel", m.multiCancel))
}

func (m *command) multiStart(ctx *ext.Context, u *ext.Update) error {
	chatID := u.EffectiveChat().GetID()
	if !guard(ctx, u, chatID) {
		return dispatcher.EndGroups
	}
	batches.start(chatID)
	exp := "Fɪʟᴇs ɪɴ ᴛʜᴇ ʙᴏᴛ's sᴛᴏʀᴀɢᴇ ᴇxᴘɪʀᴇ ᴀғᴛᴇʀ " + utils.HumanDuration(config.ValueOf.FileExpiry())
	if config.ValueOf.FileExpiry() == 0 {
		exp = "Fɪʟᴇs ɴᴇᴠᴇʀ ᴇxᴘɪʀᴇ"
	}
	ctx.Reply(u, ext.ReplyTextStyledTextArray([]styling.StyledTextOption{
		styling.Bold("📦 Mᴜʟᴛɪ-ʟɪɴᴋ ᴍᴏᴅᴇ ᴏɴ\n\n"),
		styling.Plain(fmt.Sprintf("Send me up to %d files. I'll stay quiet until you send /done, then give you ALL the links in one message.\n\n", config.ValueOf.MaxBatch)),
		styling.Plain("/done – get the links\n/cancel – abort\n\n"),
		styling.Italic(exp),
	}), nil)
	return dispatcher.EndGroups
}

func (m *command) addToBatch(ctx *ext.Context, u *ext.Update, chatID int64, s *batchSession) error {
	if len(s.links) >= config.ValueOf.MaxBatch {
		ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("⚠️ Batch is full (%d files). Send /done to get your links.", config.ValueOf.MaxBatch)), nil)
		return dispatcher.EndGroups
	}
	link, err := m.storeFile(ctx, chatID, u.EffectiveMessage.ID, s.id)
	if err != nil {
		utils.Logger.Sugar().Errorf("batch store failed: %v", err)
		ctx.Reply(u, ext.ReplyTextString("❌ "+err.Error()), nil)
		return dispatcher.EndGroups
	}
	s.links = append(s.links, link)

	text := fmt.Sprintf("📥 %d file(s) added.\nSend more, or /done to get all links.", len(s.links))
	if s.statusMsg != 0 { // keep ONE status message instead of spamming
		_, err := ctx.Raw.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{
			Peer: userPeer(ctx, chatID), ID: s.statusMsg, Message: text,
		})
		if err == nil || strings.Contains(fmt.Sprint(err), "MESSAGE_NOT_MODIFIED") {
			return dispatcher.EndGroups
		}
	}
	if sent, err := ctx.Reply(u, ext.ReplyTextString(text), nil); err == nil && sent != nil {
		s.statusMsg = sent.ID
	}
	return dispatcher.EndGroups
}

func (m *command) multiCancel(ctx *ext.Context, u *ext.Update) error {
	chatID := u.EffectiveChat().GetID()
	s := batches.get(chatID)
	if s == nil {
		ctx.Reply(u, ext.ReplyTextString("You're not in multi-link mode."), nil)
		return dispatcher.EndGroups
	}
	batches.end(chatID)
	bg := context.Background()
	for _, l := range s.links {
		m.removeStored(ctx, l)
		_ = database.GetDB().DeleteLink(bg, l.LinkID)
	}
	ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("❌ Cancelled. %d file(s) discarded.", len(s.links))), nil)
	return dispatcher.EndGroups
}

func (m *command) multiDone(ctx *ext.Context, u *ext.Update) error {
	chatID := u.EffectiveChat().GetID()
	s := batches.get(chatID)
	if s == nil {
		ctx.Reply(u, ext.ReplyTextString("You're not in multi-link mode. Send /multi first."), nil)
		return dispatcher.EndGroups
	}
	if len(s.links) == 0 {
		ctx.Reply(u, ext.ReplyTextString("No files yet — send some files, then /done. (/cancel to abort)"), nil)
		return dispatcher.EndGroups
	}
	batches.end(chatID)

	// Restart the expiry clock for the whole batch so every link in the message
	// dies together (and the message is edited once).
	var exp *time.Time
	if d := config.ValueOf.FileExpiry(); d > 0 && !s.links[0].Custom {
		t := now().Add(d)
		exp = &t
		for _, l := range s.links {
			l.ExpiresAt = &t
		}
	}

	botUsername := ctx.Self.Username
	chunks := chunkLinks(s.links, 3300)
	bg := context.Background()
	total := len(s.links)
	idx := 0
	for ci, chunk := range chunks {
		parts := batchParts(botUsername, chunk, idx, total, ci+1, len(chunks))
		text, ents := StyledToRaw(parts)
		res, err := ctx.Raw.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
			Peer: userPeer(ctx, chatID), Message: text, Entities: ents,
			NoWebpage: true, RandomID: randID(),
			ReplyMarkup: &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{
				&tg.KeyboardButtonCallback{Text: "ᴄʟᴏsᴇ", Data: []byte("close")},
			}}}},
		})
		if err != nil {
			utils.Logger.Sugar().Error(err)
			continue
		}
		msgID := sentMessageID(res)
		ids := make([]string, 0, len(chunk))
		for _, l := range chunk {
			ids = append(ids, l.LinkID)
		}
		_ = database.GetDB().FinalizeBatch(bg, ids, exp, chatID, msgID, userHash(ctx, chatID))
		idx += len(chunk)
	}
	return dispatcher.EndGroups
}

// removeStored deletes a stored file (+ caption) — only from the OWNER channel.
// A user's own channel is never touched.
func (m *command) removeStored(ctx *ext.Context, l *database.FileLink) {
	if l.Custom {
		return
	}
	ids := []int{l.MessageID}
	if l.LogMsgID != 0 {
		ids = append(ids, l.LogMsgID)
	}
	_ = utils.DeleteChannelMessages(context.Background(), ctx.Raw, ctx.PeerStorage, l.ChannelID, l.ChannelAccessHash, ids)
}

func chunkLinks(links []*database.FileLink, maxChars int) [][]*database.FileLink {
	var out [][]*database.FileLink
	var cur []*database.FileLink
	size := 0
	for _, l := range links {
		n := len(l.FileName) + 3*len(utils.StreamURL(l.LinkID)) + 120
		if len(cur) > 0 && size+n > maxChars {
			out = append(out, cur)
			cur, size = nil, 0
		}
		cur = append(cur, l)
		size += n
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

func batchParts(botUsername string, chunk []*database.FileLink, offset, total, part, parts int) []styling.StyledTextOption {
	head := fmt.Sprintf("📦 Yᴏᴜʀ %d Lɪɴᴋs", total)
	if parts > 1 {
		head += fmt.Sprintf(" (ᴘᴀʀᴛ %d/%d)", part, parts)
	}
	out := []styling.StyledTextOption{styling.Bold(head + "\n\n")}
	for i, l := range chunk {
		out = append(out,
			styling.Bold(fmt.Sprintf("%d. ", offset+i+1)), styling.Bold(l.FileName),
			styling.Plain("  ("), styling.Code(humanize.IBytes(uint64(l.FileSize))), styling.Plain(")\n"),
			styling.Plain("📥 "), styling.Code(utils.DownloadURL(l.LinkID)), styling.Plain("\n"),
		)
		if isMediaLink(l.MimeType) {
			out = append(out, styling.Plain("🖥 "), styling.Code(utils.StreamURL(l.LinkID)), styling.Plain("\n"))
		}
		out = append(out, styling.Plain("🔗 "), styling.Code(utils.ShareURL(botUsername, l.LinkID)), styling.Plain("\n\n"))
	}
	out = append(out, styling.Italic(expiryLine(chunk[0])))
	return out
}
