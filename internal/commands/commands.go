package commands

import (
	"reflect"
	"strings"

	"github.com/celestix/gotgproto"
	"github.com/celestix/gotgproto/dispatcher"
	"go.uber.org/zap"
)

type command struct {
	log    *zap.Logger
	client *gotgproto.Client
}

func Load(log *zap.Logger, d dispatcher.Dispatcher, client *gotgproto.Client) {
	log = log.Named("commands")
	defer log.Info("Initialized all command handlers")
	cmd := &command{log: log, client: client}
	cmdType := reflect.TypeOf(cmd)
	cmdValue := reflect.ValueOf(cmd)
	dispatcherValue := reflect.ValueOf(d)

	for i := 0; i < cmdType.NumMethod(); i++ {
		method := cmdType.Method(i)

		// Only call methods whose name starts with "Load" — all others are
		// helper/callback methods with different signatures that must not be called here.
		if !strings.HasPrefix(method.Name, "Load") {
			continue
		}

		// Guard: method must take exactly 2 inputs (receiver + dispatcher arg).
		// This prevents calling any Load* method that happens to have a different signature.
		if method.Type.NumIn() != 2 {
			continue
		}

		// Safe call — we know the signature is (receiver, dispatcher.Dispatcher)
		method.Func.Call([]reflect.Value{cmdValue, dispatcherValue})
	}
}
