import asyncio
import os
from datetime import datetime, timedelta
from typing import Dict, Optional
import tempfile
import logging
import re

from aiogram import Bot, Dispatcher, types
from aiogram.filters import Command
from dotenv import load_dotenv
# Removed MongoDB import
from openai import AsyncOpenAI
import posthog

load_dotenv()

# Настройка логгера
logging.basicConfig(
    level=logging.INFO,
    format='%(asctime)s - %(name)s - %(levelname)s - %(message)s',
    datefmt='%Y-%m-%d %H:%M:%S'
)
logger = logging.getLogger(__name__)

# Конфигурация
BOT_TOKEN = os.getenv("BOT_TOKEN")
OPENAI_API_KEY = os.getenv("OPENAI_API_KEY")
# MONGODB_URL no longer needed
CHANNEL_ID = os.getenv("CHANNEL_ID")
CONTEXT_TIMEOUT = 180 
SUPPORT_BOT = os.getenv("SUPPORT_BOT", "@useVityaEffect")

# Инициализация PostHog
posthog.api_key = os.getenv("POSTHOG_API_KEY")
if os.getenv("POSTHOG_HOST"):
    posthog.host = os.getenv("POSTHOG_HOST")

# Инициализация бота и диспетчера
bot = Bot(token=BOT_TOKEN)
dp = Dispatcher()

# Инициализация OpenAI
openai_client = AsyncOpenAI(api_key=OPENAI_API_KEY)

# In-memory storage only
# Хранение контекста пользователей в памяти
user_contexts: Dict[int, Dict] = {}

async def check_channel_subscription(user_id: int, message: types.Message = None) -> bool:
    if os.getenv("ENV_MODE") == "development":
        return True
        
    try:
        member = await bot.get_chat_member(chat_id=CHANNEL_ID, user_id=user_id)
        is_subscribed = member.status not in ["left", "kicked", "banned"]
        
        if not is_subscribed and message:
            logger.warning(f"User {user_id} not subscribed to channel")
            posthog.capture(
                str(user_id),
                "bot_start_without_subscription",
                properties={
                    "username": message.from_user.username,
                    "first_name": message.from_user.first_name,
                    "bot": "useGPTEffect"
                }
            )
            await message.answer("Please subscribe to our channel @useVityaEffect to use the bot in full functionality.")
        
        return is_subscribed
    except Exception:
        return False

async def clean_old_contexts():
    while True:
        current_time = datetime.now()
        to_remove = []
        for user_id, context in user_contexts.items():
            if (current_time - context["last_update"]) > timedelta(seconds=CONTEXT_TIMEOUT):
                to_remove.append(user_id)
        
        for user_id in to_remove:
            del user_contexts[user_id]
        
        await asyncio.sleep(60)  # Проверка каждую минуту

async def get_user_context(user_id: int) -> list:
    current_time = datetime.now()
    
    if user_id not in user_contexts:
        logger.info(f"Creating new context for user {user_id}")
        user_contexts[user_id] = {
            "messages": [],
            "last_update": current_time
        }
    else:
        # Проверяем время с последнего сообщения
        time_diff = current_time - user_contexts[user_id]["last_update"]
        # Если прошло больше 3 минут, сбрасываем контекст
        if time_diff > timedelta(minutes=3):
            logger.info(f"Resetting context for user {user_id} due to inactivity")
            user_contexts[user_id]["messages"] = []
    
    return user_contexts[user_id]["messages"]

async def download_voice_message(bot: Bot, file_id: str) -> str:
    logger.info(f"Downloading voice message with file_id: {file_id}")
    file = await bot.get_file(file_id)
    file_path = file.file_path
    
    # Создаем временный файл с расширением .oga
    with tempfile.NamedTemporaryFile(suffix='.oga', delete=False) as temp_file:
        temp_path = temp_file.name
    
    # Скачиваем файл
    await bot.download_file(file_path, temp_path)
    logger.info(f"Voice message saved to temporary file: {temp_path}")
    return temp_path

async def transcribe_audio(file_path: str) -> str:
    logger.info(f"Starting audio transcription for file: {file_path}")
    try:
        with open(file_path, 'rb') as audio_file:
            transcript = await openai_client.audio.transcriptions.create(
                model="whisper-1",
                file=audio_file,
                language="ru"
            )
        logger.info("Audio transcription completed successfully")
        return transcript.text
    except Exception as e:
        logger.error(f"Error during audio transcription: {e}")
        raise
    finally:
        # Удаляем временный файл
        os.unlink(file_path)
        logger.info(f"Temporary file deleted: {file_path}")

async def update_user_stats(user_id: int, username: str, first_name: str):
    # Just log user activity without storing in database
    logger.info(f"User activity: {user_id}, username: {username}, first_name: {first_name}")

async def retry_with_backoff(func, *args, max_retries=3, initial_delay=1, **kwargs):
    delay = initial_delay
    for attempt in range(max_retries):
        try:
            return await func(*args, **kwargs)
        except Exception as e:
            if "Flood control exceeded" in str(e) or "Too Many Requests" in str(e):
                if attempt == max_retries - 1:
                    raise
                
                # Extract retry time from error message
                retry_match = re.search(r"retry after (\d+)", str(e))
                if retry_match:
                    delay = int(retry_match.group(1))
                
                logger.warning(f"Flood control hit, waiting {delay} seconds before retry {attempt + 1}/{max_retries}")
                await asyncio.sleep(delay)
                delay *= 2  # Exponential backoff
            else:
                raise

@dp.message(Command("start"))
async def start_command(message: types.Message):
    user_id = message.from_user.id
    logger.info(f"Start command received from user {user_id}")
    
    if not await check_channel_subscription(user_id, message):
        return

    await update_user_stats(user_id, message.from_user.username, message.from_user.first_name)

    posthog.capture(
        str(user_id),
        "bot_start",
        properties={
            "username": message.from_user.username,
            "first_name": message.from_user.first_name,
            "bot": "useGPTEffect"
        }
    )
    
    await message.answer("Hi! I'm ChatGPT bot implemented for @useVityaEffect subscribers 🤖\n🎤 You can send Voice Messages instead of text\n🦄 Current model: gpt-4o", parse_mode='Markdown')

@dp.message(Command("new"))
async def new_command(message: types.Message):
    user_id = message.from_user.id
    logger.info(f"New command received from user {user_id}")
    
    if not await check_channel_subscription(user_id, message):
        return
    
    await update_user_stats(user_id, message.from_user.username, message.from_user.first_name)
    
    # Сбрасываем контекст пользователя
    if user_id in user_contexts:
        user_contexts[user_id]["messages"] = []
        user_contexts[user_id]["last_update"] = datetime.now()
        logger.info(f"Context reset for user {user_id}")
    
    posthog.capture(
        str(user_id),
        "new_conversation",
        properties={
            "username": message.from_user.username,
            "bot": "useGPTEffect"
        }
    )
    
    await message.answer("🆕 Starting new dialog ✅", parse_mode='Markdown')

@dp.message(Command("help"))
async def help_command(message: types.Message):
    user_id = message.from_user.id
    logger.info(f"Help command received from user {user_id}")
    
    if not await check_channel_subscription(user_id, message):
        return
    
    await update_user_stats(user_id, message.from_user.username, message.from_user.first_name)
    
    help_text = f"🔧 *Need help or found a bug?*\n\nIf something isn't working properly or you have questions, feel free to contact our support: {SUPPORT_BOT}\n\nWe'll be happy to help! 🤝"
    
    posthog.capture(
        str(user_id),
        "help_command",
        properties={
            "username": message.from_user.username,
            "bot": "useGPTEffect"
        }
    )
    
    await message.answer(help_text, parse_mode='Markdown')

@dp.message()
async def handle_message(message: types.Message):
    user_id = message.from_user.id
    logger.info(f"Received message from user {user_id}")
    
    if not await check_channel_subscription(user_id, message):
        return

    await update_user_stats(user_id, message.from_user.username, message.from_user.first_name)

    # Обработка голосовых сообщений и видео-кружков
    if message.voice or message.video_note:
        try:
            logger.info(f"Processing voice/video message from user {user_id}")
            # Отправляем статус о том, что обрабатываем голосовое сообщение            
            # Получаем file_id в зависимости от типа сообщения
            file_id = message.voice.file_id if message.voice else message.video_note.file_id
            
            # Скачиваем и транскрибируем
            audio_path = await download_voice_message(bot, file_id)
            user_text = await transcribe_audio(audio_path)
            logger.info(f"Voice message transcribed: {user_text[:50]}...")

        except Exception as e:
            logger.error(f"Error processing voice message: {e}")
            await message.answer("Sorry, I couldn't process your voice message.")
            print(f"Error processing voice message: {e}")
            return
    else:
        user_text = message.text

    context = await get_user_context(user_id)
    context.append({"role": "user", "content": user_text})
    user_contexts[user_id]["last_update"] = datetime.now()

    try:
        # Отправляем начальное сообщение
        bot_message = await message.answer("...")
        
        # Создаем задачу для отправки статуса typing
        typing_task = asyncio.create_task(send_typing(message.chat.id))
        
        logger.info(f"Starting OpenAI stream for user {user_id}")
        # Создаем стрим
        stream = await openai_client.chat.completions.create(
            model="gpt-4o-mini",
            temperature=0.7,
            max_tokens=2000,
            messages=context,
            stream=True
        )

        accumulated_message = ""
        buffer = ""  # Буфер для накопления частей сообщения
        streaming_enabled = True  # Флаг для управления стримингом
        
        async for chunk in stream:
            if chunk.choices[0].delta.content:
                buffer += chunk.choices[0].delta.content
                accumulated_message += chunk.choices[0].delta.content
                
                # Обновляем сообщение только если стриминг включен
                if streaming_enabled and (len(buffer) >= 100):
                    try:
                        await bot_message.edit_text(accumulated_message)
                        buffer = ""  # Очищаем буфер после обновления
                    except Exception as e:
                        if "Flood control exceeded" in str(e):
                            await message.answer(str(e))
                            logger.warning(f"Flood control exceeded for user {user_id}, disabling streaming")
                            streaming_enabled = False  # Отключаем стриминг при ошибке флуда
                        else:
                            logger.error(f"Error updating message: {e}")

        # Отправляем финальное сообщение с Markdown
        if accumulated_message:
            try:
                if not streaming_enabled:
                    # Если стриминг был отключен, добавляем задержку
                    await asyncio.sleep(30)
                    await retry_with_backoff(
                        message.answer,
                        accumulated_message,
                        parse_mode='Markdown'
                    )
                else:
                    await retry_with_backoff(
                        bot_message.edit_text,
                        accumulated_message,
                        parse_mode='Markdown'
                    )
            except Exception as e:
                logger.error(f"Error sending final message with Markdown: {e}")
                # Если не получилось отправить с Markdown, отправляем без форматирования
                try:
                    await retry_with_backoff(
                        bot_message.edit_text,
                        accumulated_message
                    )
                except Exception as e:
                    logger.error(f"Error sending final message without formatting: {e}")
                    # В крайнем случае, пробуем отправить новое сообщение
                    try:
                        await retry_with_backoff(
                            message.answer,
                            accumulated_message
                        )
                    except Exception as e:
                        logger.error(f"Failed to send message after all retries: {e}")

        # Отменяем отправку статуса typing
        typing_task.cancel()

        posthog.capture(
            str(user_id),
            "message_sent",
            properties={
                "bot": "useGPTEffect",
                "tokens": len(accumulated_message),
                "user_id": user_id,
                "message_length": len(user_text),
                "message_type": "voice" if (message.voice or message.video_note) else "text"
            }
        )

        logger.info(f"Completed message generation for user {user_id}")
        
        # Сохраняем контекст только в памяти
        context.append({"role": "assistant", "content": accumulated_message})
        logger.info(f"Conversation context updated in memory for user {user_id}")

    except Exception as e:
        logger.error(f"Error processing message: {e}", exc_info=True)
        posthog.capture(
            str(user_id),
            "error_occurred",
            properties={
                "error_type": type(e).__name__,
                "error_message": str(e),
                "bot": "useGPTEffect"
            }
        )
        await message.answer("Извините, произошла ошибка. Попробуйте позже.")
        print(f"Error: {e}")

async def send_typing(chat_id: int):
    while True:
        try:
            await bot.send_chat_action(chat_id=chat_id, action="typing")
            await asyncio.sleep(5)
        except Exception as e:
            logger.error(f"Error sending typing status: {e}")
            break

async def main():
    logger.info("Starting bot...")
    
    # Устанавливаем команды бота
    commands = [
        types.BotCommand(command="start", description="Start the bot and get welcome message"),
        types.BotCommand(command="new", description="Start new conversation (clear context)"),
        types.BotCommand(command="help", description="Get help and support information")
    ]
    
    try:
        await bot.set_my_commands(commands)
        logger.info("Bot commands have been set successfully")
    except Exception as e:
        logger.error(f"Error setting bot commands: {e}")
    
    asyncio.create_task(clean_old_contexts())
    await dp.start_polling(bot)

if __name__ == "__main__":
    asyncio.run(main())
