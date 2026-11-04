package chat

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"slices"
	"unicode/utf8"

	"github.com/jahrulnr/nusamation/internal/agent"
	"github.com/jahrulnr/nusamation/internal/ids"
)

type Elicitation struct {
	ID             string          `json:"id"`
	ConversationID string          `json:"conversationId"`
	AgentID        string          `json:"agentId"`
	Request        json.RawMessage `json:"request"`
}

type ElicitationResponse struct {
	Action  string         `json:"action"`
	Content map[string]any `json:"content,omitempty"`
}

type elicitationWaiter struct {
	request Elicitation
	result  chan ElicitationResponse
}

// Elicit keeps the ACP request alive until the user answers or its turn ends.
func (s *Service) Elicit(ctx context.Context, sess *agent.Session, request json.RawMessage) (any, error) {
	e := Elicitation{ID: ids.New("el"), ConversationID: sess.ConversationID(), AgentID: sess.AgentID(), Request: append(json.RawMessage(nil), request...)}
	w := &elicitationWaiter{request: e, result: make(chan ElicitationResponse, 1)}
	s.mu.Lock()
	s.elicitations[e.ID] = w
	s.mu.Unlock()
	s.emit("elicitation.created", e.ConversationID, e)
	defer func() {
		s.mu.Lock()
		delete(s.elicitations, e.ID)
		s.mu.Unlock()
		s.dropPollWaiters(func(w *pollWaiter) bool { return w.elicitationID == e.ID })
		s.emit("elicitation.resolved", e.ConversationID, map[string]any{"id": e.ID, "conversationId": e.ConversationID})
	}()
	var turnDone <-chan struct{}
	if active := s.activeFor(e.ConversationID); active != nil {
		turnDone = active.ctx.Done()
		s.notifyElicitation(active, e)
	}
	select {
	case result := <-w.result:
		return result, nil
	case <-ctx.Done():
		return ElicitationResponse{Action: "cancel"}, nil
	case <-turnDone:
		return ElicitationResponse{Action: "cancel"}, nil
	}
}

func (s *Service) PendingElicitations(conversationID string) []Elicitation {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Elicitation{}
	for _, w := range s.elicitations {
		if w.request.ConversationID == conversationID {
			out = append(out, w.request)
		}
	}
	return out
}

func (s *Service) ResolveElicitation(conversationID, id string, response ElicitationResponse) error {
	if response.Action != "accept" && response.Action != "decline" && response.Action != "cancel" {
		return fmt.Errorf("invalid elicitation action")
	}
	if response.Action != "accept" {
		response.Content = nil
	}
	s.mu.Lock()
	w := s.elicitations[id]
	if w == nil || w.request.ConversationID != conversationID {
		s.mu.Unlock()
		return fmt.Errorf("this request is no longer pending")
	}
	if response.Action == "accept" {
		if err := validateElicitation(w.request.Request, response.Content); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	delete(s.elicitations, id)
	s.mu.Unlock()
	s.dropPollWaiters(func(w *pollWaiter) bool { return w.elicitationID == id })
	w.result <- response
	return nil
}

func validateElicitation(raw json.RawMessage, content map[string]any) error {
	var req struct {
		Mode   string `json:"mode"`
		URL    string `json:"url"`
		Schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
			Required   []string                   `json:"required"`
		} `json:"requestedSchema"`
	}
	if err := json.Unmarshal(raw, &req); err != nil {
		return err
	}
	if req.Mode == "url" {
		u, err := url.ParseRequestURI(req.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return fmt.Errorf("invalid URL")
		}
		return nil
	}
	if req.Mode != "form" {
		return fmt.Errorf("unsupported request mode")
	}
	for _, key := range req.Schema.Required {
		if _, ok := content[key]; !ok {
			return fmt.Errorf("%s is required", key)
		}
	}
	for key, value := range content {
		rawSpec, ok := req.Schema.Properties[key]
		if !ok {
			return fmt.Errorf("unknown field %s", key)
		}
		var spec struct {
			Type  string   `json:"type"`
			Enum  []string `json:"enum"`
			OneOf []struct {
				Const string `json:"const"`
			} `json:"oneOf"`
			MinLength *int     `json:"minLength"`
			MaxLength *int     `json:"maxLength"`
			Pattern   string   `json:"pattern"`
			Minimum   *float64 `json:"minimum"`
			Maximum   *float64 `json:"maximum"`
			MinItems  *int     `json:"minItems"`
			MaxItems  *int     `json:"maxItems"`
			Items     struct {
				Enum  []string `json:"enum"`
				AnyOf []struct {
					Const string `json:"const"`
				} `json:"anyOf"`
			} `json:"items"`
		}
		if err := json.Unmarshal(rawSpec, &spec); err != nil {
			return err
		}
		switch spec.Type {
		case "string":
			v, ok := value.(string)
			if !ok {
				return fmt.Errorf("%s must be text", key)
			}
			if spec.MinLength != nil && utf8.RuneCountInString(v) < *spec.MinLength || spec.MaxLength != nil && utf8.RuneCountInString(v) > *spec.MaxLength {
				return fmt.Errorf("%s has an invalid length", key)
			}
			if spec.Pattern != "" {
				re, err := regexp.Compile(spec.Pattern)
				if err != nil || !re.MatchString(v) {
					return fmt.Errorf("%s does not match the requested format", key)
				}
			}
			choices := append([]string(nil), spec.Enum...)
			for _, c := range spec.OneOf {
				choices = append(choices, c.Const)
			}
			if len(choices) > 0 && !slices.Contains(choices, v) {
				return fmt.Errorf("%s has an invalid choice", key)
			}
		case "number", "integer":
			v, ok := value.(float64)
			if !ok || spec.Type == "integer" && math.Trunc(v) != v {
				return fmt.Errorf("%s must be a number", key)
			}
			if spec.Minimum != nil && v < *spec.Minimum || spec.Maximum != nil && v > *spec.Maximum {
				return fmt.Errorf("%s is outside the requested range", key)
			}
		case "boolean":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("%s must be true or false", key)
			}
		case "array":
			v, ok := value.([]any)
			if !ok {
				return fmt.Errorf("%s must be a list", key)
			}
			if spec.MinItems != nil && len(v) < *spec.MinItems || spec.MaxItems != nil && len(v) > *spec.MaxItems {
				return fmt.Errorf("%s has an invalid number of choices", key)
			}
			choices := append([]string(nil), spec.Items.Enum...)
			for _, c := range spec.Items.AnyOf {
				choices = append(choices, c.Const)
			}
			for _, item := range v {
				text, ok := item.(string)
				if !ok || !slices.Contains(choices, text) {
					return fmt.Errorf("%s has an invalid choice", key)
				}
			}
		default:
			return fmt.Errorf("unsupported field type %q", spec.Type)
		}
	}
	return nil
}
