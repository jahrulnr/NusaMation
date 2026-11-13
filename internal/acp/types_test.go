package acp

import (
	"errors"
	"testing"
)

func TestIsUnknownSession(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"not an RPC error", errors.New("boom"), false},
		{"nil RPC error", (*RPCError)(nil), false},
		{"resource not found", &RPCError{Code: CodeResourceNotFound, Message: "no such session"}, true},
		{"invalid params", &RPCError{Code: CodeInvalidParams, Message: "bad session"}, true},
		{"archived session in data", &RPCError{
			Code:    CodeInternalError,
			Message: "Internal error",
			Data:    []byte(`{"details":"session 01a1c55e is archived. Run 'codex unarchive' to unarchive it first."}`),
		}, true},
		{"archived session in message", &RPCError{Code: CodeInternalError, Message: "Session is archived"}, true},
		{"internal error without archive", &RPCError{Code: CodeInternalError, Message: "Internal error", Data: []byte(`{"details":"disk full"}`)}, false},
		{"method not found", &RPCError{Code: CodeMethodNotFound, Message: "nope"}, false},
		{"auth required", &RPCError{Code: CodeAuthRequired, Message: "login"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsUnknownSession(tc.err); got != tc.want {
				t.Fatalf("IsUnknownSession() = %v, want %v", got, tc.want)
			}
		})
	}
}
