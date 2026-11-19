import { $, esc, icon } from "./util.js";
import { S } from "./state.js";
import { api } from "./api.js";
import { toast } from "./toast.js";
import { confirmModal } from "./modal.js";
import { channelCards, renderSettings } from "./settings.js";

// telegramPanel renders the Telegram channel screen. It mirrors the WhatsApp
// panel's layout — status hero, behaviour toggles, allowlists, blocked ring —
// but pairs with a bot token instead of a QR flow.
export function telegramPanel() {
  const ch = S.telegram || S.channels.find((c) => c.name === "telegram");
  if (!ch) {
    if (S.bootstrap?.app?.telegramDisabled) {
      return `<div class="empty-settings">Telegram is disabled for this process.<br>Remove <code class="mono">NUSAMATION_NO_TELEGRAM</code> and restart to enable it.</div>`;
    }
    return `<div class="empty-settings">Telegram failed to start for this process.<br>Check the server log for the startup error.</div>`;
  }
  const settings = S.telegramSettings || { enabled: ch.enabled, autoStart: true, groupPolicy: "mention", allowedUserIds: [], allowedGroupIds: [], tokenSet: false };
  const tokenSet = !!settings.tokenSet;
  const wildcard = (settings.allowedUserIds || []).includes("*");
  const badge = ch.connected ? `<span class="badge-soft green">connected</span>` : ch.state === "connecting" ? `<span class="badge-soft amber">connecting</span>` : ch.state === "error" ? `<span class="badge-soft red">error</span>` : tokenSet ? `<span class="badge-soft gray">${esc(ch.state || "offline")}</span>` : `<span class="badge-soft gray">no token</span>`;
  const botLine = settings.botUsername || ch.selfID
    ? `<h4 style="margin:0 0 4px">${esc(settings.botUsername ? "@" + settings.botUsername.replace(/^@/, "") : ch.selfID)}${ch.stats?.botName ? " · " + esc(ch.stats.botName) : ""}</h4><p style="margin:0;color:var(--muted);font-size:11.5px">Polling Telegram for new messages. Chats get one formatted message when a turn finishes.</p>`
    : `<h4 style="margin:0 0 4px">No bot token</h4><p style="margin:0;color:var(--muted);font-size:11.5px">Create a bot with @BotFather in Telegram, paste its token below, then Connect.</p>`;
  return `<div class="settings-title" style="display:flex;align-items:center;gap:8px">Telegram ${badge}<span class="grow"></span>
    <button class="btn btn-secondary btn-xs" data-action="tg-connect" ${ch.connected || ch.state === "connecting" ? "disabled" : ""}>Connect</button>
    <button class="btn btn-ghost btn-xs" data-action="tg-disconnect" ${!ch.connected && ch.state !== "connecting" ? "disabled" : ""}>Disconnect</button>
    <button class="btn btn-danger btn-xs" data-action="tg-logout" ${!tokenSet ? "disabled" : ""}>Forget token</button></div>
    <div class="settings-desc" style="margin:6px 0 14px">${esc(ch.detail || (ch.connected ? "Long polling is live — Telegram pushes each new message as it arrives." : "Not polling. Connect starts listening once a token is saved."))}</div>
    <div class="settings-desc">The / menu lists the bot commands and, once an agent is connected in that chat, the room's session commands. Type @botname to mention a room or a project — turn inline mode on in @BotFather with /setinline.</div>
    <div class="qr-box"><div class="tool-icon kind-execute" style="width:44px;height:44px">${icon("send")}</div><div class="grow">${botLine}</div></div>
    <div class="settings-section" style="margin-top:20px">
      <div class="settings-title">Bot token</div>
      <div class="settings-desc">From @BotFather. Stored encrypted on this machine and never shown again — saving a new token replaces it; leaving the field empty keeps the current one.</div>
      <div class="form-actions" style="justify-content:flex-start;gap:8px">
        <input class="input" style="width:320px;max-width:100%" type="password" id="tg-token" autocomplete="off" spellcheck="false" placeholder="${tokenSet ? "Token saved — paste a new one to replace" : "123456789:AA…"}">
        <button class="btn btn-secondary btn-sm" data-action="tg-save-token">Save token</button>
      </div>
    </div>
    <div class="settings-section">
      <div class="settings-title">Behaviour</div>
      <div class="settings-row"><div><div class="label">Enabled</div><div class="desc">Accept inbound messages and send replies.</div></div><button role="switch" aria-checked="${settings.enabled}" class="switch ${settings.enabled ? "on" : ""}" data-action="tg-toggle" data-key="enabled"></button></div>
      <div class="settings-row"><div><div class="label">Auto-connect on boot</div><div class="desc">Only when a bot token is already saved.</div></div><button role="switch" aria-checked="${settings.autoStart}" class="switch ${settings.autoStart ? "on" : ""}" data-action="tg-toggle" data-key="autoStart"></button></div>
      <div class="settings-row"><div><div class="label">Group policy</div><div class="desc">Mention requires @bot or a reply to the bot; "all messages" still needs privacy mode turned off in @BotFather or ordinary group messages never reach the bot.</div></div><select class="select" style="width:150px" data-change="tg-group"><option value="mention" ${settings.groupPolicy === "mention" ? "selected" : ""}>mention only</option><option value="all" ${settings.groupPolicy === "all" ? "selected" : ""}>all messages</option><option value="disabled" ${settings.groupPolicy === "disabled" ? "selected" : ""}>disabled</option></select></div>
    </div>
    <div class="settings-section">
      <div class="settings-title">User allowlist</div>
      <div class="settings-desc">One numeric Telegram user id per line. An empty allowlist allows <em>no one</em> — a message only gets a reply once you add an id or <code class="mono">*</code>. The wildcard <code class="mono">*</code> allows every sender and must be the only entry. Group policy still applies to group chats.</div>
      <textarea class="textarea allowlist" id="tg-user-allowlist" placeholder="123456789">${esc((settings.allowedUserIds || []).join("\n"))}</textarea>
      <div class="form-actions" style="justify-content:flex-start"><button class="btn btn-secondary btn-sm" data-action="tg-save-users">Save user allowlist</button><button class="btn btn-ghost btn-sm" data-action="tg-allow-everyone">Allow everyone (*)</button></div>
    </div>
    <div class="settings-section">
      <div class="settings-title">Group allowlist</div>
      <div class="settings-desc">One numeric chat id per line — group and supergroup ids are negative (for example <code class="mono">-1001234567890</code>). The id shows in the blocked list below when a message arrives from an unlisted chat.${wildcard ? ` <b>The user wildcard <code class="mono">*</code> is active — remove it before allowing a group, because it must be the only allowlist entry.</b>` : ""}</div>
      <textarea class="textarea allowlist" id="tg-group-allowlist" placeholder="-1001234567890" ${wildcard ? "disabled" : ""}>${esc((settings.allowedGroupIds || []).join("\n"))}</textarea>
      <div class="form-actions" style="justify-content:flex-start"><button class="btn btn-secondary btn-sm" data-action="tg-save-groups" ${wildcard ? "disabled" : ""}>Save group allowlist</button></div>
    </div>
    ${blockedSection(ch, settings)}`;
}

// blockedSection lists recent senders the allowlist rejected, each with an
// Allow button that appends the numeric id — usernames are display-only
// because they can change. Group traffic also gets Allow-group: whitelisting
// the member's user id would open their private chats, not the group.
function blockedSection(ch, settings) {
  const blocked = ch?.stats?.blocked || [];
  if (!blocked.length) return "";
  const usersAllowed = new Set(settings?.allowedUserIds || []);
  const groupsAllowed = new Set(settings?.allowedGroupIds || []);
  const wildcard = usersAllowed.has("*");
  return `<div class="settings-section">
    <div class="settings-title">Blocked senders <span class="muted" style="font-weight:400">(${blocked.length})</span></div>
    <div class="settings-desc">Messages these senders were ignored. Allowing a user id opens their private chat; for group traffic allow the group's chat id.</div>
    ${blocked.map((b) => {
      const userOn = wildcard || usersAllowed.has(String(b.id));
      const groupOn = groupsAllowed.has(String(b.chat));
      const groupDisabled = wildcard || groupOn;
      const groupLabel = groupOn ? "Group allowed" : "Allow group";
      const groupTitle = wildcard ? ` title="Remove the * user wildcard first — it must be the only allowlist entry"` : "";
      const groupBtn = b.isGroup ? `<button class="btn ${groupOn ? "btn-ghost" : "btn-secondary"} btn-sm" data-action="tg-allow-group" data-chat="${esc(b.chat)}" ${groupDisabled ? "disabled" : ""}${groupTitle}>${groupLabel}</button>` : "";
      const name = b.name || b.username || b.id;
      const sub = [b.chatName && b.chatName !== name ? b.chatName : "", "id " + b.id, b.username ? "@" + b.username : ""].filter(Boolean).join(" · ");
      return `<div class="settings-row"><div><div class="label">${esc(name)}</div><div class="desc mono">${esc(sub)}</div></div><div style="display:flex;gap:6px">${groupBtn}<button class="btn ${userOn ? "btn-ghost" : "btn-secondary"} btn-sm" data-action="tg-allow-sender" data-id="${esc(b.id)}" ${userOn ? "disabled" : ""}>${userOn ? "Allowed" : "Allow"}</button></div></div>`;
    }).join("")}
  </div>`;
}

export function updateTelegramPanel() {
  if (S.view !== "settings" || S.settingsTab !== "channels") return;
  if (!S.channel) {
    const cards = $("#channel-cards");
    if (cards) cards.innerHTML = channelCards();
    return;
  }
  if (S.channel !== "telegram") return;
  const el = $("#tg-panel");
  if (!el) return;
  const active = document.activeElement;
  if (active && typeof active.closest === "function" && active.closest("#tg-panel") && active.matches("input,textarea,select")) return;
  el.innerHTML = telegramPanel();
}

export async function saveTgSettings(patch) {
  const current = S.telegramSettings || { enabled: true, autoStart: true, groupPolicy: "mention", allowedUserIds: [], allowedGroupIds: [] };
  const next = { ...current, ...patch };
  try {
    const d = await api("/channels/telegram/settings", { method: "PUT", body: next });
    S.telegramSettings = d.settings;
    if (d.channel) {
      const i = S.channels.findIndex((c) => c.name === "telegram");
      if (i >= 0) S.channels[i] = d.channel; else S.channels.push(d.channel);
      S.telegram = d.channel;
    }
    updateTelegramPanel();
    return true;
  } catch (e) {
    // Keep the panel mounted: re-rendering here would throw away the text the
    // operator just typed. The toast carries the server's refusal.
    toast("Could not save Telegram settings", e.message, "error");
    return false;
  }
}

export async function refreshTelegram() {
  try {
    const d = await api("/channels");
    S.channels = d.channels || [];
    const tg = S.channels.find((c) => c.name === "telegram");
    if (tg) S.telegram = tg;
    updateTelegramPanel();
  } catch (e) { console.warn(e); }
}

export async function tgAction(action) {
  try {
    if (action === "connect") {
      await api("/channels/telegram/connect", { method: "POST" });
      toast("Connecting Telegram", "Validating the bot token, then polling starts.", "info");
      S.telegram = { ...(S.telegram || {}), state: "connecting" };
      updateTelegramPanel();
      await refreshTelegram();
    } else if (action === "disconnect") {
      await api("/channels/telegram/disconnect", { method: "POST" });
      await refreshTelegram();
      renderSettings();
    } else if (action === "logout") {
      confirmModal("Forget the bot token?", "Polling stops and the stored token is deleted. The bot itself stays live in Telegram — create a new token with @BotFather any time.", async () => {
        await api("/channels/telegram/logout", { method: "POST" });
        S.telegramSettings = { ...(S.telegramSettings || {}), tokenSet: false };
        await refreshTelegram();
        renderSettings();
        toast("Telegram token removed", "", "success");
      }, "Forget token");
    }
  } catch (e) {
    toast("Telegram action failed", e.message, "error");
    await refreshTelegram();
  }
}

// allowTgSender whitelists a blocked sender by numeric user id — never by
// username, which the owner can change at any time.
export async function allowTgSender(id) {
  if (!id) return;
  const current = (S.telegramSettings?.allowedUserIds || []).slice();
  if (!current.includes(id)) current.push(id);
  await saveTgSettings({ allowedUserIds: current });
}

// allowTgGroup whitelists the chat a blocked group message came from; the
// sender's user id would open their private chats, not that group. The server
// refuses a group entry next to the user wildcard, so explain instead of
// letting the save fail.
export async function allowTgGroup(chat) {
  if (!chat) return;
  if ((S.telegramSettings?.allowedUserIds || []).includes("*")) {
    toast("The * wildcard is active", "Remove the * user allowlist entry before allowing a group — it must be the only entry.", "info");
    return;
  }
  const current = (S.telegramSettings?.allowedGroupIds || []).slice();
  if (current.includes(chat)) return;
  if (await saveTgSettings({ allowedGroupIds: [...current, chat] })) {
    toast("Group added to allowlist", chat, "success");
  }
}

// allowTgEveryone turns on the user wildcard. The server requires it to stand
// alone, so when a group list exists the confirmation says it will be cleared
// and one save sends both changes.
export async function allowTgEveryone() {
  const clearGroups = (S.telegramSettings?.allowedGroupIds || []).length > 0;
  const save = async () => {
    if (await saveTgSettings({ allowedUserIds: ["*"], allowedGroupIds: [] })) {
      toast("Everyone allowed", clearGroups
        ? "The group allowlist was cleared so * stands alone."
        : "The wildcard * is now the only allowlist entry.", "success");
    }
  };
  if (!clearGroups) {
    await save();
    return;
  }
  confirmModal("Allow every sender?",
    "The wildcard * must be the only allowlist entry, so the group allowlist will be cleared too.",
    save, "Allow everyone");
}
