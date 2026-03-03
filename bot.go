package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/sashabaranov/go-openai"
)

type UserContext struct {
	Messages   []openai.ChatCompletionMessage
	LastUpdate time.Time
	mu         sync.RWMutex
}

type Bot struct {
	tg           *gotgbot.Bot
	openai       *openai.Client
	config       Config
	userContexts map[int64]*UserContext
	contextMu    sync.RWMutex
}

func NewBot(config Config) (*Bot, error) {
	tg, err := gotgbot.NewBot(config.BotToken, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram bot: %w", err)
	}

	// Configure OpenRouter client (OpenAI-compatible API)
	openaiConfig := openai.DefaultConfig(config.OpenRouterAPIKey)
	openaiConfig.BaseURL = "https://openrouter.ai/api/v1"
	openaiClient := openai.NewClientWithConfig(openaiConfig)

	bot := &Bot{
		tg:           tg,
		openai:       openaiClient,
		config:       config,
		userContexts: make(map[int64]*UserContext),
	}

	return bot, nil
}

func (b *Bot) checkChannelSubscription(userID int64, chatID int64) bool {
	if b.config.EnvMode == "development" {
		return true
	}

	if b.config.ChannelID == "" {
		return true
	}

	var status string

	if parsedID, parseErr := strconv.ParseInt(b.config.ChannelID, 10, 64); parseErr == nil {
		member, err := b.tg.GetChatMember(parsedID, userID, nil)
		if err != nil {
			log.Printf("Error getting chat member: %v", err)
			return false
		}
		status = member.GetStatus()
	} else {
		// For string channel IDs (usernames), use raw Request
		result, err := b.tg.Request("getChatMember", map[string]any{
			"chat_id": b.config.ChannelID,
			"user_id": userID,
		}, nil)
		if err != nil {
			log.Printf("Error getting chat member: %v", err)
			return false
		}
		var memberResp struct {
			Status string `json:"status"`
		}
		if jsonErr := json.Unmarshal(result, &memberResp); jsonErr != nil {
			log.Printf("Error parsing chat member response: %v", jsonErr)
			return false
		}
		status = memberResp.Status
	}

	isSubscribed := status != "left" && status != "kicked" && status != "banned"

	if !isSubscribed {
		b.tg.SendMessage(chatID, "Please subscribe to our channel @useVityaEffect to use the bot in full functionality.", nil)
	}

	return isSubscribed
}

func (b *Bot) getUserContext(userID int64) *UserContext {
	b.contextMu.Lock()
	defer b.contextMu.Unlock()

	context, exists := b.userContexts[userID]
	if !exists {
		log.Printf("Creating new context for user %d", userID)
		context = &UserContext{
			Messages:   make([]openai.ChatCompletionMessage, 0),
			LastUpdate: time.Now(),
		}
		b.userContexts[userID] = context
	} else {
		context.mu.Lock()
		if time.Since(context.LastUpdate) > 3*time.Minute {
			log.Printf("Resetting context for user %d due to inactivity", userID)
			context.Messages = make([]openai.ChatCompletionMessage, 0)
		}
		context.LastUpdate = time.Now()
		context.mu.Unlock()
	}

	return context
}

func (b *Bot) cleanOldContexts(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			b.contextMu.Lock()
			for userID, context := range b.userContexts {
				context.mu.RLock()
				if time.Since(context.LastUpdate) > 3*time.Minute {
					delete(b.userContexts, userID)
					log.Printf("Cleaned context for user %d", userID)
				}
				context.mu.RUnlock()
			}
			b.contextMu.Unlock()
		}
	}
}

func (b *Bot) logActivity(userID int64, username, firstName, event string) {
	log.Printf("User activity - ID: %d, Username: %s, FirstName: %s, Event: %s",
		userID, username, firstName, event)
}

// getCommand extracts the bot command from a message, e.g. "/start" -> "start".
// Returns empty string if the message is not a command.
func getCommand(msg *gotgbot.Message) string {
	if len(msg.Entities) == 0 {
		return ""
	}
	e := msg.Entities[0]
	if e.Type != "bot_command" || e.Offset != 0 {
		return ""
	}
	cmd := msg.Text[1:e.Length]
	// Strip @botname suffix
	if i := strings.Index(cmd, "@"); i != -1 {
		cmd = cmd[:i]
	}
	return cmd
}

func (b *Bot) Start(ctx context.Context) error {
	commands := []gotgbot.BotCommand{
		{Command: "start", Description: "Start the bot and get welcome message"},
		{Command: "new", Description: "Start new conversation (clear context)"},
		{Command: "help", Description: "Get help and support information"},
	}

	if _, err := b.tg.SetMyCommands(commands, nil); err != nil {
		log.Printf("Error setting bot commands: %v", err)
	} else {
		log.Println("Bot commands have been set successfully")
	}

	go b.cleanOldContexts(ctx)

	log.Println("Bot started successfully!")

	var offset int64
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		updates, err := b.tg.GetUpdatesWithContext(ctx, &gotgbot.GetUpdatesOpts{
			Offset:  offset,
			Timeout: 60,
			RequestOpts: &gotgbot.RequestOpts{
				Timeout: 65 * time.Second,
			},
		})
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			log.Printf("Error getting updates: %v", err)
			time.Sleep(3 * time.Second)
			continue
		}

		for _, update := range updates {
			offset = update.UpdateId + 1

			if update.Message == nil {
				continue
			}

			if cmd := getCommand(update.Message); cmd != "" {
				switch cmd {
				case "start":
					go b.handleStart(update)
				case "new":
					go b.handleNew(update)
				case "help":
					go b.handleHelp(update)
				}
			} else {
				go b.handleMessage(update)
			}
		}
	}
}
