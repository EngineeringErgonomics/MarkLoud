# MarkLoud

Markdown → AAC in a Bubble Tea TUI powered by OpenAI’s tts-1-hd-1106 model. Requires Go 1.24+.

## Quick start

1. Set your OpenAI key (and optional defaults):
   ```bash
   export OPENAI_API_KEY=sk-...
   export OPENAI_TTS_VOICE=alloy            # optional, defaults to alloy
   export OPENAI_TTS_INSTRUCTIONS="Speak clearly for podcast listening."  # optional
   ```
2. Run the TUI:
   ```bash
   go run ./cmd/markloud
   # or
   go build -o markloud ./cmd/markloud && ./markloud
   ```

3. Check version metadata (set by GoReleaser builds):
   ```bash
   markloud --version
   ```

## CLI flags

- `-i` / `--input`: input directory containing markdown files
- `-o` / `--output`: output directory for audio (default `./audio_out`)
- `-voice`: OpenAI TTS voice name (default `alloy`)
- `-overwrite`: overwrite existing audio files

## How it works
- Recursively finds `*.md` files under the input directory.
- Strips light Markdown syntax, chunks text to ~6k characters, and streams each chunk to OpenAI TTS (`tts-1-hd-1106`) with `response_format=aac`.
- Writes `.aac` files that mirror the source tree inside your output directory.
- Idempotent by default: existing audio is skipped unless you toggle **Overwrite** (spacebar) in the TUI.
- Uses a worker pool (`num CPU cores - 2`, min 1) for parallel file conversion.
- Live UI shows parallel file progress bars and last error (if any) without dumping text content.
- Errors are also written to `logs/markloud_errors.log` in the current working directory for post-run inspection.

## Keys inside the TUI
- `tab` / `shift+tab` — move between inputs  
- `enter` — start conversion  
- `space` — toggle overwrite  
- `q` or `ctrl+c` — quit

## Notes
- The app uses `OPENAI_API_KEY` from your environment (or `.env` if present).
- Voice defaults to `alloy`, but you can type any supported voice name.
- Output uses AAC (user request said “ACC” — AAC is the correct response format for the OpenAI endpoint).
- See `.env.example` for environment variable scaffolding; do **not** commit your real key.

## Development

```bash
go install honnef.co/go/tools/cmd/staticcheck@latest
make fmt vet lint test
```

CI runs `fmtcheck`, `vet`, `staticcheck`, and `go test ./...` on every push/PR (see `.github/workflows/ci.yml`).

### Releases

We use [GoReleaser](https://goreleaser.com/) to ship tarballs/zip + checksums for linux/darwin/windows (amd64, arm64):

```bash
goreleaser release --clean --snapshot   # local smoke test
goreleaser release --clean              # real release (needs Git tag and repo access)
```
