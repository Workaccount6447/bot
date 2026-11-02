// Package expiry is the background cleaner for files stored in the OWNER channel.
//
// Every EXPIRY_CHECK_SEC seconds it:
//  1. finds links whose expires_at has passed,
//  2. EDITS the bot message that delivered those links (same message) into an
//     "expired" notice that also shows the expired link,
//  3. deletes the file (and its log caption) from the owner channel,
//  4. deletes the link records from the database.
//
// The stream service independently refuses expired links (and links whose record
// is gone) with an HTML "expired" page, so links stop working the moment
// expires_at passes — even if this cleaner is down for a while.
package expiry

import (
	"context"
	"fmt"
	"time"

	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/commands"
	"EverythingSuckz/fsb/internal/database"
	"EverythingSuckz/fsb/internal/utils"

	"github.com/celestix/gotgproto"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

const maxDeleteAttempts = 5

func Start(client *gotgproto.Client, log *zap.Logger) {
	log = log.Named("expiry")
	interval := time.Duration(config.ValueOf.ExpiryCheckSec) * time.Second
	log.Sugar().Infof("Cleaner started (every %s, retention %v)", interval, config.ValueOf.FileExpiry())
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		tick(client, log) // catch up immediately after a restart
		for range t.C {
			tick(client, log)
		}
	}()
}

type msgKey struct {
	chat int64
	msg  int
}

func tick(client *gotgproto.Client, log *zap.Logger) {
	defer func() {
		if r := recover(); r != nil {
			log.Error("cleaner panic", zap.Any("recover", r))
		}
	}()
	bg, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	db := database.GetDB()
	due, err := db.DueLinks(bg, time.Now().UTC(), 200)
	if err != nil {
		log.Error("query due links", zap.Error(err))
		return
	}
	if len(due) == 0 {
		return
	}
	ctx := client.CreateContext()

	// 2. edit the delivering message(s) once each
	done := map[msgKey]bool{}
	for _, l := range due {
		if l.NotifyMsgID == 0 || l.NotifyDone {
			continue
		}
		k := msgKey{l.NotifyChatID, l.NotifyMsgID}
		if done[k] {
			continue
		}
		done[k] = true
		group, err := db.LinksByNotify(bg, l.NotifyChatID, l.NotifyMsgID)
		if err != nil || len(group) == 0 {
			continue
		}
		text, ents := commands.StyledToRaw(commands.ExpiredParts(group))
		_, err = ctx.Raw.MessagesEditMessage(bg, &tg.MessagesEditMessageRequest{
			Peer:        &tg.InputPeerUser{UserID: l.NotifyChatID, AccessHash: l.NotifyHash},
			ID:          l.NotifyMsgID,
			Message:     text,
			Entities:    ents,
			ReplyMarkup: commands.ExpiredMarkup(group),
			NoWebpage:   true,
		})
		if err != nil {
			// user deleted the message / blocked the bot: nothing to edit, move on
			log.Debug("edit expired message", zap.Error(err))
		}
		db.MarkNotified(bg, l.NotifyChatID, l.NotifyMsgID)
	}

	// 3+4. delete stored files, then the records
	var removable []string
	for _, l := range due {
		ids := []int{l.MessageID}
		if l.LogMsgID != 0 {
			ids = append(ids, l.LogMsgID)
		}
		if l.Custom { // never touch a user's own channel
			removable = append(removable, l.LinkID)
			continue
		}
		err := utils.DeleteChannelMessages(bg, ctx.Raw, ctx.PeerStorage, l.ChannelID, l.ChannelAccessHash, ids)
		if err != nil && l.Attempts+1 < maxDeleteAttempts {
			log.Warn("could not delete expired file, will retry", zap.String("link", l.LinkID), zap.Error(err))
			db.BumpAttempts(bg, []string{l.LinkID})
			continue
		}
		if err != nil {
			log.Error(fmt.Sprintf("giving up deleting file after %d attempts (record removed anyway)", maxDeleteAttempts),
				zap.String("link", l.LinkID), zap.Error(err))
		}
		removable = append(removable, l.LinkID)
	}
	if len(removable) > 0 {
		if err := db.DeleteLinks(bg, removable); err != nil {
			log.Error("delete expired records", zap.Error(err))
			return
		}
		log.Sugar().Infof("Expired %d file(s)", len(removable))
	}
}
