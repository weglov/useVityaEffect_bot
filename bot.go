package main

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/sashabaranov/go-openai"
)

type UserContext struct {
	Messages   []openai.ChatCompletionMessage
	LastUpdate time.Time
	mu         sync.RWMutex
}

type Bot struct {
	tg           *tgbotapi.BotAPI
	openai       *openai.Client
	config       Config
	userContexts map[int64]*UserContext
	contextMu    sync.RWMutex
}

func NewBot(config Config) (*Bot, error) {
	tg, err := tgbotapi.NewBotAPI(config.BotToken)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram bot: %w", err)
	}

	openaiClient := openai.NewClient(config.OpenAIAPIKey)

	bot := &Bot{
		tg:           tg,
		openai:       openaiClient,
		config:       config,
		userContexts: make(map[int64]*UserContext),
	}

	return bot, nil
}

func (b *Bot) checkChannelSubscription(userID int64, chatID int64) bool {
	// Временно отключаем проверку подписки
	return true

	if b.config.EnvMode == "development" {
		return true
	}

	if b.config.ChannelID == "" {
		return true
	}

	var channelID interface{}
	if parsedID, err := strconv.ParseInt(b.config.ChannelID, 10, 64); err == nil {
		channelID = parsedID
	} else {
		channelID = b.config.ChannelID
	}

	member, err := b.tg.GetChatMember(tgbotapi.GetChatMemberConfig{
		ChatConfigWithUser: tgbotapi.ChatConfigWithUser{
			ChatID: channelID,
			UserID: userID,
		},
	})
	if err != nil {
		log.Printf("Error getting chat member: %v", err)
		return false
	}

	isSubscribed := member.Status != "left" && member.Status != "kicked" && member.Status != "banned"

	if !isSubscribed {
		msg := tgbotapi.NewMessage(chatID, "Please subscribe to our channel @useVityaEffect to use the bot in full functionality.")
		b.tg.Send(msg)
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

func (b *Bot) Start(ctx context.Context) error {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := b.tg.GetUpdatesChan(u)

	commands := []tgbotapi.BotCommand{
		{Command: "start", Description: "Start the bot and get welcome message"},
		{Command: "new", Description: "Start new conversation (clear context)"},
		{Command: "help", Description: "Get help and support information"},
	}

	if _, err := b.tg.Request(tgbotapi.NewSetMyCommands(commands...)); err != nil {
		log.Printf("Error setting bot commands: %v", err)
	} else {
		log.Println("Bot commands have been set successfully")
	}

	go b.cleanOldContexts(ctx)

	log.Println("Bot started successfully!")

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case update := <-updates:
			if update.Message == nil {
				continue
			}

			switch {
			case update.Message.IsCommand():
				switch update.Message.Command() {
				case "start":
					go b.handleStart(update)
				case "new":
					go b.handleNew(update)
				case "help":
					go b.handleHelp(update)
				}
			default:
				go b.handleMessage(update)
			}
		}
	}
}