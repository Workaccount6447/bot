package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/database"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/storage"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
)

// ---- choose where files are stored ---------------------------------------------
//
//   default            -> owner's channel (LOG_CHANNEL); links expire after FILE_EXPIRY_HOURS
//   /setchannel        -> user's OWN channel; links never expire
//   /removechannel     -> back to the owner's channel

var awaiting = struct {
	mu sync.Mutex
	m  map[int64]time.Time
}{m: map[int64]time.Time{}}

func setAwaiting(id int64, on bool) {
	awaiting.mu.Lock()
	defer awaiting.mu.Unlock()
	if on {
		awaiting.m[id] = time.Now()
	} else {
		delete(awaiting.m, id)
	}
}

func isAwaiting(id int64) bool {
	awaiting.mu.Lock()
	defer awaiting.mu.Unlock()
	t, ok := awaiting.m[id]
	if ok && time.Since(t) > 10*time.Minute {
		delete(awaiting.m, id)
		return false
	}
	return ok
}

func (m *command) LoadStorage(d dispatcher.Dispatcher) {
	log := m.log.Named("storage")
	defer log.Sugar().Info("Loaded")
	d.AddHandler(handlers.NewCommand("storage", m.storageCmd))
	d.AddHandler(handlers.NewCommand("setchannel", m.setChannel))
	d.AddHandler(handlers.NewCommand("removechannel", m.removeChannel))
}

func storageSummary(user *database.User) ([]styling.StyledTextOption, *tg.ReplyInlineMarkup) {
	var parts []styling.StyledTextOption
	var rows []tg.KeyboardButtonRow
	if user != nil && user.CustomChannelID != 0 {
		title := user.CustomChannelTitle
		if title == "" {
			title = strconv.FormatInt(user.CustomChannelID, 10)
		}
		parts = []styling.StyledTextOption{
			styling.Bold("💾 Sᴛᴏʀᴀɢᴇ : Yᴏᴜʀ ᴏᴡɴ ᴄʜᴀɴɴᴇʟ\n\n"),
			styling.Plain("📡 "), styling.Bold(title), styling.Plain("\n"),
			styling.Plain("♾ Your files are saved in your channel and links never expire.\n\n"),
			styling.Italic("Use the button below to go back to the bot's temporary storage."),
		}
		rows = append(rows, tg.KeyboardButtonRow{Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonCallback{Text: "🗑 ᴜsᴇ ʙᴏᴛ sᴛᴏʀᴀɢᴇ", Data: []byte("st_remove")},
		}})
	} else {
		exp := "files never expire"
		if d := config.ValueOf.FileExpiry(); d > 0 {
			exp = "files are deleted after " + utils.HumanDuration(d)
		}
		parts = []styling.StyledTextOption{
			styling.Bold("💾 Sᴛᴏʀᴀɢᴇ : Bᴏᴛ ᴄʜᴀɴɴᴇʟ (ᴛᴇᴍᴘᴏʀᴀʀʏ)\n\n"),
			styling.Plain("⏳ On this storage " + exp + " and the links stop working.\n\n"),
			styling.Italic("Want permanent links? Connect your own channel — the bot will save your files there and they never expire."),
		}
		rows = append(rows, tg.KeyboardButtonRow{Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonCallback{Text: "📡 sᴇᴛ ᴍʏ ᴏᴡɴ ᴄʜᴀɴɴᴇʟ", Data: []byte("st_set")},
		}})
	}
	rows = append(rows, tg.KeyboardButtonRow{Buttons: []tg.KeyboardButtonClass{
		&tg.KeyboardButtonCallback{Text: "ᴄʟᴏsᴇ", Data: []byte("close")},
	}})
	return parts, &tg.ReplyInlineMarkup{Rows: rows}
}

func (m *command) storageCmd(ctx *ext.Context, u *ext.Update) error {
	chatID := u.EffectiveChat().GetID()
	if !guard(ctx, u, chatID) {
		return dispatcher.EndGroups
	}
	var user *database.User
	if database.IsEnabled() {
		user, _ = database.GetDB().GetUser(context.Background(), chatID)
	}
	parts, markup := storageSummary(user)
	ctx.Reply(u, ext.ReplyTextStyledTextArray(parts), &ext.ReplyOpts{Markup: markup})
	return dispatcher.EndGroups
}

const setChannelHelp = `📡 Connect your own channel

1. Create a channel (private is fine).
2. Add this bot as an ADMIN with the "Post messages" permission.
3. Now do ONE of these:
   • forward any post from that channel to me, or
   • send:  /setchannel @username   or   /setchannel -100123456789

Files you send afterwards are saved in YOUR channel and their links never expire.

⚠️ Don't delete files from your channel yourself — those links would stop working.`

func (m *command) setChannel(ctx *ext.Context, u *ext.Update) error {
	chatID := u.EffectiveChat().GetID()
	if !guard(ctx, u, chatID) {
		return dispatcher.EndGroups
	}
	if !database.IsEnabled() {
		ctx.Reply(u, ext.ReplyTextString("❌ Database not configured."), nil)
		return dispatcher.EndGroups
	}
	fields := strings.Fields(u.EffectiveMessage.Text)
	if len(fields) >= 2 {
		m.applyChannelArg(ctx, u, chatID, fields[1])
		return dispatcher.EndGroups
	}
	setAwaiting(chatID, true)
	ctx.Reply(u, ext.ReplyTextString(setChannelHelp), nil)
	return dispatcher.EndGroups
}

func (m *command) removeChannel(ctx *ext.Context, u *ext.Update) error {
	chatID := u.EffectiveChat().GetID()
	if !guard(ctx, u, chatID) || !database.IsEnabled() {
		return dispatcher.EndGroups
	}
	setAwaiting(chatID, false)
	_ = database.GetDB().ClearCustomChannel(context.Background(), chatID)
	ctx.Reply(u, ext.ReplyTextString("✅ Back to the bot's temporary storage. Old links in your channel keep working."), nil)
	return dispatcher.EndGroups
}

// handleChannelForward is called for every incoming message. It returns true if the
// message was consumed as a channel selection.
func (m *command) handleChannelForward(ctx *ext.Context, u *ext.Update, chatID int64) bool {
	if !isAwaiting(chatID) {
		return false
	}
	raw := u.EffectiveMessage.Message
	if raw == nil {
		return false
	}
	fwd, ok := raw.GetFwdFrom()
	if !ok {
		return false
	}
	peer, ok := fwd.GetFromID()
	if !ok {
		ctx.Reply(u, ext.ReplyTextString("I couldn't read the channel from that post (the channel hides its source). Use /setchannel @username or the numeric ID instead."), nil)
		return true
	}
	pc, ok := peer.(*tg.PeerChannel)
	if !ok {
		ctx.Reply(u, ext.ReplyTextString("That post is not from a channel. Forward a post from your channel."), nil)
		return true
	}
	m.connectChannel(ctx, u, chatID, pc.ChannelID, 0)
	return true
}

func (m *command) applyChannelArg(ctx *ext.Context, u *ext.Update, chatID int64, arg string) {
	arg = strings.TrimSpace(arg)
	arg = strings.TrimPrefix(strings.TrimPrefix(arg, "https://t.me/"), "http://t.me/")
	arg = strings.TrimPrefix(arg, "@")
	if id, err := strconv.ParseInt(arg, 10, 64); err == nil {
		m.connectChannel(ctx, u, chatID, config.StripChannelID(id), 0)
		return
	}
	res, err := ctx.Raw.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: arg})
	if err != nil {
		ctx.Reply(u, ext.ReplyTextString("❌ I couldn't find that channel. Check the username (private channels: forward a post instead)."), nil)
		return
	}
	for _, c := range res.Chats {
		if ch, ok := c.(*tg.Channel); ok {
			m.connectChannel(ctx, u, chatID, ch.ID, ch.AccessHash)
			return
		}
	}
	ctx.Reply(u, ext.ReplyTextString("❌ That username is not a channel."), nil)
}

func (m *command) connectChannel(ctx *ext.Context, u *ext.Update, chatID, channelID, hash int64) {
	bg := context.Background()
	if hash == 0 {
		if p, ok := ctx.PeerStorage.GetInputPeerById(channelID).(*tg.InputPeerChannel); ok {
			hash = p.AccessHash
		}
	}
	res, err := ctx.Raw.ChannelsGetChannels(bg, []tg.InputChannelClass{&tg.InputChannel{ChannelID: channelID, AccessHash: hash}})
	if err != nil || len(res.GetChats()) == 0 {
		ctx.Reply(u, ext.ReplyTextString("❌ I can't see that channel. Add me as an admin first (with “Post messages”), then forward a post from it to me."), nil)
		return
	}
	ch, ok := res.GetChats()[0].(*tg.Channel)
	if !ok {
		ctx.Reply(u, ext.ReplyTextString("❌ That is not a channel."), nil)
		return
	}
	ctx.PeerStorage.AddPeer(ch.GetID(), ch.AccessHash, storage.TypeChannel, "")
	canPost, err := utils.BotCanPost(bg, ctx.Raw, ch.AsInput())
	if err != nil || !canPost {
		ctx.Reply(u, ext.ReplyTextString("❌ I'm not an admin with “Post messages” in that channel. Fix the permissions and try again."), nil)
		return
	}
	if ch.ID == config.ValueOf.LogChannelID {
		ctx.Reply(u, ext.ReplyTextString("❌ That is the bot's own storage channel. Use a channel you own."), nil)
		return
	}
	if err := database.GetDB().SetCustomChannel(bg, chatID, ch.ID, ch.AccessHash, ch.Title); err != nil {
		ctx.Reply(u, ext.ReplyTextString("❌ Database error: "+err.Error()), nil)
		return
	}
	setAwaiting(chatID, false)
	ctx.Reply(u, ext.ReplyTextStyledTextArray([]styling.StyledTextOption{
		styling.Bold("✅ Cʜᴀɴɴᴇʟ ᴄᴏɴɴᴇᴄᴛᴇᴅ\n\n"),
		styling.Plain("📡 "), styling.Bold(ch.Title), styling.Plain("\n\n"),
		styling.Plain(fmt.Sprintf("From now on your files are saved in this channel and their links %s.\n\n", "never expire")),
		styling.Italic("/removechannel to go back to the bot's temporary storage."),
	}), nil)
}

// storageCallback handles the inline buttons of /storage.
func (m *command) storageCallback(ctx *ext.Context, u *ext.Update, action string) error {
	chatID := u.EffectiveChat().GetID()
	ctx.AnswerCallback(&tg.MessagesSetBotCallbackAnswerRequest{QueryID: u.CallbackQuery.QueryID})
	switch action {
	case "set":
		setAwaiting(chatID, true)
		ctx.Raw.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{
			Peer: userPeer(ctx, chatID), ID: u.CallbackQuery.MsgID, Message: setChannelHelp,
			ReplyMarkup: &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{
				&tg.KeyboardButtonCallback{Text: "ᴄʟᴏsᴇ", Data: []byte("close")},
			}}}},
		})
	case "remove":
		_ = database.GetDB().ClearCustomChannel(context.Background(), chatID)
		parts, markup := storageSummary(nil)
		text, ents := StyledToRaw(parts)
		ctx.Raw.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{
			Peer: userPeer(ctx, chatID), ID: u.CallbackQuery.MsgID, Message: text, Entities: ents, ReplyMarkup: markup,
		})
	}
	return dispatcher.EndGroups
}
