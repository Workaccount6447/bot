package main

import (
	"EverythingSuckz/fsb/config"
	"EverythingSuckz/fsb/internal/bot"
	"EverythingSuckz/fsb/internal/database"
	"EverythingSuckz/fsb/internal/expiry"
	"EverythingSuckz/fsb/internal/utils"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the bot.",
	Run:   runApp,
}

var startTime = time.Now()

func runApp(cmd *cobra.Command, args []string) {
	utils.InitLogger(false)
	config.Load(utils.Logger, cmd)
	utils.InitLogger(config.ValueOf.Dev)
	log := utils.Logger
	main := log.Named("Main")

	if err := database.Init(config.ValueOf.DatabaseURL); err != nil {
		log.Sugar().Fatalf("MongoDB connection failed (DATABASE_URL is required): %v", err)
	}
	main.Info("MongoDB connected")

	client, err := bot.StartClient(log)
	if err != nil {
		log.Sugar().Fatalf("Failed to start bot: %v", err)
	}
	expiry.Start(client, log)

	// tiny health endpoint so PaaS hosts (Render/Heroku/Koyeb…) keep the process alive
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "bot": client.Self.Username, "version": versionString,
			"uptime": utils.TimeFormat(uint64(time.Since(startTime).Seconds())),
		})
	})
	mux.HandleFunc("/uptime", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	main.Sugar().Infof("Bot @%s running (health on :%d)", client.Self.Username, config.ValueOf.Port)
	if err := http.ListenAndServe(fmt.Sprintf(":%d", config.ValueOf.Port), mux); err != nil {
		log.Sugar().Fatalf("health server: %v", err)
	}
}
