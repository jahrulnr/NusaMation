# Scheduled automations

An automation runs a saved prompt through an enabled ACP agent. Each job has a
name, prompt, agent, schedule, timezone, session mode and optional expiry. The
server scheduler must be running for jobs to execute. Automation turns use the
normal chat queue, so agent permissions and approvals still apply.

## Create and manage in the UI

1. Open **Control center → Automations → Add automation**.
2. Enter a name and prompt, and select the ACP agent.
3. Choose **New chat each run** (`isolated`) or **Reuse dedicated chat**
   (`same`). Each isolated run creates its own conversation. A same-session
   job reuses its dedicated conversation, including its chat context.
4. Choose **Run once** with a date and time, or **Repeat (cron)**. The schedule
   builder offers minute/hour intervals and daily, weekly and monthly choices;
   the resulting five-field expression remains editable.
5. Set the timezone and, optionally, **Expires at**, then save.

The list provides Edit, Pause/Resume, Delete and History. A history entry shows
its status and can open its chat. Deleting a job removes that job's run history,
but does not delete its conversation transcripts.

## CLI

All commands accept `--data-dir DIR` (default `~/.nusamation`). Use an enabled
agent's slug or ID; `nusamation agent list` shows available agents. Times can
be RFC3339 with an offset, or `YYYY-MM-DDTHH:MM` in the chosen IANA timezone.
The timezone defaults to the CLI machine's local timezone if `--tz` is
omitted.

```bash
# Run every day at 09:00 Jakarta time, reusing one dedicated chat.
nusamation automation add --name 'Daily review' --prompt 'Review the workspace' \
  --agent codex --cron '0 9 * * *' --tz Asia/Jakarta --session same \
  --expires '2026-12-31T18:00'

# Schedule a one-time run in a new chat.
nusamation automation add --name 'One-off review' --prompt 'Inspect the logs' \
  --agent codex --at '2026-12-01T09:00' --tz Asia/Jakarta --session isolated

nusamation automation list --json
nusamation automation get <id> --json
nusamation automation history <id> --json
nusamation automation set <id> --prompt 'Updated prompt' --expires '2026-12-31T18:00'
nusamation automation set <id> --expires ''       # remove expiry
nusamation automation pause <id>
nusamation automation resume <id>
nusamation automation remove <id>
```

`set` changes the specified fields; use `--at` or `--cron` to change the
schedule, and `--session isolated|same` to change the session mode. A running
server polls the database and picks up CLI changes without a restart. `list`,
`get` and `history` support `--json`. IDs are returned by `add` and `list`.

## Schedule, expiry and recovery

- Cron has five fields: minute, hour, day of month, month and day of week.
  `0 9 * * *` means 09:00 every day in the job's timezone. The UI and CLI
  validate the expression before saving.
- If the server was offline at one or more scheduled times, an overdue job is
  triggered once after it starts again, then returns to its normal schedule.
  Pausing prevents execution; after resuming, an overdue job is likewise
  eligible for one catch-up run. A job past its expiry is not triggered.
- A run submitted to a busy dedicated chat waits in that chat's queue. If it
  reaches the queue head after expiry, its turn is cancelled instead of
  starting. Expiry does not stop a turn that has already started.
- If a dedicated chat was deleted, the next run creates a replacement. If it
  was archived or switched to a different agent, launch fails and the error is
  recorded in history.
- A server interruption after a scheduler claim but before its turn is linked
  leaves an `interrupted` run rather than automatically replaying it. This
  recovery status is set after the claim has been outstanding for five
  minutes. Inspect the linked chat, when available, before deciding to retry.
- Runs appear in the web conversation and history; scheduling does not itself
  send the result to a WhatsApp chat. See [whatsapp.md](whatsapp.md) for chat
  and agent routing.

For API payloads and endpoints, see [api.md](api.md).
