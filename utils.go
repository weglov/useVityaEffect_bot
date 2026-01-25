package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
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

	// Read and encode audio file to base64
	audioData, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("error reading audio file: %w", err)
	}
	audioBase64 := base64.StdEncoding.EncodeToString(audioData)

	// Prepare multimodal request for OpenRouter
	dataURL := "data:audio/ogg;base64," + audioBase64
	requestBody := map[string]interface{}{
		"model": b.config.TranscriptionModel,
		"messages": []map[string]interface{}{
			{
				"role": "user",
				"content": []map[string]interface{}{
					{
						"type": "text",
						"text": "Transcribe this audio to text. Output ONLY the transcribed text in the original language, nothing else.",
					},
					{
						"type": "image_url",
						"image_url": map[string]string{
							"url": dataURL,
						},
					},
				},
			},
		},
	}

	jsonBody, err := json.Marshal(requestBody)
	if err != nil {
		return "", fmt.Errorf("error marshaling request: %w", err)
	}

	req, err := http.NewRequest("POST", "https://openrouter.ai/api/v1/chat/completions", bytes.NewBuffer(jsonBody))
	if err != nil {
		return "", fmt.Errorf("error creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.config.OpenRouterAPIKey)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("error sending request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("error decoding response: %w", err)
	}

	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no transcription result")
	}

	transcription := strings.TrimSpace(result.Choices[0].Message.Content)
	log.Println("Audio transcription completed successfully")
	return transcription, nil
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