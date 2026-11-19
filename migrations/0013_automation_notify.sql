-- Automation runs can mirror their final answer into a chat channel (for
-- example a WhatsApp chat). The job carries the channel name and target
-- reference; the scheduler wires the run conversation's channel link and
-- mirror routing at launch time.

ALTER TABLE automations ADD COLUMN notify_channel TEXT NOT NULL DEFAULT '';
ALTER TABLE automations ADD COLUMN notify_target TEXT NOT NULL DEFAULT '';
