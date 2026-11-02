package bot

import (
	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/commands"
	"context"
	"time"

	"github.com/celestix/gotgproto"
	"github.com/celestix/gotgproto/sessionMaker"
	"github.com/gotd/td/tg"
	"go.uber.org/zap"
)

var Bot *gotgproto.Client

func StartClient(log *zap.Logger) (*gotgproto.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	type result struct {
		client *gotgproto.Client
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		client, err := gotgproto.NewClient(
			int(config.ValueOf.ApiID),
			config.ValueOf.ApiHash,
			gotgproto.ClientTypeBot(config.ValueOf.BotToken),
			&gotgproto.ClientOpts{Session: sessionMaker.SimpleSession(), DisableCopyright: true},
		)
		ch <- result{client, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		if r.err != nil {
			return nil, r.err
		}
		commands.Load(log, r.client.Dispatcher, r.client)
		log.Info("Client started", zap.String("username", r.client.Self.Username))
		Bot = r.client
		go setBotCommands(r.client)
		return r.client, nil
	}
}

func setBotCommands(client *gotgproto.Client) {
	ctx := client.CreateContext()
	ctx.Raw.BotsSetBotCommands(ctx, &tg.BotsSetBotCommandsRequest{
		Scope:    &tg.BotCommandScopeDefault{},
		LangCode: "",
		Commands: []tg.BotCommand{
			{Command: "start", Description: "Start the bot"},
			{Command: "multi", Description: "Many files → all links in one message"},
			{Command: "done", Description: "Finish multi-link mode and get the links"},
			{Command: "storage", Description: "Choose bot storage or your own channel"},
			{Command: "setchannel", Description: "Connect your own channel (links never expire)"},
			{Command: "myfiles", Description: "Browse your uploaded files with stream links"},
			{Command: "stats", Description: "View your personal usage statistics"},
			{Command: "clearfiles", Description: "Delete all your files from history"},
		},
	})
}
