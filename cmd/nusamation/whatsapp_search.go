package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"

	"github.com/jahrulnr/nusamation/internal/auth"
	"github.com/jahrulnr/nusamation/internal/config"
	"github.com/jahrulnr/nusamation/internal/store"
)

const searchUsage = `usage: nusamation whatsapp search [flags] [text]
Search captured WhatsApp messages; text is a case-insensitive substring.
  -n N          Results per page, 1-500 (default 50)
  -cursor C     Continue from nextCursor in the previous result
  -chat JID     Restrict to a chat JID
  -sender JID   Restrict to a sender JID
  -raw          Include the stored WhatsApp message payload
  -data-dir DIR NusaMation data directory
`

func whatsappSearch(args []string) error {
	fs, dir := subFlagSet("whatsapp search", searchUsage)
	limit := fs.Int("n", 50, "page size")
	cursor := fs.String("cursor", "", "continuation cursor")
	chat := fs.String("chat", "", "chat JID")
	sender := fs.String("sender", "", "sender JID")
	raw := fs.Bool("raw", false, "include raw message")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usagef("search text must be a single quoted argument")
	}
	q := ""
	if len(pos) == 1 {
		q = pos[0]
	}
	st, err := openStore(*dir)
	if err != nil {
		return err
	}
	defer st.Close()
	page, err := st.SearchWhatsAppMessages(context.Background(), store.WhatsAppSearch{Query: q, Chat: *chat, Sender: *sender, Cursor: *cursor, Limit: *limit, Raw: *raw})
	if err != nil {
		return err
	}
	printJSON(page)
	return nil
}

func whatsappUser(args []string) error {
	fs, dir := subFlagSet("whatsapp user", "usage: nusamation whatsapp user [-data-dir DIR] [-addr HOST:PORT] JID\n")
	defaultAddr := os.Getenv("NUSAMATION_ADDR")
	if defaultAddr == "" {
		defaultAddr = config.DefaultAddr
	}
	addr := fs.String("addr", defaultAddr, "running server address")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 1 {
		return usagef("user requires one WhatsApp user JID")
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
	u := url.URL{Scheme: "http", Host: net.JoinHostPort(strings.Trim(host, "[]"), port), Path: "/api/channels/whatsapp/user"}
	q := u.Query()
	q.Set("jid", pos[0])
	u.RawQuery = q.Encode()
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("WhatsApp user lookup: %s", strings.TrimSpace(string(body)))
	}
	fmt.Print(string(body))
	return nil
}

const contactsUsage = `usage: nusamation whatsapp contacts [flags] [text]
Search locally synced contact names and JIDs without connecting another WhatsApp client.
  -n N          Results per page, 1-500 (default 50)
  -page N       Page number starting at 1 (default 1)
  -data-dir DIR NusaMation data directory
`

type waContact struct {
	JID  string            `json:"jid"`
	Info types.ContactInfo `json:"info"`
}

func whatsappContacts(args []string) error {
	fs, dir := subFlagSet("whatsapp contacts", contactsUsage)
	limit := fs.Int("n", 50, "page size")
	page := fs.Int("page", 1, "page number")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 1 {
		return usagef("contact text must be a single quoted argument")
	}
	if *limit < 1 || *limit > 500 || *page < 1 {
		return usagef("n must be 1-500 and page must be at least 1")
	}
	q := ""
	if len(pos) > 0 {
		q = strings.ToLower(pos[0])
	}
	path := filepath.Join(*dir, "whatsapp.db")
	path, err = filepath.Abs(path)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("WhatsApp database: %w", err)
	}
	uri := url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&_pragma=busy_timeout(8000)"}
	db, err := sql.Open("sqlite", uri.String())
	if err != nil {
		return err
	}
	container := sqlstore.NewWithDB(db, "sqlite", nil)
	defer container.Close()
	ctx := context.Background()
	device, err := container.GetFirstDevice(ctx)
	if err != nil {
		return err
	}
	if device.ID == nil {
		return fmt.Errorf("WhatsApp has not been paired")
	}
	contacts, err := device.Contacts.GetAllContacts(ctx)
	if err != nil {
		return err
	}
	all := make([]waContact, 0, len(contacts))
	for jid, info := range contacts {
		value := strings.ToLower(jid.String() + " " + info.FirstName + " " + info.FullName + " " + info.PushName + " " + info.BusinessName)
		if strings.Contains(value, q) {
			all = append(all, waContact{jid.String(), info})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].JID < all[j].JID })
	start := len(all)
	if *page-1 <= len(all)/(*limit) {
		start = (*page - 1) * (*limit)
		if start > len(all) {
			start = len(all)
		}
	}
	end := start + *limit
	if end > len(all) {
		end = len(all)
	}
	result := struct {
		Contacts []waContact `json:"contacts"`
		Page     int         `json:"page"`
		HasMore  bool        `json:"hasMore"`
	}{all[start:end], *page, end < len(all)}
	printJSON(result)
	return nil
}
