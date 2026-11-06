# ACP prompt context

## Protocol findings

Checked on 2026-11-06 against the ACP sources:

- [Stable v1 session setup](https://agentclientprotocol.com/protocol/v1/session-setup) does not define a client system-prompt field. [Prompt turns](https://agentclientprotocol.com/protocol/v1/prompt-turn) carry user messages as content blocks.
- [Draft v1 session setup](https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/protocol/v1/draft/session-setup.mdx#system-prompt-unstable) defines an **unstable** `systemPrompt` object with `mode` and `content`. It is gated by `agentCapabilities.sessionCapabilities.systemPrompt` and applies to session lifecycle requests.
- The [client-provided system prompt RFD](https://github.com/agentclientprotocol/agent-client-protocol/blob/main/docs/rfds/client-system-prompt.mdx) explains append/override modes and lifecycle semantics. This is not a universally implemented agent capability.

XML tags such as `<system-reminder>` do not create a system-role message. ACP's user-turn transport does not assign instruction priority based on tag names. Neither `_meta`, resource blocks, nor an arbitrary extra `role` field should be treated as a portable system-prompt mechanism.

## NusaMation's current contract

Keep session-wide instructions separate in concept from per-input facts. A shared conversation can receive web messages, WhatsApp messages, and scheduled tasks; its current delivery surface is not a permanent agent persona.

`internal/chat/prompt_context.go` composes one versioned JSON context block, enclosed in `<nusamation-context>`, after the user content and attachments. The wrapper is a NusaMation convention, not an ACP feature or a privilege boundary. JSON encoding preserves quoted text, paths, and delimiters as field values. The original user message remains in its own content block and in storage.

The context contains:

| Field | Meaning |
| --- | --- |
| `version` | Context format version, currently 1 |
| `feature` | `chat` or `automation` |
| `inputChannel` | Where the input was submitted |
| `deliveryChannel` | Where this turn's response will be delivered |
| `automationRunId` | Persisted scheduled-run identity, when present |
| `whatsapp` | Target, message ID, sender ID, and quoted text when available |
| `instructions` | Current delivery formatting and relevant operational guidance |

Formatting still comes from each channel's existing `ResponseStyler`. Web input sent to WhatsApp uses WhatsApp formatting. WhatsApp input with reply-to-origin disabled uses web formatting. Automation uses its actual delivery destination and is identified independently of that destination.

Steering includes the incoming message's source context, while retaining the running turn's reply destination and formatting. Steering does not move the active delivery. A rejected steer that becomes a queued turn receives fresh context when that turn starts.

Automation submission persists `automationRunId` on the message before queue execution, so queued runs retain the distinction across restart. Older messages without this metadata retain the default `chat` classification; no historical data is rewritten.

The context is sent on every turn so channel changes and recovered sessions receive current facts. Appending it preserves earlier transcript ordering; actual prompt caching and instruction adherence depend on the adapter and model. No cache-hit guarantee is implied.

## Native system prompts

NusaMation currently uses the stable user-turn transport for this context. It does not send the draft `systemPrompt` field or rewrite workspace instruction files per channel.

If native support is adopted, use only agents that advertise the capability, prefer `append` to retain the agent's own operating instructions, and resend the same session-wide content on new/load/resume (and fork if implemented). Keep dynamic channel facts in each turn. Handle a rejected native prompt explicitly; do not silently claim system-level delivery after falling back to a user message. Agent-specific CLI flags or instruction files require adapter-specific verification and should not be assumed portable.

## Verification and limits

Tests cover web/WhatsApp source versus destination, scheduled-run metadata persistence, unstyled channels, quoted Unicode and delimiter round trips, and cross-channel steering through the fake ACP agent. Run `make test`, `make vet`, and `make build`.

These checks verify composition, persistence, routing, and transport. They do not measure an LLM's compliance. Before claiming better model adherence, evaluate each deployed adapter/model on alternating web/WhatsApp turns, automation in a reused session, recovery, and steering. Check the requested task outcome and destination formatting; do not infer quality from the wrapper name.
