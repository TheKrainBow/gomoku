package main

import (
	"os"
	"strings"
)

func currentBotHash() string {
	data, err := os.ReadFile("BOT_HASH")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
