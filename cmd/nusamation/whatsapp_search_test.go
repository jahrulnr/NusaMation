package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/jahrulnr/nusamation/internal/store"
	"go.mau.fi/whatsmeow/proto/waAdv"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
)

func captureCommand(t *testing.T, fn func() error) []byte {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	defer func() { os.Stdout = original }()
	err = fn()
	writer.Close()
	out, readErr := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	return out
}

func TestWhatsAppSearchCommand(t *testing.T) {
	dir := t.TempDir()
	st, err := openStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		if err := st.SaveWhatsAppMessage(context.Background(), store.WhatsAppMessage{Chat: "c@g.us", ID: id, Timestamp: 10, Text: "upgrade php"}); err != nil {
			t.Fatal(err)
		}
	}
	st.Close()
	out := captureCommand(t, func() error { return whatsappSearch([]string{"upgrade php", "-n", "1", "-data-dir", dir}) })
	var first store.WhatsAppSearchPage
	if err := json.Unmarshal(out, &first); err != nil || len(first.Messages) != 1 || first.NextCursor == "" {
		t.Fatalf("first %s: %v", out, err)
	}
	out = captureCommand(t, func() error {
		return whatsappSearch([]string{"-cursor", first.NextCursor, "-n", "1", "-data-dir", dir, "upgrade php"})
	})
	var second store.WhatsAppSearchPage
	if err := json.Unmarshal(out, &second); err != nil || len(second.Messages) != 1 || second.NextCursor != "" || second.Messages[0].ID == first.Messages[0].ID {
		t.Fatalf("second %s: %v", out, err)
	}
}

func TestWhatsAppContactsReadsCachedNames(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	container, err := sqlstore.New(ctx, "sqlite", "file:"+filepath.Join(dir, "whatsapp.db")+"?_pragma=foreign_keys(1)", nil)
	if err != nil {
		t.Fatal(err)
	}
	device := container.NewDevice()
	device.Account = &waAdv.ADVSignedDeviceIdentity{Details: []byte{1}, AccountSignature: make([]byte, 64), AccountSignatureKey: make([]byte, 32), DeviceSignature: make([]byte, 64)}
	jid := types.NewJID("100", types.DefaultUserServer)
	device.ID = &jid
	if err := device.Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := device.Contacts.PutContactName(ctx, types.NewJID("200", types.DefaultUserServer), "Alice", "Alice Example"); err != nil {
		t.Fatal(err)
	}
	container.Close()
	out := captureCommand(t, func() error { return whatsappContacts([]string{"Alice", "-data-dir", dir}) })
	var result struct {
		Contacts []waContact `json:"contacts"`
	}
	if err := json.Unmarshal(out, &result); err != nil || len(result.Contacts) != 1 || result.Contacts[0].JID != "200@s.whatsapp.net" {
		t.Fatalf("contacts %s: %v", out, err)
	}
}
