package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/auth"
	"github.com/jahrulnr/nusamation/internal/config"
)

// whatsappGroups lists the joined WhatsApp groups (JID + name) through the
// running server, so the operator has the JIDs the allowlist needs.
func whatsappGroups(args []string) error {
	fs, dir := subFlagSet("whatsapp groups", "usage: nusamation whatsapp groups [--addr HOST:PORT] [--data-dir DIR]\n")
	defaultAddr := os.Getenv("NUSAMATION_ADDR")
	if defaultAddr == "" {
		defaultAddr = config.DefaultAddr
	}
	addr := fs.String("addr", defaultAddr, "running server address")
	if _, err := parseInterleaved(fs, args); err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(*addr)
	if err != nil {
		return fmt.Errorf("invalid server address: %w", err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	st, err := openStore(*dir)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	token, _, err := auth.CreateSession(ctx, st, "127.0.0.1", "nusamation-cli", 2*time.Minute)
	if err != nil {
		return err
	}
	defer auth.DeleteSession(context.Background(), st, token)
	u := url.URL{Scheme: "http", Host: net.JoinHostPort(strings.Trim(host, "[]"), port), Path: "/api/channels/whatsapp/groups"}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	req.AddCookie(&http.Cookie{Name: "nm_session", Value: token})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("contact running NusaMation: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("WhatsApp group listing: %s", strings.TrimSpace(string(body)))
	}
	fmt.Print(string(body))
	return nil
}
