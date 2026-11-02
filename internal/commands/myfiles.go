package commands

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"

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

const myFilesPerPage = 1

func (m *command) LoadMyFiles(d dispatcher.Dispatcher) {
	log := m.log.Named("myfiles")
	defer log.Sugar().Info("Loaded")
	d.AddHandler(handlers.NewCommand("myfiles", m.myFiles))
}

func (m *command) myFiles(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	if !database.IsEnabled() {
		ctx.Reply(u, ext.ReplyTextString("❌ Database not configured."), nil)
		return dispatcher.EndGroups
	}
	if !guard(ctx, u, chatId) {
		return dispatcher.EndGroups
	}
	return m.sendFileCard(ctx, u, chatId, 0, false, 0)
}

// card is everything the /myfiles page needs, for both new and legacy records.
type card struct {
	name, mime              string
	size                    int64
	stream, download, share string
	expiry                  string
	msg                     *tg.Message // for the media attachment (may be nil)
}

func (m *command) buildCard(ctx *ext.Context, rec *database.FileLink) (*card, error) {
	bg := context.Background()
	botUsername := ctx.Self.Username

	if !rec.IsLegacy() {
		c := &card{
			name: rec.FileName, mime: rec.MimeType, size: rec.FileSize,
			stream: utils.StreamURL(rec.LinkID), download: utils.DownloadURL(rec.LinkID),
			share: utils.ShareURL(botUsername, rec.LinkID), expiry: expiryLine(rec),
		}
		if msg, err := utils.GetTGMessage(bg, ctx.Raw, ctx.PeerStorage, rec.ChannelID, rec.ChannelAccessHash, rec.MessageID); err == nil {
			c.msg = msg
		}
		return c, nil
	}

	// legacy record: file info must be fetched live from the owner channel
	msg, err := utils.GetTGMessage(bg, ctx.Raw, ctx.PeerStorage, config.ValueOf.LogChannelID, 0, rec.MessageID)
	if err != nil {
		return nil, err
	}
	file, err := utils.FileFromMedia(msg.Media)
	if err != nil {
		return nil, err
	}
	full := utils.PackFileLegacy(file.FileName, file.FileSize, file.MimeType, file.ID)
	stream := utils.LegacyStreamURL(rec.MessageID, full)
	return &card{
		name: utils.DisplayName(file), mime: file.MimeType, size: file.FileSize,
		stream: stream, download: stream + "&d=true",
		share:  fmt.Sprintf("https://t.me/%s?start=file_%d_%s", botUsername, rec.MessageID, utils.ShortHash(full)),
		expiry: "♾ Nᴇᴠᴇʀ ᴇxᴘɪʀᴇs", msg: msg,
	}, nil
}

func (m *command) sendFileCard(ctx *ext.Context, u *ext.Update, userID int64, page int, isEdit bool, msgEditID int) error {
	db := database.GetDB()
	records, total, err := db.GetUserFiles(context.Background(), userID, page, myFilesPerPage)
	if err != nil || total == 0 || len(records) == 0 {
		emptyParts := []styling.StyledTextOption{
			styling.Bold("Yᴏᴜ ʜᴀᴠᴇ ɴᴏ ꜰɪʟᴇs ʏᴇᴛ.\n\n"),
			styling.Plain("Send me any media to generate a stream link."),
		}
		if isEdit {
			text, ents := StyledToRaw(emptyParts)
			ctx.Raw.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{
				Peer: userPeer(ctx, userID), ID: msgEditID, Message: text, Entities: ents,
			})
		} else {
			ctx.Reply(u, ext.ReplyTextStyledTextArray(emptyParts), nil)
		}
		return dispatcher.EndGroups
	}

	totalPages := int(math.Ceil(float64(total) / float64(myFilesPerPage)))
	rec := records[0]
	c, err := m.buildCard(ctx, &rec)
	if err != nil {
		ctx.Reply(u, ext.ReplyTextString("❌ Could not read this file — it may have been removed from storage."), nil)
		return dispatcher.EndGroups
	}

	isVideo := strings.Contains(c.mime, "video")
	parts := buildCardParts(fileEmoji(c.mime, c.name), c.name, humanize.IBytes(uint64(c.size)),
		c.download, c.stream, c.share, c.expiry, utils.IsStreamable(c.mime), page+1, totalPages)

	var linkRow tg.KeyboardButtonRow
	if isVideo {
		linkRow = tg.KeyboardButtonRow{Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonURL{Text: "🖥 sᴛʀᴇᴀᴍ", URL: c.stream},
			&tg.KeyboardButtonURL{Text: "📥 ᴅᴏᴡɴʟᴏᴀᴅ", URL: c.download},
		}}
	} else {
		linkRow = tg.KeyboardButtonRow{Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonURL{Text: "📥 ᴅᴏᴡɴʟᴏᴀᴅ", URL: c.download},
			&tg.KeyboardButtonURL{Text: "🔗 sʜᴀʀᴇ", URL: c.share},
		}}
	}
	rows := []tg.KeyboardButtonRow{linkRow}
	if isVideo {
		rows = append(rows, tg.KeyboardButtonRow{Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonURL{Text: "🔗 sʜᴀʀᴇ ʟɪɴᴋ", URL: c.share},
		}})
	}
	var nav []tg.KeyboardButtonClass
	if page > 0 {
		nav = append(nav, &tg.KeyboardButtonCallback{Text: "◀ Pʀᴇᴠɪᴏᴜs", Data: []byte(fmt.Sprintf("mf_page_%d", page-1))})
	}
	if page < totalPages-1 {
		nav = append(nav, &tg.KeyboardButtonCallback{Text: "Nᴇxᴛ ▶", Data: []byte(fmt.Sprintf("mf_page_%d", page+1))})
	}
	if len(nav) > 0 {
		rows = append(rows, tg.KeyboardButtonRow{Buttons: nav})
	}
	if config.ValueOf.UpdatesChannel != "" {
		rows = append(rows, tg.KeyboardButtonRow{Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonURL{Text: "📢 ᴜᴘᴅᴀᴛᴇs ᴄʜᴀɴɴᴇʟ", URL: getUpdatesURL()},
		}})
	}
	markup := &tg.ReplyInlineMarkup{Rows: rows}

	if isEdit {
		text, ents := StyledToRaw(parts)
		ctx.Raw.MessagesEditMessage(ctx, &tg.MessagesEditMessageRequest{
			Peer: userPeer(ctx, userID), ID: msgEditID, Message: text, Entities: ents,
			ReplyMarkup: markup, NoWebpage: true,
		})
		return dispatcher.EndGroups
	}

	if c.msg != nil {
		if inputMedia := inputMediaFrom(c.msg); inputMedia != nil {
			text, ents := StyledToRaw(parts)
			_, sendErr := ctx.Raw.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
				Peer: userPeer(ctx, userID), Media: inputMedia, Message: text,
				Entities: ents, ReplyMarkup: markup, RandomID: randID(),
			})
			if sendErr == nil {
				return dispatcher.EndGroups
			}
		}
	}
	ctx.Reply(u, ext.ReplyTextStyledTextArray(parts), &ext.ReplyOpts{Markup: markup})
	return dispatcher.EndGroups
}

func buildCardParts(emoji, fileName, humanSize, downloadLink, streamLink, shareLink, expiry string, isMedia bool, page, totalPages int) []styling.StyledTextOption {
	parts := []styling.StyledTextOption{
		styling.Bold(fmt.Sprintf("%s Yᴏᴜʀ Fɪʟᴇ\n\n", emoji)),
		styling.Bold("📂 Nᴀᴍᴇ : "), styling.Bold(fileName), styling.Plain("\n\n"),
		styling.Bold("📦 Sɪᴢᴇ : "), styling.Code(humanSize), styling.Plain("\n\n"),
		styling.Bold("📥 Dᴏᴡɴʟᴏᴀᴅ :\n"), styling.Code(downloadLink), styling.Plain("\n\n"),
	}
	if isMedia {
		parts = append(parts, styling.Bold("🖥 Wᴀᴛᴄʜ/Sᴛʀᴇᴀᴍ :\n"), styling.Code(streamLink), styling.Plain("\n\n"))
	}
	parts = append(parts,
		styling.Bold("🔗 Sʜᴀʀᴇ :\n"), styling.Code(shareLink), styling.Plain("\n\n"),
		styling.Italic(expiry), styling.Plain("\n"),
		styling.Italic(fmt.Sprintf("Pᴀɢᴇ %d ᴏꜰ %d", page, totalPages)),
	)
	return parts
}

func fileEmoji(mimeType, fileName string) string {
	switch {
	case strings.Contains(mimeType, "video"):
		return "🎬"
	case strings.Contains(mimeType, "audio"):
		return "🎵"
	case strings.Contains(mimeType, "pdf"):
		return "📄"
	case strings.Contains(mimeType, "image"):
		return "🖼"
	case strings.Contains(mimeType, "zip"), strings.Contains(mimeType, "rar"),
		strings.Contains(mimeType, "tar"), strings.Contains(mimeType, "7z"):
		return "🗜"
	case strings.HasSuffix(strings.ToLower(fileName), ".apk"):
		return "📱"
	}
	return "📁"
}

func (m *command) myFilesPageCallback(ctx *ext.Context, u *ext.Update, pageStr string) error {
	page, err := strconv.Atoi(pageStr)
	if err != nil || page < 0 {
		ctx.AnswerCallback(&tg.MessagesSetBotCallbackAnswerRequest{QueryID: u.CallbackQuery.QueryID, Message: "Invalid page", Alert: true})
		return dispatcher.EndGroups
	}
	ctx.AnswerCallback(&tg.MessagesSetBotCallbackAnswerRequest{QueryID: u.CallbackQuery.QueryID})
	return m.sendFileCard(ctx, u, u.EffectiveChat().GetID(), page, true, u.CallbackQuery.MsgID)
}
