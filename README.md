# MarkLoud

Markdown → AAC in a Bubble Tea TUI powered by OpenAI’s tts-1-hd-1106 model.

## Quick start

1. Set your OpenAI key (and optional defaults):
   ```bash
   export OPENAI_API_KEY=sk-...
   export OPENAI_TTS_VOICE=alloy            # optional, defaults to alloy
   export OPENAI_TTS_INSTRUCTIONS="Speak clearly for podcast listening."  # optional
   ```
2. Run the TUI:
   ```bash
   go run .
   # or
   go build -o markloud && ./markloud
   ```

## How it works
- Recursively finds `*.md` files under the input directory.
- Strips light Markdown syntax, chunks text to ~6k characters, and streams each chunk to OpenAI TTS (`tts-1-hd-1106`) with `response_format=aac`.
- Writes `.aac` files that mirror the source tree inside your output directory.
- Idempotent by default: existing audio is skipped unless you toggle **Overwrite** (spacebar) in the TUI.

## Keys inside the TUI
- `tab` / `shift+tab` — move between inputs
- `enter` — start conversion
- `space` — toggle overwrite
- `q` or `ctrl+c` — quit

## Notes
- The app uses `OPENAI_API_KEY` from your environment (or `.env` if present).
- Voice defaults to `alloy`, but you can type any supported voice name.
- Output uses AAC (user request said “ACC” — AAC is the correct response format for the OpenAI endpoint).
