package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/sashabaranov/go-openai"
)

func (b *Bot) downloadVoiceMessage(fileID string) (string, error) {
	log.Printf("Downloading voice message with file_id: %s", fileID)

	file, err := b.tg.GetFile(tgbotapi.FileConfig{FileID: fileID})
	if err != nil {
		return "", fmt.Errorf("error getting file: %w", err)
	}

	fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.config.BotToken, file.FilePath)
	resp, err := http.Get(fileURL)
	if err != nil {
		return "", fmt.Errorf("error downloading file: %w", err)
	}
	defer resp.Body.Close()

	fileBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("error reading file: %w", err)
	}

	tempFile, err := os.CreateTemp("", "voice_*.oga")
	if err != nil {
		return "", fmt.Errorf("error creating temp file: %w", err)
	}
	defer tempFile.Close()

	_, err = tempFile.Write(fileBytes)
	if err != nil {
		os.Remove(tempFile.Name())
		return "", fmt.Errorf("error writing to temp file: %w", err)
	}

	log.Printf("Voice message saved to temporary file: %s", tempFile.Name())
	return tempFile.Name(), nil
}

func (b *Bot) transcribeAudio(filePath string) (string, error) {
	log.Printf("Starting audio transcription for file: %s", filePath)
	defer os.Remove(filePath)

	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("error opening file: %w", err)
	}
	defer file.Close()

	req := openai.AudioRequest{
		Model:    openai.Whisper1,
		FilePath: filePath,
		Language: "ru",
	}

	resp, err := b.openai.CreateTranscription(context.Background(), req)
	if err != nil {
		return "", fmt.Errorf("error during audio transcription: %w", err)
	}

	log.Println("Audio transcription completed successfully")
	return resp.Text, nil
}

func (b *Bot) retryWithBackoff(fn func() error, maxRetries int) error {
	delay := time.Second

	for attempt := 0; attempt < maxRetries; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}

		if strings.Contains(err.Error(), "Flood control exceeded") || strings.Contains(err.Error(), "Too Many Requests") {
			if attempt == maxRetries-1 {
				return err
			}

			re := regexp.MustCompile(`retry after (\d+)`)
			matches := re.FindStringSubmatch(err.Error())
			if len(matches) > 1 {
				if retrySeconds, parseErr := strconv.Atoi(matches[1]); parseErr == nil {
					delay = time.Duration(retrySeconds) * time.Second
				}
			}

			log.Printf("Flood control hit, waiting %v before retry %d/%d", delay, attempt+1, maxRetries)
			time.Sleep(delay)
			delay *= 2
		} else {
			return err
		}
	}

	return fmt.Errorf("max retries exceeded")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}