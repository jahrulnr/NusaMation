package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"time"
)

// defaultAPIBase is the public Bot API endpoint; tests point the client at an
// httptest server instead — api.telegram.org is never dialled from tests.
const defaultAPIBase = "https://api.telegram.org"

// apiClient is the thinnest possible Bot API wrapper: POST JSON to
// /bot<token>/<method>, decode the {ok,result,description,error_code}
// envelope. It deliberately carries no Bot API semantics beyond the methods
// this slice needs.
type apiClient struct {
	token string
	base  string
	http  *http.Client
}

func newAPIClient(token, base string) *apiClient {
	if base == "" {
		base = defaultAPIBase
	}
	return &apiClient{token: token, base: base, http: &http.Client{Timeout: 0}}
}

type apiError struct {
	code       int
	desc       string
	retryAfter int
}

func (e *apiError) Error() string {
	if e.code != 0 {
		return fmt.Sprintf("telegram api error %d: %s", e.code, e.desc)
	}
	return "telegram api error: " + e.desc
}

// retryAfterDelay reads Telegram's 429 hint. A hint above a minute is capped
// so a hostile or buggy response cannot park a poll loop indefinitely.
func retryAfterDelay(err error) (time.Duration, bool) {
	var ae *apiError
	if !errors.As(err, &ae) || ae.code != 429 || ae.retryAfter <= 0 {
		return 0, false
	}
	secs := min(ae.retryAfter, 60)
	return time.Duration(secs) * time.Second, true
}

type apiEnvelope struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

// isParseError reports whether Telegram refused the HTML entities. That
// failure is recoverable: the same content is accepted unparsed.
func isParseError(err error) bool {
	var ae *apiError
	if !errors.As(err, &ae) {
		return false
	}
	desc := strings.ToLower(ae.desc)
	return ae.code == 400 &&
		(strings.Contains(desc, "parse entities") || strings.Contains(desc, "parse_entities"))
}

// call posts one JSON Bot API method. The caller owns the context deadline —
// getUpdates long polls need ~35s while ordinary calls are bounded by their
// callers, so the HTTP client itself has no timeout.
func (a *apiClient) call(ctx context.Context, method string, params, out any) error {
	var body io.Reader
	contentType := ""
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
		contentType = "application/json"
	}
	return a.do(ctx, method, body, contentType, out)
}

// do posts one prepared Bot API request and decodes the {ok,result} envelope.
func (a *apiClient) do(ctx context.Context, method string, body io.Reader, contentType string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.base+"/bot"+a.token+"/"+method, body)
	if err != nil {
		return a.redact(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return a.redact(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return a.redact(err)
	}
	var env apiEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("telegram %s: invalid response (%d): %w", method, resp.StatusCode, err)
	}
	if !env.OK {
		desc := env.Description
		if desc == "" {
			desc = http.StatusText(resp.StatusCode)
		}
		return &apiError{code: env.ErrorCode, desc: desc, retryAfter: env.Parameters.RetryAfter}
	}
	if out != nil && len(env.Result) > 0 {
		return json.Unmarshal(env.Result, out)
	}
	return nil
}

// redact strips the bot token from transport errors. net/http puts the full
// request URL — which carries the token — into its error text, and those
// errors reach logs and channel notices.
func (a *apiClient) redact(err error) error {
	if err == nil || a.token == "" {
		return err
	}
	msg := strings.ReplaceAll(err.Error(), a.token, "<token>")
	if msg == err.Error() {
		return err
	}
	return fmt.Errorf("%s", msg)
}

var quoteEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`)

// safeUploadName makes a file name safe for a multipart Content-Disposition
// header. Control characters (CR/LF in particular) would let an agent-supplied
// label inject header lines, so they are removed; path separators and length
// are normalised too. The result is always non-empty.
func safeUploadName(name string) string {
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, name)
	name = strings.TrimSpace(strings.ReplaceAll(name, `\`, "/"))
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "attachment"
	}
	runes := []rune(name)
	if len(runes) > 120 {
		name = string(runes[len(runes)-120:])
	}
	return name
}

// sendMedia uploads one attachment as multipart/form-data and sends it with
// the given Bot API method (sendPhoto, sendDocument, …).
func (a *apiClient) sendMedia(ctx context.Context, method, field, fileName, mime string, data []byte, fields map[string]string) (*tgMessage, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`,
		quoteEscaper.Replace(field), quoteEscaper.Replace(safeUploadName(fileName))))
	if mime != "" {
		h.Set("Content-Type", mime)
	}
	part, err := w.CreatePart(h)
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	var out tgMessage
	if err := a.do(ctx, method, &buf, w.FormDataContentType(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

/* ---------------- Bot API types (subset this slice uses) ---------------- */

type tgUser struct {
	ID        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

type tgChat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"` // private, group, supergroup, channel
	Title     string `json:"title"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
}

// tgEntity offsets are UTF-16 code units, not bytes — see entityText.
type tgEntity struct {
	Type   string  `json:"type"` // mention, text_mention, bot_command, ...
	Offset int     `json:"offset"`
	Length int     `json:"length"`
	User   *tgUser `json:"user"`
}

// tgMedia is the file-carrying slice shared by document, video, audio, voice,
// video_note, animation and sticker payloads.
type tgMedia struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name"`
	MIME     string `json:"mime_type"`
	FileSize int64  `json:"file_size"`
}

// tgPhotoSize is one rendition of a photo; Telegram sends them ascending, so
// the last element is the largest.
type tgPhotoSize struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
}

type tgMessage struct {
	MessageID       int64      `json:"message_id"`
	MessageThreadID int64      `json:"message_thread_id"`
	From            *tgUser    `json:"from"`
	Chat            tgChat     `json:"chat"`
	Date            int64      `json:"date"`
	Text            string     `json:"text"`
	Entities        []tgEntity `json:"entities"`
	Caption         string     `json:"caption"`
	// CaptionEntities covers Caption; offsets count UTF-16 units of it.
	CaptionEntities []tgEntity    `json:"caption_entities"`
	Photo           []tgPhotoSize `json:"photo"`
	Document        *tgMedia      `json:"document"`
	Video           *tgMedia      `json:"video"`
	Audio           *tgMedia      `json:"audio"`
	Voice           *tgMedia      `json:"voice"`
	VideoNote       *tgMedia      `json:"video_note"`
	Animation       *tgMedia      `json:"animation"`
	Sticker         *tgMedia      `json:"sticker"`
	// MediaGroupID is shared by every item of one album.
	MediaGroupID   string     `json:"media_group_id"`
	ReplyToMessage *tgMessage `json:"reply_to_message"`
	// ViaBot is set when the message was sent through a bot's inline mode;
	// combined with the bot's own username it marks an inline pick.
	ViaBot *tgUser `json:"via_bot"`
}

// tgInlineQuery arrives while a user types "@botname <text>" — the bot
// answers it with tappable article results via answerInlineQuery.
type tgInlineQuery struct {
	ID     string  `json:"id"`
	From   *tgUser `json:"from"`
	Query  string  `json:"query"`
	Offset string  `json:"offset"`
}

type tgUpdate struct {
	UpdateID    int64          `json:"update_id"`
	Message     *tgMessage     `json:"message"`
	InlineQuery *tgInlineQuery `json:"inline_query"`
}

// getUpdates long polls; the caller's context bounds the wait. allowed_updates
// is pinned to "message" and "inline_query" — edits, channel posts and
// callback queries are not delivered at all rather than received and discarded.
func (a *apiClient) getUpdates(ctx context.Context, offset int64, timeoutSec int) ([]tgUpdate, error) {
	var out []tgUpdate
	err := a.call(ctx, "getUpdates", map[string]any{
		"offset":          offset,
		"timeout":         timeoutSec,
		"allowed_updates": []string{"message", "inline_query"},
	}, &out)
	return out, err
}

func (a *apiClient) getMe(ctx context.Context) (*tgUser, error) {
	var me tgUser
	if err := a.call(ctx, "getMe", map[string]any{}, &me); err != nil {
		return nil, err
	}
	return &me, nil
}

type botCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

// setMyCommands replaces the bot's "/" menu under the given scope — the
// default scope covers every chat; a {"type":"chat","chat_id":…} scope
// overrides the menu for one chat (session-advertised commands).
func (a *apiClient) setMyCommands(ctx context.Context, cmds []botCommand, scope map[string]any) error {
	return a.call(ctx, "setMyCommands", map[string]any{
		"commands": cmds,
		"scope":    scope,
	}, nil)
}

// deleteMyCommands removes a scoped menu so the chat falls back to the
// default scope's menu.
func (a *apiClient) deleteMyCommands(ctx context.Context, scope map[string]any) error {
	return a.call(ctx, "deleteMyCommands", map[string]any{"scope": scope}, nil)
}

// inlineResult is one answerInlineQuery article — tapping it inserts
// input_message_content.message_text as the user's message.
type inlineResult struct {
	Type                string         `json:"type"`
	ID                  string         `json:"id"`
	Title               string         `json:"title"`
	Description         string         `json:"description,omitempty"`
	InputMessageContent map[string]any `json:"input_message_content"`
}

// answerInlineQuery replies to one inline_query. is_personal keeps results
// out of Telegram's cross-user cache; cache_time 0 asks clients to re-query
// every keystroke so room lists stay fresh.
func (a *apiClient) answerInlineQuery(ctx context.Context, queryID string, results []inlineResult) error {
	if results == nil {
		results = []inlineResult{}
	}
	return a.call(ctx, "answerInlineQuery", map[string]any{
		"inline_query_id": queryID,
		"results":         results,
		"is_personal":     true,
		"cache_time":      0,
	}, nil)
}

// sendChatAction shows a transient status such as "typing". Telegram expires
// it after about five seconds, so delivery loops refresh it while a turn runs.
func (a *apiClient) sendChatAction(ctx context.Context, chatID, threadID int64) error {
	params := map[string]any{"chat_id": chatID, "action": "typing"}
	if threadID != 0 {
		params["message_thread_id"] = threadID
	}
	return a.call(ctx, "sendChatAction", params, nil)
}

// sendMessage delivers one message. parseMode is "" for unparsed text and
// "HTML" when text was sanitized; message_thread_id keeps forum topics
// separate — an outgoing reply must land back in the topic the inbound
// message arrived in, or the forum's threads merge into one stream.
func (a *apiClient) sendMessage(ctx context.Context, chatID, threadID int64, text, parseMode string) (*tgMessage, error) {
	params := map[string]any{"chat_id": chatID, "text": text}
	if threadID != 0 {
		params["message_thread_id"] = threadID
	}
	if parseMode != "" {
		params["parse_mode"] = parseMode
	}
	var out tgMessage
	if err := a.call(ctx, "sendMessage", params, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// tgFile is getFile's result. FilePath is relative to /file/bot<token>/.
type tgFile struct {
	FileID   string `json:"file_id"`
	FileSize int64  `json:"file_size"`
	FilePath string `json:"file_path"`
}

func (a *apiClient) getFile(ctx context.Context, fileID string) (*tgFile, error) {
	var out tgFile
	if err := a.call(ctx, "getFile", map[string]any{"file_id": fileID}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// downloadFile fetches a getFile result from the Bot API file endpoint,
// bounded by max bytes. The URL contains the token; every error leaving here
// is redacted first.
func (a *apiClient) downloadFile(ctx context.Context, filePath string, max int64) ([]byte, error) {
	if max <= 0 {
		max = 25 << 20
	}
	url := a.base + "/file/bot" + a.token + "/" + strings.TrimPrefix(filePath, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, a.redact(err)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, a.redact(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("telegram file download failed: %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, a.redact(err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("telegram file exceeds the %d byte limit", max)
	}
	return data, nil
}
