# ACP prompt context

## Protocol findings

Checked on 2026-11-11 against the [ACP session setup specification](https://agentclientprotocol.com/protocol/v1/session-setup#system-prompt) and [SDK protocol reference](https://agentclientprotocol.github.io/rust-sdk/protocol.html):

- Schema 1.16 defines `agentCapabilities.sessionCapabilities.systemPrompt` and the `systemPrompt` field on session new, load, and resume. It is not universally implemented by adapters.
- An advertised object enables text content in `append` mode. ACP says the agent should give it system-level precedence; exact placement is agent-dependent. Only trusted client instructions belong there.
- A client should send the same prompt on load and resume. An agent that cannot apply it must reject the operation.

XML tags such as `<system-reminder>` do not create a system-role message. ACP's user-turn transport does not assign instruction priority based on tag names. Neither `_meta`, resource blocks, nor an arbitrary extra `role` field should be treated as a portable system-prompt mechanism.

## NusaMation's current contract

Keep session-wide instructions separate in concept from per-input facts. A shared conversation can receive web messages, WhatsApp messages, and scheduled tasks; its current delivery surface is not a permanent agent persona.

`internal/chat/prompt_context.go` appends a concise `<nusamation-context>` text block after the user content and attachments. It is user-role content, not an ACP feature or a privilege boundary. Each block identifies the input source (`Send-by`), the actual delivery destination and its formatting rules (`Response-output`), and the NusaMation workspace instructions (`Harness rule`). For example:

```text
<nusamation-context>
Send-by: whatsapp
Response-output: web — Full Markdown, including code, tables, math, footnotes and Mermaid diagrams.
Harness rule: /path/to/workspace/AGENTS.md
</nusamation-context>
```

Automation runs add their run ID and purpose. Inputs from another NusaMation conversation identify their origin and control restriction. WhatsApp-origin inputs with source metadata include the target, message ID, sender ID, and quoted text needed for native replies or reactions; their values are individually quoted and escaped so line breaks and closing tags remain part of the value. The original user message remains in its own content block and in storage.

Formatting comes from each channel's `ResponseStyler`. Web input sent to WhatsApp uses WhatsApp formatting. WhatsApp input with reply-to-origin disabled uses web formatting. Automation uses its actual delivery destination and is identified independently of that destination.

Steering includes the incoming message's source context, while retaining the running turn's reply destination and formatting. Steering does not move the active delivery. A rejected steer that becomes a queued turn receives fresh context when that turn starts.

Automation submission persists `automationRunId` on the message before queue execution, so queued runs retain the distinction across restart. Older messages without this metadata retain the default `chat` classification; no historical data is rewritten.

The context is sent on ordinary turns so channel changes and recovered sessions receive current facts. Slash-prefixed inputs and selected agent commands are sent without the additional context block so adapters can recognize commands expecting a plain prompt. A selected non-command reference starting with `/` is identified as a reference rather than interpreted as a command. Appending context preserves earlier transcript ordering; actual prompt caching and instruction adherence depend on the adapter and model. No cache-hit guarantee is implied.

## Session instructions and fallback

NusaMation sends stable instructions for interpreting the context and using conversation-control tools in `systemPrompt` with `mode: append` when the initialized agent advertises the capability object. The same instructions are sent on `session/new`, `session/load`, and `session/resume`. A lifecycle rejection is returned as an error rather than silently claiming that native instructions were applied.

When the agent does not advertise support, the first ordinary user prompt after creating **or attaching** a session contains those instructions in a separate `<nusamation-session-instructions>` text block. Slash commands defer this bootstrap and any recovery recap until the next ordinary prompt. Attaching includes load and resume, which also covers sessions created before this feature existed. This block remains user-role content; it does not acquire system-level priority. No separate bootstrap turn is sent. On a failed prompt the next attempt includes the bootstrap again. Compaction retention of fallback instructions depends on the adapter; essential delivery context and channel rules remain in ordinary inputs.

## Verification and limits

Tests cover capability detection, new/load/resume payloads, fallback on first input and reattachment, web/WhatsApp source versus destination, scheduled-run metadata persistence, unstyled channels, quoted Unicode and delimiter escaping, and cross-channel steering through the fake ACP agent. Run `make test`, `make vet`, and `make build`.

These checks verify composition, persistence, routing, and transport. They do not measure an LLM's compliance. Before claiming better model adherence, evaluate each deployed adapter/model on alternating web/WhatsApp turns, automation in a reused session, recovery, and steering. Check the requested task outcome and destination formatting; do not infer quality from the wrapper name.
