package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/sashabaranov/go-openai"
)

func (b *Bot) handleStart(update tgbotapi.Update) {
	userID := update.Message.From.ID
	log.Printf("Start command received from user %d", userID)

	if !b.checkChannelSubscription(userID, update.Message.Chat.ID) {
		return
	}

	b.logActivity(userID, update.Message.From.UserName, update.Message.From.FirstName, "bot_start")

	text := fmt.Sprintf("Hi! I'm ChatGPT bot implemented for @useVityaEffect subscribers 🤖\n🎤 You can send Voice Messages instead of text\n🦄 Current model: %s", b.config.GPTModel)
	msg := tgbotapi.NewMessage(update.Message.Chat.ID, text)
	msg.ParseMode = "Markdown"
	b.tg.Send(msg)
}

func (b *Bot) handleNew(update tgbotapi.Update) {
	userID := update.Message.From.ID
	log.Printf("New command received from user %d", userID)

	if !b.checkChannelSubscription(userID, update.Message.Chat.ID) {
		return
	}

	userContext := b.getUserContext(userID)
	userContext.mu.Lock()
	userContext.Messages = make([]openai.ChatCompletionMessage, 0)
	userContext.LastUpdate = time.Now()
	userContext.mu.Unlock()

	b.logActivity(userID, update.Message.From.UserName, update.Message.From.FirstName, "new_conversation")

	msg := tgbotapi.NewMessage(update.Message.Chat.ID, "🆕 Starting new dialog ✅")
	msg.ParseMode = "Markdown"
	b.tg.Send(msg)
}

func (b *Bot) handleHelp(update tgbotapi.Update) {
	userID := update.Message.From.ID
	log.Printf("Help command received from user %d", userID)

	if !b.checkChannelSubscription(userID, update.Message.Chat.ID) {
		return
	}

	b.logActivity(userID, update.Message.From.UserName, update.Message.From.FirstName, "help_command")

	text := fmt.Sprintf("🔧 *Need help or found a bug?*\n\nIf something isn't working properly or you have questions, feel free to contact our support: %s\n\nWe'll be happy to help! 🤝", b.config.SupportBot)
	msg := tgbotapi.NewMessage(update.Message.Chat.ID, text)
	msg.ParseMode = "Markdown"
	b.tg.Send(msg)
}

func (b *Bot) handleMessage(update tgbotapi.Update) {
	userID := update.Message.From.ID
	log.Printf("Received message from user %d", userID)

	if !b.checkChannelSubscription(userID, update.Message.Chat.ID) {
		return
	}

	var userText string

	if update.Message.Voice != nil || update.Message.VideoNote != nil {
		log.Printf("Processing voice/video message from user %d", userID)

		var fileID string
		if update.Message.Voice != nil {
			fileID = update.Message.Voice.FileID
		} else {
			fileID = update.Message.VideoNote.FileID
		}

		audioPath, err := b.downloadVoiceMessage(fileID)
		if err != nil {
			log.Printf("Error downloading voice message: %v", err)
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Sorry, I couldn't process your voice message.")
			b.tg.Send(msg)
			return
		}

		userText, err = b.transcribeAudio(audioPath)
		if err != nil {
			log.Printf("Error transcribing audio: %v", err)
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Sorry, I couldn't process your voice message.")
			b.tg.Send(msg)
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

	// Показываем typing
	action := tgbotapi.NewChatAction(update.Message.Chat.ID, tgbotapi.ChatTyping)
	b.tg.Send(action)

	log.Printf("Starting OpenAI streaming request for user %d", userID)

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
		log.Printf("Error creating OpenAI completion stream: %v", err)
		msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Извините, произошла ошибка. Попробуйте позже.")
		b.tg.Send(msg)
		return
	}
	defer stream.Close()

	// Отправляем первое пустое сообщение для последующего редактирования
	msg := tgbotapi.NewMessage(update.Message.Chat.ID, "...")
	sentMsg, err := b.tg.Send(msg)
	if err != nil {
		log.Printf("Error sending initial message: %v", err)
		return
	}

	var responseText string
	var lastEditTime time.Time
	const editThrottleInterval = 500 * time.Millisecond

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

			// Редактируем сообщение с ограничением частоты
			if time.Since(lastEditTime) > editThrottleInterval {
				editMsg := tgbotapi.NewEditMessageText(
					update.Message.Chat.ID,
					sentMsg.MessageID,
					responseText,
				)
				editMsg.ParseMode = "Markdown"
				_, editErr := b.tg.Send(editMsg)
				if editErr != nil {
					// Если не получилось с Markdown, пробуем без форматирования
					editMsg.ParseMode = ""
					b.tg.Send(editMsg)
				}
				lastEditTime = time.Now()
			}
		}
	}

	// Финальное редактирование с полным текстом
	if responseText != "" {
		editMsg := tgbotapi.NewEditMessageText(
			update.Message.Chat.ID,
			sentMsg.MessageID,
			responseText,
		)
		editMsg.ParseMode = "Markdown"
		_, err = b.tg.Send(editMsg)
		if err != nil {
			editMsg.ParseMode = ""
			b.tg.Send(editMsg)
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

	b.logActivity(userID, update.Message.From.UserName, update.Message.From.FirstName,
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
