package commands

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/database"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto/ext"
	"github.com/dustin/go-humanize"
	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
)

// guard runs the ban / allow-list checks every command shares.
// Returns false (after replying) if the user may not continue.
func guard(ctx *ext.Context, u *ext.Update, chatID int64) bool {
	if database.IsEnabled() && database.GetDB().IsUserBanned(context.Background(), chatID) {
		ctx.Reply(u, ext.ReplyTextStyledTextArray([]styling.StyledTextOption{
			styling.Italic("Sᴏʀʀʏ Sɪʀ, Yᴏᴜ ᴀʀᴇ Bᴀɴɴᴇᴅ ᴛᴏ ᴜsᴇ ᴍᴇ.\n\n"),
			styling.TextURL("Cᴏɴᴛᴀᴄᴛ Dᴇᴠᴇʟᴏᴘᴇʀ", getUpdatesURL()),
			styling.Bold(" Tʜᴇʏ Wɪʟʟ Hᴇʟᴘ Yᴏᴜ"),
		}), nil)
		return false
	}
	if len(config.ValueOf.AllowedUsers) != 0 && !utils.Contains(config.ValueOf.AllowedUsers, chatID) {
		ctx.Reply(u, ext.ReplyTextString("Yᴏᴜ ᴀʀᴇ ɴᴏᴛ ᴀᴜᴛʜᴏʀɪᴢᴇᴅ ᴛᴏ ᴜsᴇ ᴛʜɪs ʙᴏᴛ."), nil)
		return false
	}
	return true
}

// styledToRaw converts styling options into raw text + entities (for edits / sends).
func StyledToRaw(parts []styling.StyledTextOption) (string, []tg.MessageEntityClass) {
	tb := entity.Builder{}
	_ = styling.Perform(&tb, parts...)
	return tb.Complete()
}

func isMediaLink(mimeType string) bool { return utils.IsStreamable(mimeType) }

// expiryLine is the line shown under the links.
func expiryLine(l *database.FileLink) string {
	if l.ExpiresAt == nil {
		return "♾ Nᴇᴠᴇʀ ᴇxᴘɪʀᴇs (sᴛᴏʀᴇᴅ ɪɴ ʏᴏᴜʀ ᴏᴡɴ ᴄʜᴀɴɴᴇʟ)"
	}
	return fmt.Sprintf("⏳ Exᴘɪʀᴇs ɪɴ %s (%s UTC)", utils.HumanLeft(*l.ExpiresAt), l.ExpiresAt.UTC().Format("02 Jan 15:04"))
}

// singleLinkParts is the body of the "your link is ready" message.
func singleLinkParts(botUsername string, l *database.FileLink) []styling.StyledTextOption {
	size := humanize.IBytes(uint64(l.FileSize))
	parts := []styling.StyledTextOption{
		styling.Bold("𝗬𝗼𝘂𝗿 𝗟𝗶𝗻𝗸 𝗚𝗲𝗻𝗲𝗿𝗮𝘁𝗲𝗱 !\n\n"),
		styling.Bold("📂 Fɪʟᴇ ɴᴀᴍᴇ : "), styling.Bold(l.FileName), styling.Plain("\n\n"),
		styling.Bold("📦 Fɪʟᴇ ꜱɪᴢᴇ : "), styling.Code(size), styling.Plain("\n\n"),
		styling.Bold("📥 Dᴏᴡɴʟᴏᴀᴅ : "), styling.Code(utils.DownloadURL(l.LinkID)), styling.Plain("\n\n"),
	}
	if isMediaLink(l.MimeType) {
		parts = append(parts, styling.Bold("🖥 Wᴀᴛᴄʜ : "), styling.Code(utils.StreamURL(l.LinkID)), styling.Plain("\n\n"))
	}
	parts = append(parts,
		styling.Bold("🔗 Sʜᴀʀᴇ : "), styling.Code(utils.ShareURL(botUsername, l.LinkID)), styling.Plain("\n\n"),
		styling.Italic(expiryLine(l)), styling.Plain("\n"),
		styling.Plain("Oᴘᴇɴ ᴛʜɪs ʟɪɴᴋ ᴏɴ Bʀᴏᴡsᴇʀ 🌐 ᴛᴏ ᴀᴠᴏɪᴅ ɪssᴜᴇs."),
	)
	return parts
}

func singleLinkMarkup(botUsername string, l *database.FileLink) *tg.ReplyInlineMarkup {
	var rows []tg.KeyboardButtonRow
	if strings.Contains(l.MimeType, "video") {
		rows = append(rows, tg.KeyboardButtonRow{Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonURL{Text: "sᴛʀᴇᴀᴍ", URL: utils.StreamURL(l.LinkID)},
			&tg.KeyboardButtonURL{Text: "ᴅᴏᴡɴʟᴏᴀᴅ", URL: utils.DownloadURL(l.LinkID)},
		}})
	} else {
		rows = append(rows, tg.KeyboardButtonRow{Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonURL{Text: "ᴅᴏᴡɴʟᴏᴀᴅ", URL: utils.DownloadURL(l.LinkID)},
		}})
	}
	rows = append(rows,
		tg.KeyboardButtonRow{Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonURL{Text: "ɢᴇᴛ ғɪʟᴇ", URL: utils.ShareURL(botUsername, l.LinkID)},
			&tg.KeyboardButtonCallback{Text: "ʀᴇᴠᴏᴋᴇ ғɪʟᴇ", Data: []byte("revoke_" + l.LinkID)},
		}},
		tg.KeyboardButtonRow{Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonCallback{Text: "ᴄʟᴏsᴇ", Data: []byte("close")},
		}},
	)
	return &tg.ReplyInlineMarkup{Rows: rows}
}

// ExpiredParts / ExpiredMarkup are what the SAME message is edited into when the
// file expires: names struck through, plus the (now "expired") link.
func ExpiredParts(links []database.FileLink) []styling.StyledTextOption {
	parts := []styling.StyledTextOption{
		styling.Bold("⌛ Lɪɴᴋ Exᴘɪʀᴇᴅ\n\n"),
	}
	for i, l := range links {
		if len(links) > 1 {
			parts = append(parts, styling.Plain(fmt.Sprintf("%d. ", i+1)))
		}
		parts = append(parts,
			styling.Strike(l.FileName), styling.Plain("\n"),
			styling.Bold("❌ Exᴘɪʀᴇᴅ ʟɪɴᴋ : "), styling.Strike(utils.StreamURL(l.LinkID)), styling.Plain("\n\n"),
		)
	}
	parts = append(parts,
		styling.Italic("The file was deleted from storage and these links no longer work. "+
			"Send the file again for a new link, or use /storage to keep files forever in your own channel."),
	)
	return parts
}

func ExpiredMarkup(links []database.FileLink) *tg.ReplyInlineMarkup {
	if len(links) == 0 {
		return &tg.ReplyInlineMarkup{}
	}
	return &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{{Buttons: []tg.KeyboardButtonClass{
		&tg.KeyboardButtonURL{Text: "⌛ ᴇxᴘɪʀᴇᴅ ʟɪɴᴋ", URL: utils.StreamURL(links[0].LinkID)},
	}}}}
}

func now() time.Time { return time.Now().UTC() }

func randID() int64 { return rand.Int63() }

// sentMessageID digs the new message ID out of a send result.
func sentMessageID(res tg.UpdatesClass) int {
	switch r := res.(type) {
	case *tg.UpdateShortSentMessage:
		return r.ID
	case *tg.Updates:
		for _, u := range r.Updates {
			if x, ok := u.(*tg.UpdateMessageID); ok {
				return x.ID
			}
		}
	}
	return 0
}

// userHash returns the cached access hash of a user (0 if unknown).
func userHash(ctx *ext.Context, userID int64) int64 {
	if p, ok := ctx.PeerStorage.GetInputPeerById(userID).(*tg.InputPeerUser); ok {
		return p.AccessHash
	}
	return 0
}

func userPeer(ctx *ext.Context, userID int64) tg.InputPeerClass {
	return &tg.InputPeerUser{UserID: userID, AccessHash: userHash(ctx, userID)}
}
