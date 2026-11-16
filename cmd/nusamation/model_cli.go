package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jahrulnr/nusamation/internal/auth"
	"github.com/jahrulnr/nusamation/internal/chat"
	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/store"
)

// modelCmd reads or applies a conversation's model selector through the
// running server's HTTP API. It never writes the store directly: SetConfig's
// desiredConfig read-modify-write could be overwritten by the live server.
func modelCmd(args []string) error {
	fs, dataDir := subFlagSet("model", "usage: nusamation model [--data-dir DIR] [--addr HOST:PORT] [--json] <conversation-id> [selector]\n")
	defaultAddr := os.Getenv("NUSAMATION_ADDR")
	if defaultAddr == "" {
		defaultAddr = config.DefaultAddr
	}
	addr := fs.String("addr", defaultAddr, "running server address")
	jsonOut := fs.Bool("json", false, "print the raw API response")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		return usagef("expected <conversation-id> [selector]")
	}
	convID := fs.Arg(0)
	selector := ""
	if fs.NArg() == 2 {
		selector = fs.Arg(1)
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

	method, path, body := http.MethodGet, "/api/conversations/"+url.PathEscape(convID), []byte(nil)
	if selector != "" {
		method, path = http.MethodPost, path+"/config"
		if body, err = json.Marshal(map[string]string{"selector": selector}); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+net.JoinHostPort(strings.Trim(host, "[]"), port)+path, bytes.NewReader(body))
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
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var fail struct {
			Error      string              `json:"error"`
			Candidates []chat.ConfigChoice `json:"candidates"`
		}
		if json.Unmarshal(raw, &fail) == nil && fail.Error != "" {
			for _, c := range fail.Candidates {
				fmt.Fprintln(os.Stderr, c.Value)
			}
			return fmt.Errorf("%s", fail.Error)
		}
		return fmt.Errorf("model: %s", strings.TrimSpace(string(raw)))
	}
	if *jsonOut {
		fmt.Print(string(raw))
		return nil
	}
	var out struct {
		Conversation store.Conversation   `json:"conversation"`
		Configs      json.RawMessage      `json:"configs"`
		Result       *chat.SelectorResult `json:"result"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return fmt.Errorf("invalid response: %w", err)
	}
	if selector == "" {
		fmt.Println(chat.EchoSelector(out.Conversation, chat.ParseConfigOptions(out.Configs)))
		return nil
	}
	if res := out.Result; res != nil {
		fmt.Printf("Model set to %s.\n", res.Model)
		for _, m := range res.Applied {
			fmt.Printf("%s=%s applied.\n", m.Key, m.Value)
		}
		for _, f := range res.Failed {
			line := fmt.Sprintf("%s=%s %s", f.Modifier.Key, f.Modifier.Value, f.Reason)
			if len(f.Offers) > 0 {
				line += " — offers: " + strings.Join(f.Offers, ", ")
			}
			fmt.Println(line + ".")
		}
	}
	return nil
}
