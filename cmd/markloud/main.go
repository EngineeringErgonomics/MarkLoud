package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/markloud/markloud/internal/config"
	"github.com/markloud/markloud/internal/ui"
)

// Overridden at build time by GoReleaser via -ldflags.
var (
	version = "dev"
	commit  = "none"
	date    = ""
)

func main() {
	_ = godotenv.Load()

	// Load persisted config for defaults
	cfg, _ := config.Load()

	inputDir := flag.String("i", "", "Input directory containing markdown files")
	outputDir := flag.String("o", "", "Output directory for audio files")
	voice := flag.String("voice", getenv("OPENAI_TTS_VOICE", cfg.Voice), "TTS voice (alloy, echo, fable, onyx, nova, shimmer)")
	overwrite := flag.Bool("overwrite", false, "Overwrite existing audio files")
	showVersion := flag.Bool("version", false, "Print version and exit")
	useDefaults := flag.Bool("defaults", false, "Skip prompts and use config file values")
	flag.Parse()

	if *showVersion {
		fmt.Printf("markloud %s (commit %s, built %s)\n", version, commit, date)
		return
	}

	var opts *ui.CLIOptions
	if *inputDir != "" || *useDefaults {
		if *outputDir == "" {
			if *inputDir != "" {
				*outputDir = "./audio_out"
			} else {
				*outputDir = cfg.OutputDir
			}
		}
		if *inputDir == "" {
			*inputDir = cfg.InputDir
		}
		opts = &ui.CLIOptions{
			InputDir:  *inputDir,
			OutputDir: *outputDir,
			Voice:     *voice,
			Overwrite: *overwrite,
		}
	}

	v := ui.VersionInfo{Version: version, Commit: commit, Date: date}
	if err := ui.Run(opts, v); err != nil {
		fmt.Println("error:", err)
		os.Exit(1)
	}
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
