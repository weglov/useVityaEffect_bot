package main

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	BotToken           string
	OpenRouterAPIKey   string
	ChannelID          string
	SupportBot         string
	GPTModel           string
	TranscriptionModel string
	EnvMode            string
}

func LoadConfig() (Config, error) {
	_ = godotenv.Load()

	config := Config{
		BotToken:           os.Getenv("BOT_TOKEN"),
		OpenRouterAPIKey:   os.Getenv("OPENROUTER_API_KEY"),
		ChannelID:          getEnv("CHANNEL_ID", ""),
		SupportBot:         getEnv("SUPPORT_BOT", "@useVityaEffect"),
		GPTModel:           getEnv("GPT_MODEL", "openai/gpt-5-mini"),
		TranscriptionModel: getEnv("TRANSCRIPTION_MODEL", "google/gemini-3-flash-preview"),
		EnvMode:            getEnv("ENV_MODE", "production"),
	}

	if config.BotToken == "" || config.OpenRouterAPIKey == "" {
		return config, fmt.Errorf("BOT_TOKEN and OPENROUTER_API_KEY are required")
	}

	return config, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}