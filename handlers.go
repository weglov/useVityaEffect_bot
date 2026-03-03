package main

import (
	"context"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"time"

	"github.com/PaulSonOfLars/gotgbot/v2"
	"github.com/sashabaranov/go-openai"
)

func (b *Bot) handleStart(update gotgbot.Update) {
	userID := update.Message.From.Id
	log.Printf("Start command received from user %d", userID)

	if !b.checkChannelSubscription(userID, update.Message.Chat.Id) {
		return
	}

	b.logActivity(userID, update.Message.From.Username, update.Message.From.FirstName, "bot_start")

	text := fmt.Sprintf("Hi! I'm ChatGPT bot implemented for @useVityaEffect subscribers 🤖\n🎤 You can send Voice Messages instead of text\n🦄 Current model: %s", b.config.GPTModel)
	b.tg.SendMessage(update.Message.Chat.Id, text, &gotgbot.SendMessageOpts{
		ParseMode: "Markdown",
	})
}

func (b *Bot) handleNew(update gotgbot.Update) {
	userID := update.Message.From.Id
	log.Printf("New command received from user %d", userID)

	if !b.checkChannelSubscription(userID, update.Message.Chat.Id) {
		return
	}

	userContext := b.getUserContext(userID)
	userContext.mu.Lock()
	userContext.Messages = make([]openai.ChatCompletionMessage, 0)
	userContext.LastUpdate = time.Now()
	userContext.mu.Unlock()

	b.logActivity(userID, update.Message.From.Username, update.Message.From.FirstName, "new_conversation")

	b.tg.SendMessage(update.Message.Chat.Id, "🆕 Starting new dialog ✅", &gotgbot.SendMessageOpts{
		ParseMode: "Markdown",
	})
}

func (b *Bot) handleHelp(update gotgbot.Update) {
	userID := update.Message.From.Id
	log.Printf("Help command received from user %d", userID)

	if !b.checkChannelSubscription(userID, update.Message.Chat.Id) {
		return
	}

	b.logActivity(userID, update.Message.From.Username, update.Message.From.FirstName, "help_command")

	text := fmt.Sprintf("🔧 *Need help or found a bug?*\n\nIf something isn't working properly or you have questions, feel free to contact our support: %s\n\nWe'll be happy to help! 🤝", b.config.SupportBot)
	b.tg.SendMessage(update.Message.Chat.Id, text, &gotgbot.SendMessageOpts{
		ParseMode: "Markdown",
	})
}

func (b *Bot) handleMessage(update gotgbot.Update) {
	userID := update.Message.From.Id
	log.Printf("Received message from user %d", userID)

	if !b.checkChannelSubscription(userID, update.Message.Chat.Id) {
		return
	}

	var userText string

	if update.Message.Voice != nil || update.Message.VideoNote != nil {
		log.Printf("Processing voice/video message from user %d", userID)

		b.tg.SendChatAction(update.Message.Chat.Id, "typing", nil)

		var fileID string
		if update.Message.Voice != nil {
			fileID = update.Message.Voice.FileId
		} else {
			fileID = update.Message.VideoNote.FileId
		}

		audioPath, err := b.downloadVoiceMessage(fileID)
		if err != nil {
			log.Printf("Error downloading voice message: %v", err)
			b.tg.SendMessage(update.Message.Chat.Id, "Sorry, I couldn't process your voice message.", nil)
			return
		}

		b.tg.SendChatAction(update.Message.Chat.Id, "typing", nil)

		userText, err = b.transcribeAudio(audioPath)
		if err != nil {
			log.Printf("Error transcribing audio: %v", err)
			b.tg.SendMessage(update.Message.Chat.Id, "Sorry, I couldn't process your voice message.", nil)
			return
		}

		log.Printf("Voice message transcribed: %s...", userText[:min(len(userText), 50)])
	} else {
		userText = update.Message.Text
	}

	userContext := b.getUserContext(userID)
	userContext.mu.Lock()
	userContext.Messages = append(userContext.Messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: userText,
	})
	userContext.LastUpdate = time.Now()
	userContext.mu.Unlock()

	b.tg.SendChatAction(update.Message.Chat.Id, "typing", nil)

	log.Printf("Starting OpenRouter streaming request for user %d", userID)

	const (
		defaultTemperature = 0.7
		maxTokensLimit     = 2000
	)

	req := openai.ChatCompletionRequest{
		Model:    b.config.GPTModel,
		Messages: userContext.Messages,
		Stream:   true,
	}

	if isReasoningModel(b.config.GPTModel) {
		req.MaxCompletionTokens = maxTokensLimit
	} else {
		req.MaxTokens = maxTokensLimit
		req.Temperature = defaultTemperature
	}

	stream, err := b.openai.CreateChatCompletionStream(context.Background(), req)
	if err != nil {
		log.Printf("Error creating OpenRouter completion stream: %v", err)
		b.tg.SendMessage(update.Message.Chat.Id, "Извините, произошла ошибка. Попробуйте позже.", nil)
		return
	}
	defer stream.Close()

	chatID := update.Message.Chat.Id
	draftID := int64(rand.Intn(999999) + 1)

	var responseText string
	var lastDraftTime time.Time
	const draftThrottleInterval = 300 * time.Millisecond

	for {
		response, err := stream.Recv()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			log.Printf("Error receiving stream: %v", err)
			break
		}

		if len(response.Choices) > 0 && response.Choices[0].Delta.Content != "" {
			responseText += response.Choices[0].Delta.Content

			if time.Since(lastDraftTime) > draftThrottleInterval {
				if _, draftErr := b.tg.SendMessageDraft(chatID, draftID, responseText, nil); draftErr != nil {
					log.Printf("Error sending message draft: %v", draftErr)
				}
				lastDraftTime = time.Now()
			}
		}
	}

	// Отправляем финальное сообщение (заменяет черновик)
	if responseText != "" {
		_, err = b.tg.SendMessage(chatID, responseText, &gotgbot.SendMessageOpts{
			ParseMode: "Markdown",
		})
		if err != nil {
			b.tg.SendMessage(chatID, responseText, nil)
		}
	}

	// Сохраняем в контекст
	userContext.mu.Lock()
	userContext.Messages = append(userContext.Messages, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleAssistant,
		Content: responseText,
	})
	userContext.mu.Unlock()

	messageType := "text"
	if update.Message.Voice != nil || update.Message.VideoNote != nil {
		messageType = "voice"
	}

	b.logActivity(userID, update.Message.From.Username, update.Message.From.FirstName,
		fmt.Sprintf("message_sent_%s", messageType))

	log.Printf("Completed message generation for user %d", userID)
}

func isReasoningModel(model string) bool {
	m := strings.ToLower(model)
	return strings.HasPrefix(m, "gpt-5") ||
		strings.HasPrefix(m, "o1") ||
		strings.HasPrefix(m, "o3") ||
		strings.HasPrefix(m, "o4")
}
