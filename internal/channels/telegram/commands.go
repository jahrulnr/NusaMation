package telegram

// menuCommands is the fixed "/" menu published with setMyCommands when the
// bot connects. The order matches the spec — start/help first, then the same
// order helpOrder uses — and every name satisfies Telegram's
// ^[a-z0-9_]{1,32}$ grammar with a 1–256 character description. /cancel stays
// handled by the chat parser but is deliberately absent from the menu.
var menuCommands = []botCommand{
	{Command: "start", Description: "Show the welcome and command list"},
	{Command: "help", Description: "Show the command list"},
	{Command: "new", Description: "Start a fresh conversation"},
	{Command: "agent", Description: "List or switch agents"},
	{Command: "model", Description: "List or change the model"},
	{Command: "mode", Description: "List or change the session mode"},
	{Command: "set", Description: "Change any session option"},
	{Command: "status", Description: "Agent, model, turn and approval status"},
	{Command: "stop", Description: "Cancel the running turn"},
	{Command: "retry", Description: "Retry the last message"},
	{Command: "approve", Description: "List or allow a pending tool call"},
	{Command: "deny", Description: "Reject a pending tool call"},
}
