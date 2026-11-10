package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/auth"
	"github.com/jahrulnr/nusamation/internal/channels/whatsapp"
	"github.com/jahrulnr/nusamation/internal/config"
)

// whatsappSend uses the running server's existing WhatsApp connection; it
// never opens a second whatsmeow device against the same session database.
func whatsappSend(args []string) error {
	fs, dataDir := subFlagSet("whatsapp send", "usage: nusamation whatsapp send [--data-dir DIR] [--addr HOST:PORT] < request.json\n")
	defaultAddr := os.Getenv("NUSAMATION_ADDR")
	if defaultAddr == "" {
		defaultAddr = config.DefaultAddr
	}
	addr := fs.String("addr", defaultAddr, "running server address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return usagef("unexpected arguments")
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, 36<<20+1))
	if err != nil {
		return err
	}
	if len(raw) > 36<<20 {
		return fmt.Errorf("request is too large")
	}
	var payload whatsapp.OutboundRequest
	if err = json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("invalid request: %w", err)
	}
	host, port, err := net.SplitHostPort(*addr)
	if err != nil {
		return fmt.Errorf("invalid server address: %w", err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	st, err := openStore(*dataDir)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	token, session, err := auth.CreateSession(ctx, st, "127.0.0.1", "nusamation-cli", 2*time.Minute)
	if err != nil {
		return err
	}
	defer auth.DeleteSession(context.Background(), st, token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+net.JoinHostPort(strings.Trim(host, "[]"), port)+"/api/channels/whatsapp/send", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", session.CSRF)
	req.AddCookie(&http.Cookie{Name: "nm_session", Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("contact running NusaMation: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("WhatsApp send: %s", strings.TrimSpace(string(body)))
	}
	fmt.Print(string(body))
	return nil
}
