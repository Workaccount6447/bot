package commands

import (
	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/database"
	"context"
	"fmt"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/tg"
)

func isOwner(userID int64) bool {
	return config.ValueOf.OwnerID != 0 && userID == config.ValueOf.OwnerID
}

func ownerOnly(next func(*ext.Context, *ext.Update) error) func(*ext.Context, *ext.Update) error {
	return func(ctx *ext.Context, u *ext.Update) error {
		user := u.EffectiveUser()
		if user == nil || !isOwner(user.ID) {
			ctx.Reply(u, ext.ReplyTextString("You are not authorised to use this command."), nil)
			return dispatcher.EndGroups
		}
		return next(ctx, u)
	}
}

func requireDB(ctx *ext.Context, u *ext.Update) bool {
	if !database.IsEnabled() {
		ctx.Reply(u, ext.ReplyTextString("Database not configured. Set DATABASE_URL in your env file."), nil)
		return false
	}
	return true
}

func (m *command) LoadAdmin(d dispatcher.Dispatcher) {
	log := m.log.Named("admin")
	defer log.Sugar().Info("Loaded")
	d.AddHandler(handlers.NewCommand("ban", ownerOnly(m.banUser)))
	d.AddHandler(handlers.NewCommand("unban", ownerOnly(m.unbanUser)))
	d.AddHandler(handlers.NewCommand("status", ownerOnly(status)))
	d.AddHandler(handlers.NewCommand("broadcast", ownerOnly(m.broadcast)))
}

// resolveTargetID reads a user ID from either:
//  1. The command argument: /ban 12345
//  2. A reply to a message — fetches the replied message manually since
//     gotgproto does not pre-populate ReplyToMessage on command updates.
func (m *command) resolveTargetID(ctx *ext.Context, u *ext.Update) (targetID int64, display string, errMsg string) {
	parts := strings.Fields(u.EffectiveMessage.Text)
	if len(parts) >= 2 {
		id, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return 0, "", "Invalid user ID. Provide a numeric ID or reply to a message."
		}
		return id, fmt.Sprintf("%d", id), ""
	}

	// Check raw ReplyTo header and fetch the message manually
	rawMsg := u.EffectiveMessage.Message
	if rawMsg != nil {
		if replyHeader, ok := rawMsg.ReplyTo.(*tg.MessageReplyHeader); ok && replyHeader != nil {
			res, err := ctx.Raw.MessagesGetMessages(ctx, []tg.InputMessageClass{
				&tg.InputMessageID{ID: replyHeader.ReplyToMsgID},
			})
			if err == nil {
				if msgs, ok2 := res.(*tg.MessagesMessages); ok2 && len(msgs.Messages) > 0 {
					if msg, ok3 := msgs.Messages[0].(*tg.Message); ok3 {
						if peerUser, ok4 := msg.FromID.(*tg.PeerUser); ok4 {
							id := peerUser.UserID
							return id, fmt.Sprintf("%d", id), ""
						}
					}
				}
			}
		}
	}

	return 0, "", "Usage: /ban <user_id> or reply to a user's message with /ban"
}

func (m *command) banUser(ctx *ext.Context, u *ext.Update) error {
	if !requireDB(ctx, u) {
		return dispatcher.EndGroups
	}

	targetID, displayName, errMsg := m.resolveTargetID(ctx, u)
	if errMsg != "" {
		ctx.Reply(u, ext.ReplyTextString(errMsg), nil)
		return dispatcher.EndGroups
	}

	if isOwner(targetID) {
		ctx.Reply(u, ext.ReplyTextString("You cannot ban the owner."), nil)
		return dispatcher.EndGroups
	}

	db := database.GetDB()
	bgCtx := context.Background()
	err := db.BanUser(bgCtx, targetID)
	if err == database.ErrUserAlreadyBanned {
		ctx.Reply(u, ext.ReplyTextStyledTextArray([]styling.StyledTextOption{
			styling.Code(displayName), styling.Bold(" is Already Banned"),
		}), nil)
		return dispatcher.EndGroups
	}
	if err != nil {
		ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("something went wrong: %s", err.Error())), nil)
		return dispatcher.EndGroups
	}
	_ = db.DeleteUser(bgCtx, targetID)

	ctx.Raw.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: targetID},
		Message:  "You are banned from using this bot.",
		RandomID: rand.Int63(),
	})

	ctx.Reply(u, ext.ReplyTextStyledTextArray([]styling.StyledTextOption{
		styling.Code(displayName), styling.Bold(" is Banned"),
	}), nil)
	return dispatcher.EndGroups
}

func (m *command) unbanUser(ctx *ext.Context, u *ext.Update) error {
	if !requireDB(ctx, u) {
		return dispatcher.EndGroups
	}

	targetID, displayName, errMsg := m.resolveTargetID(ctx, u)
	if errMsg != "" {
		ctx.Reply(u, ext.ReplyTextString(errMsg), nil)
		return dispatcher.EndGroups
	}

	db := database.GetDB()
	bgCtx := context.Background()
	err := db.UnbanUser(bgCtx, targetID)
	if err == database.ErrUserNotBanned {
		ctx.Reply(u, ext.ReplyTextStyledTextArray([]styling.StyledTextOption{
			styling.Code(displayName), styling.Bold(" is not Banned"),
		}), nil)
		return dispatcher.EndGroups
	}
	if err != nil {
		ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("something went wrong: %s", err.Error())), nil)
		return dispatcher.EndGroups
	}

	ctx.Raw.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
		Peer:     &tg.InputPeerUser{UserID: targetID},
		Message:  "You are unbanned. You can use this bot now.",
		RandomID: rand.Int63(),
	})

	ctx.Reply(u, ext.ReplyTextStyledTextArray([]styling.StyledTextOption{
		styling.Code(displayName), styling.Bold(" is Unbanned"),
	}), nil)
	return dispatcher.EndGroups
}

func status(ctx *ext.Context, u *ext.Update) error {
	if !database.IsEnabled() {
		ctx.Reply(u, ext.ReplyTextString("Database not configured."), nil)
		return dispatcher.EndGroups
	}
	db := database.GetDB()
	bgCtx := context.Background()
	totalUsers, _ := db.TotalUsers(bgCtx)
	bannedUsers, _ := db.TotalBanned(bgCtx)
	totalLinks, _ := db.TotalLinks(bgCtx)

	parts := []styling.StyledTextOption{
		styling.Bold("Bot Status\n\n"),
		styling.Bold("Total Users : "), styling.Code(fmt.Sprintf("%d", totalUsers)), styling.Plain("\n"),
		styling.Bold("Banned Users : "), styling.Code(fmt.Sprintf("%d", bannedUsers)), styling.Plain("\n"),
		styling.Bold("Links Generated : "), styling.Code(fmt.Sprintf("%d", totalLinks)),
	}
	ctx.Reply(u, ext.ReplyTextStyledTextArray(parts), nil)
	return dispatcher.EndGroups
}

func (m *command) broadcast(ctx *ext.Context, u *ext.Update) error {
	if !requireDB(ctx, u) {
		return dispatcher.EndGroups
	}

	// gotgproto does not pre-populate ReplyToMessage on command updates.
	// Read the raw ReplyTo header and fetch the message manually.
	rawMsg := u.EffectiveMessage.Message
	if rawMsg == nil {
		ctx.Reply(u, ext.ReplyTextString("Reply to a message to broadcast it."), nil)
		return dispatcher.EndGroups
	}
	replyHeader, ok := rawMsg.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || replyHeader == nil {
		ctx.Reply(u, ext.ReplyTextString("Reply to a message to broadcast it."), nil)
		return dispatcher.EndGroups
	}

	res, err := ctx.Raw.MessagesGetMessages(ctx, []tg.InputMessageClass{
		&tg.InputMessageID{ID: replyHeader.ReplyToMsgID},
	})
	if err != nil {
		ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("Could not fetch replied message: %s", err.Error())), nil)
		return dispatcher.EndGroups
	}
	msgs, ok := res.(*tg.MessagesMessages)
	if !ok || len(msgs.Messages) == 0 {
		ctx.Reply(u, ext.ReplyTextString("Replied message not found."), nil)
		return dispatcher.EndGroups
	}
	replyMsg, ok := msgs.Messages[0].(*tg.Message)
	if !ok {
		ctx.Reply(u, ext.ReplyTextString("Could not read replied message."), nil)
		return dispatcher.EndGroups
	}

	db := database.GetDB()
	bgCtx := context.Background()
	totalUsers, _ := db.TotalUsers(bgCtx)

	ctx.Reply(u, ext.ReplyTextString("Broadcast started. You will be notified with a log when done."), nil)

	cursor, err := db.GetAllUsers(bgCtx)
	if err != nil {
		ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("DB error: %s", err.Error())), nil)
		return dispatcher.EndGroups
	}
	defer cursor.Close(bgCtx)

	start := time.Now()
	done, success, failed := 0, 0, 0
	var failedLog []string
	ownerID := u.EffectiveChat().GetID()

	for cursor.Next(bgCtx) {
		var user database.User
		if err := cursor.Decode(&user); err != nil {
			continue
		}

		var sendErr error
		if replyMsg.Media != nil {
			_, sendErr = ctx.Raw.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{
				RandomID: []int64{rand.Int63()},
				FromPeer: &tg.InputPeerUser{UserID: ownerID},
				ID:       []int{replyMsg.ID},
				ToPeer:   &tg.InputPeerUser{UserID: user.ID},
			})
		} else {
			_, sendErr = ctx.Raw.MessagesSendMessage(ctx, &tg.MessagesSendMessageRequest{
				Peer:     &tg.InputPeerUser{UserID: user.ID},
				Message:  replyMsg.Message,
				RandomID: rand.Int63(),
			})
		}

		if sendErr != nil {
			failed++
			failedLog = append(failedLog, fmt.Sprintf("%d: %s", user.ID, sendErr.Error()))
		} else {
			success++
		}
		done++
	}

	elapsed := time.Since(start)
	result := fmt.Sprintf(
		"Broadcast completed in %s\n\nTotal users: %d\nDone: %d | Success: %d | Failed: %d",
		elapsed.Round(time.Second).String(), totalUsers, done, success, failed,
	)
	if len(failedLog) > 0 {
		_ = os.WriteFile("broadcast.txt", []byte(strings.Join(failedLog, "\n")), 0644)
	}
	ctx.Reply(u, ext.ReplyTextString(result), nil)
	return dispatcher.EndGroups
}
