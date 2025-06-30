package main

import (
	"fmt"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	BotToken     string
	OpenAIAPIKey string
	ChannelID    string
	SupportBot   string
	GPTModel     string
	EnvMode      string
}

func LoadConfig() (Config, error) {
	_ = godotenv.Load()

	config := Config{
		BotToken:     os.Getenv("BOT_TOKEN"),
		OpenAIAPIKey: os.Getenv("OPENAI_API_KEY"),
		ChannelID:    getEnv("CHANNEL_ID", ""),
		SupportBot:   getEnv("SUPPORT_BOT", "@useVityaEffect"),
		GPTModel:     getEnv("GPT_MODEL", "gpt-4o"),
		EnvMode:      getEnv("ENV_MODE", "production"),
	}

	if config.BotToken == "" || config.OpenAIAPIKey == "" {
		return config, fmt.Errorf("BOT_TOKEN and OPENAI_API_KEY are required")
	}

	return config, nil
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}