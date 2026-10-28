// Command speech serves SpeechService. It forwards work to HTTP speech engines.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/catalystcommunity/magi/lib/authn"
	"github.com/catalystcommunity/magi/lib/rpc"
	"github.com/catalystcommunity/magi/primitives/speech/server"
)

func env(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func main() {
	listen := flag.String("listen", env("MAGI_LISTEN", ":8080"), "listen address")
	keys := flag.String("auth-keys", env("MAGI_AUTH_KEYS", ""), "comma-separated issuer key files")
	stt := flag.String("stt-url", env("MAGI_SPEECH_STT_URL", ""), "speech-to-text engine URL")
	tts := flag.String("tts-url", env("MAGI_SPEECH_TTS_URL", ""), "text-to-speech engine URL")
	tokenFile := flag.String("engine-token-file", env("MAGI_SPEECH_ENGINE_TOKEN_FILE", ""), "engine bearer token file")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(*listen, *keys, *stt, *tts, *tokenFile, log); err != nil {
		log.Error("speech", "err", err)
		os.Exit(1)
	}
}

func run(listen, keys, stt, tts, tokenFile string, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if keys == "" || stt == "" || tts == "" || tokenFile == "" {
		return errors.New("MAGI_AUTH_KEYS, MAGI_SPEECH_STT_URL, MAGI_SPEECH_TTS_URL, and MAGI_SPEECH_ENGINE_TOKEN_FILE are required")
	}
	v, err := authn.LoadVerifier(strings.Split(keys, ",")...)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(tokenFile)
	if err != nil {
		return err
	}
	token := strings.TrimSpace(string(b))
	svc := &server.Service{STT: server.NewEngine(stt, token), TTS: server.NewEngine(tts, token)}
	s := &rpc.Server{Verifier: v, MaxBody: server.MaxRequest, Log: log}
	server.Register(s, svc)
	log.Info("speech listening", "addr", listen, "stt", stt, "tts", tts)
	return rpc.ListenAndServe(ctx, listen, rpc.Mux(s, func() error { return svc.Ready(ctx) }))
}
