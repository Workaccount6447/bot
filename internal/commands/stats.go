package commands

import (
	"context"
	"fmt"

	"EverythingSuckz/fsb/internal/database"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
)

func (m *command) LoadStats(d dispatcher.Dispatcher) {
	log := m.log.Named("stats")
	defer log.Sugar().Info("Loaded")
	d.AddHandler(handlers.NewCommand("stats", m.userStats))
}

func (m *command) userStats(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()

	if !database.IsEnabled() {
		ctx.Reply(u, ext.ReplyTextString("❌ Database not configured."), nil)
		return dispatcher.EndGroups
	}

	if database.GetDB().IsUserBanned(context.Background(), chatId) {
		ctx.Reply(u, ext.ReplyTextStyledTextArray([]styling.StyledTextOption{
			styling.Italic("Sᴏʀʀʏ Sɪʀ, Yᴏᴜ ᴀʀᴇ Bᴀɴɴᴇᴅ ᴛᴏ ᴜsᴇ ᴍᴇ.\n\n"),
			styling.TextURL("Cᴏɴᴛᴀᴄᴛ Dᴇᴠᴇʟᴏᴘᴇʀ", getUpdatesURL()),
			styling.Bold(" Tʜᴇʏ Wɪʟʟ Hᴇʟᴘ Yᴏᴜ"),
		}), nil)
		return dispatcher.EndGroups
	}

	loading, _ := ctx.Reply(u, ext.ReplyTextStyledTextArray([]styling.StyledTextOption{
		styling.Italic("Fᴇᴛᴄʜɪɴɢ ʏᴏᴜʀ sᴛᴀᴛs..."),
	}), nil)

	stats, err := database.GetDB().GetUserStats(context.Background(), chatId)
	if err != nil || stats == nil {
		ctx.Reply(u, ext.ReplyTextString("❌ Could not fetch your stats. Try again later."), nil)
		return dispatcher.EndGroups
	}

	if loading != nil {
		ctx.Raw.MessagesDeleteMessages(ctx, &tg.MessagesDeleteMessagesRequest{
			Revoke: true, ID: []int{loading.ID},
		})
	}

	parts := []styling.StyledTextOption{
		styling.Bold("📊 Yᴏᴜʀ Sᴛᴀᴛs\n\n"),
		styling.Bold("📁 Fɪʟᴇs\n"),
		styling.Plain("  ⬩ Tᴏᴛᴀʟ ᴜᴘʟᴏᴀᴅᴇᴅ : "), styling.Code(fmt.Sprintf("%d", stats.TotalFiles)), styling.Plain("\n"),
		styling.Plain("  ⬩ Lɪɴᴋs ɢᴇɴᴇʀᴀᴛᴇᴅ : "), styling.Code(fmt.Sprintf("%d", stats.LinksCount)),
	}

	markup := &tg.ReplyInlineMarkup{
		Rows: []tg.KeyboardButtonRow{
			{Buttons: []tg.KeyboardButtonClass{
				&tg.KeyboardButtonCallback{Text: "📂 Mʏ Fɪʟᴇs", Data: []byte("goto_myfiles")},
				&tg.KeyboardButtonCallback{Text: "ᴄʟᴏsᴇ", Data: []byte("close")},
			}},
		},
	}

	ctx.Reply(u, ext.ReplyTextStyledTextArray(parts), &ext.ReplyOpts{Markup: markup})
	return dispatcher.EndGroups
}
