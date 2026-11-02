package main

import (
	"EverythingSuckz/fsb/config"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

const versionString = "4.0.0-bot"

var rootCmd = &cobra.Command{
	Use:               "fsb [command]",
	Short:             "Telegram File Stream Bot (bot / frontend only)",
	Long:              "The Telegram-facing half: commands, link generation, storage choice, expiry. Pair it with fsb-stream.",
	Version:           versionString,
	CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	Run:               func(cmd *cobra.Command, args []string) { cmd.Help() },
}

func init() {
	config.SetFlagsFromConfig(runCmd)
	rootCmd.AddCommand(runCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
