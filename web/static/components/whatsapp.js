import { $, esc, icon } from "./util.js";
import { S } from "./state.js";
import { api } from "./api.js";
import { toast } from "./toast.js";
import { confirmModal } from "./modal.js";
import { renderSidebar } from "./sidebar.js";
import { channelCards, renderSettings } from "./settings.js";

export function whatsappPanel() {
  const ch = S.whatsapp || S.channels.find((c) => c.name === "whatsapp");
  if (!ch) {
    if (S.bootstrap?.app?.whatsappDisabled) {
      return `<div class="empty-settings">WhatsApp is disabled for this process.<br>Remove <code class="mono">NUSAMATION_NO_WHATSAPP</code> and restart to enable it.</div>`;
    }
    return `<div class="empty-settings">WhatsApp failed to start for this process.<br>Check the server log for the startup error — the channel panel only appears once the WhatsApp database can be opened.</div>`;
  }
  const settings = S.whatsappSettings || { enabled: ch.enabled, autoStart: true, groupPolicy: "mention", readReceipts: true, sendTyping: true, allowedNumbers: [] };
  const paired = ch.stats && ch.stats.paired;
  const pairing = ch.state === "pairing";
  const connecting = ch.state === "connecting";
  const qr = pairing || connecting;
  const statusBadge = ch.loggedIn ? `<span class="badge-soft green">connected</span>` : pairing ? `<span class="badge-soft amber">pairing</span>` : connecting ? `<span class="badge-soft amber">connecting</span>` : ch.state === "error" ? `<span class="badge-soft red">error</span>` : `<span class="badge-soft gray">${esc(ch.state || "offline")}</span>`;
  return `<div class="settings-title" style="display:flex;align-items:center;gap:8px">WhatsApp ${statusBadge}<span class="grow"></span>
    ${pairing || connecting ? `<button class="btn btn-ghost btn-xs" data-action="refresh-channels">${icon("refresh", "sm")} Refresh</button>` : ""}
    <button class="btn btn-secondary btn-xs" data-action="wa-connect" ${ch.connected ? "disabled" : ""}>Connect</button>
    <button class="btn btn-ghost btn-xs" data-action="wa-disconnect" ${!ch.connected && ch.state !== "pairing" ? "disabled" : ""}>Disconnect</button>
    <button class="btn btn-danger btn-xs" data-action="wa-logout" ${!paired ? "disabled" : ""}>Unlink</button></div>
    <div class="settings-desc" style="margin:6px 0 14px">${esc(ch.detail || (paired ? "Linked and ready." : "Link this device to let NusaMation answer WhatsApp messages."))}</div>
    ${qr ? (pairing ? `<div class="qr-box"><img class="qr-img" src="/api/channels/whatsapp/qr?t=${Date.now()}" alt="WhatsApp pairing QR"><div class="qr-info"><h4>Scan this code</h4><p>Open WhatsApp on your phone → Linked devices → Link a device. The QR refreshes automatically.</p><div class="muted" style="font-size:10.5px">The web control center always receives the full stream. WhatsApp receives one message when the turn finishes. Thoughts stay on the web.</div></div></div>` : `<div class="qr-box"><div class="qr-placeholder"><span class="spinner" style="margin-bottom:8px"></span>Preparing a fresh QR code…</div><div class="qr-info"><h4>Connecting</h4><p>NusaMation is opening a secure WhatsApp web session. A QR code will appear in a moment.</p></div></div>`) : (ch.loggedIn || paired) ? `<div class="qr-box"><div class="tool-icon kind-execute" style="width:44px;height:44px">${icon("message")}</div><div class="grow"><h4 style="margin:0 0 4px">${ch.selfID ? esc(ch.selfID) : "WhatsApp linked"}</h4><p style="margin:0;color:var(--muted);font-size:11.5px">Inbound messages are gatewayed through the routing layer. The agent's full thoughts stay on web.</p></div></div>` : `<div class="qr-box"><div class="tool-icon" style="width:44px;height:44px">${icon("message")}</div><div class="grow"><h4 style="margin:0 0 4px">Not linked</h4><p style="margin:0;color:var(--muted);font-size:11.5px">Press Connect to generate a pairing QR. Already linked? Press Connect to resume.</p></div></div>`}
    <div class="settings-section" style="margin-top:20px">
      <div class="settings-title">Behaviour</div>
      <div class="settings-row"><div><div class="label">Enabled</div><div class="desc">Accept inbound messages and send replies.</div></div><button role="switch" aria-checked="${settings.enabled}" class="switch ${settings.enabled ? "on" : ""}" data-action="wa-toggle" data-key="enabled"></button></div>
      <div class="settings-row"><div><div class="label">Auto-connect on boot</div><div class="desc">Only when a device is already linked.</div></div><button role="switch" aria-checked="${settings.autoStart}" class="switch ${settings.autoStart ? "on" : ""}" data-action="wa-toggle" data-key="autoStart"></button></div>
      <div class="settings-row"><div><div class="label">Read receipts</div><div class="desc">Mark allowed messages as read.</div></div><button role="switch" aria-checked="${settings.readReceipts}" class="switch ${settings.readReceipts ? "on" : ""}" data-action="wa-toggle" data-key="readReceipts"></button></div>
      <div class="settings-row"><div><div class="label">Typing indicator</div><div class="desc">Show composing while the agent works.</div></div><button role="switch" aria-checked="${settings.sendTyping}" class="switch ${settings.sendTyping ? "on" : ""}" data-action="wa-toggle" data-key="sendTyping"></button></div>
      <div class="settings-row"><div><div class="label">Send pacing</div><div class="desc">Minimum seconds between two messages to the same chat. Extra messages wait in a queue — 0 disables pacing.</div></div><input class="input" style="width:90px" type="number" min="0" max="60" data-change="wa-number" data-key="sendPacing" value="${settings.sendPacing ?? 5}"></div>
      <div class="settings-row"><div><div class="label">Queue limit</div><div class="desc">Maximum queued messages per chat. Beyond it, the send is refused — the only path that drops.</div></div><input class="input" style="width:90px" type="number" min="1" max="200" data-change="wa-number" data-key="queueCap" value="${settings.queueCap ?? 20}"></div>
      <div class="settings-row"><div><div class="label">Group policy</div><div class="desc">Mention requires a direct @mention or a reply to the bot.</div></div><select class="select" style="width:150px" data-change="wa-group"><option value="mention" ${settings.groupPolicy === "mention" ? "selected" : ""}>mention only</option><option value="all" ${settings.groupPolicy === "all" ? "selected" : ""}>all messages</option><option value="disabled" ${settings.groupPolicy === "disabled" ? "selected" : ""}>disabled</option></select></div>
    </div>
    <div class="settings-section">
      <div class="settings-title">Allowlist</div>
      <div class="settings-desc">One phone number per line. An empty allowlist allows <em>no one</em> — a message only gets a reply once you add a number or <code class="mono">*</code>. The wildcard <code class="mono">*</code> allows everyone and must be the only entry.</div>
      <textarea class="textarea allowlist" id="wa-allowlist" placeholder="+628123456789">${esc((settings.allowedNumbers || []).join("\n"))}</textarea>
      <div class="form-actions" style="justify-content:flex-start"><button class="btn btn-secondary btn-sm" data-action="wa-save-allowlist">Save allowlist</button><button class="btn btn-ghost btn-sm" data-action="wa-allow-everyone">Allow everyone (*)</button></div>
    </div>
    ${groupsSection(settings)}
    ${blockedSection(ch)}`;
}

// groupsSection is the group allowlist as a picker: a group has no identity
// an operator can type from memory like a phone number, so groups are added
// and removed from the list of groups this account has joined. Every toggle
// saves immediately — there is no textarea step and no JID copying.
function groupsSection(settings) {
  const stored = settings.allowedGroups || [];
  const known = Array.isArray(S.waGroups) ? S.waGroups : null;
  const loading = !!S.waGroupsLoading;
  const nameOf = (jid) => (known ? (known.find((g) => g.jid === jid) || {}).name : "") || "";
  let rows = "";
  if (known) {
    const storedSet = new Set(stored);
    const left = new Set(stored);
    rows = known.map((g) => {
      left.delete(g.jid);
      const on = storedSet.has(g.jid);
      return `<div class="settings-row"><div><div class="label">${esc(g.name || "Group")}</div><div class="desc mono">${esc(g.jid)}${g.participants ? " · " + esc(String(g.participants)) + " members" : ""}</div></div><button class="btn ${on ? "btn-ghost" : "btn-secondary"} btn-sm" data-action="wa-group-toggle" data-jid="${esc(g.jid)}" data-name="${esc(g.name || "")}">${on ? "Remove ✓" : "Add"}</button></div>`;
    }).join("");
    // Groups that were allowed but left since still show, so the list
    // stays honest about what is whitelisted.
    for (const jid of left) {
      rows += `<div class="settings-row"><div><div class="label">${esc(nameOf(jid) || "Left group")}</div><div class="desc mono">${esc(jid)} · not joined anymore</div></div><button class="btn btn-ghost btn-sm" data-action="wa-group-toggle" data-jid="${esc(jid)}" data-name="">Remove</button></div>`;
    }
    if (!known.length) rows = `<div class="settings-desc">No groups joined on this account.</div>`;
  } else if (stored.length) {
    rows = stored.map((jid) => `<div class="settings-row"><div><div class="label">${esc(nameOf(jid) || "Group")}</div><div class="desc mono">${esc(jid)}</div></div><button class="btn btn-ghost btn-sm" data-action="wa-group-toggle" data-jid="${esc(jid)}" data-name="">Remove</button></div>`).join("");
  }
  const note = known ? "" : `${stored.length ? "Connect and load your joined groups to see their names." : "Load your joined groups to pick the ones the bot may answer in."}`;
  return `<div class="settings-section">
    <div class="settings-title" style="display:flex;align-items:center;gap:8px">Group allowlist <span class="grow"></span><button class="btn btn-ghost btn-xs" data-action="wa-load-groups" ${loading ? "disabled" : ""}>${icon("refresh", "sm")} ${loading ? "Loading…" : "Load groups"}</button></div>
    <div class="settings-desc">Groups are picked, not typed — a group has no number you can enter here. The group policy still decides when the bot replies inside an allowed group.</div>
    ${rows}
    ${note ? `<div class="settings-desc">${note}</div>` : ""}
  </div>`;
}

// blockedSection lists recent senders the allowlist rejected, each with a
// one-tap allow button that appends the normalized number to the allowlist.
// Group traffic gets an Allow-group action too: allowing the member's number
// would only whitelist their private chats, not the group they wrote in.
function blockedSection(ch) {
  const blocked = ch?.stats?.blocked || [];
  if (!blocked.length) return "";
  const allowed = new Set((S.whatsappSettings?.allowedNumbers || []).map((n) => String(n).replace(/\D/g, "")));
  const groupsAllowed = new Set(S.whatsappSettings?.allowedGroups || []);
  return `<div class="settings-section">
    <div class="settings-title">Blocked senders <span class="muted" style="font-weight:400">(${blocked.length})</span></div>
    <div class="settings-desc">Messages these senders were ignored. Allowing a number opens their private chat; for group traffic allow the group itself.</div>
    ${blocked.map((b) => {
      const group = b.chat && String(b.chat).endsWith("@g.us") ? b.chat : "";
      const groupOn = group && groupsAllowed.has(group);
      const senderOn = allowed.has(String(b.sender).replace(/\D/g, ""));
      const groupBtn = group ? `<button class="btn ${groupOn ? "btn-ghost" : "btn-secondary"} btn-sm" data-action="wa-allow-group" data-jid="${esc(group)}" ${groupOn ? "disabled" : ""}>${groupOn ? "Group allowed" : "Allow group"}</button>` : "";
      return `<div class="settings-row"><div><div class="label">${esc(b.pushName || b.sender)}</div><div class="desc mono">${esc(b.chat || b.sender)}${b.pushName ? " · " + esc(b.sender) : ""}</div></div><div style="display:flex;gap:6px">${groupBtn}<button class="btn ${senderOn ? "btn-ghost" : "btn-secondary"} btn-sm" data-action="wa-allow-sender" data-sender="${esc(b.sender)}" ${senderOn ? "disabled" : ""}>${senderOn ? "Allowed" : "Allow"}</button></div></div>`;
    }).join("")}
  </div>`;
}

export function startQRTimer() {
  if (S.qrTimer) clearInterval(S.qrTimer);
  S.qrTimer = null;
  // The poller lives only while the WhatsApp screen is actually visible —
  // never on the channels list or while the chat is showing.
  if (S.view !== "settings" || S.settingsTab !== "channels" || S.channel !== "whatsapp") return;
  const ch = S.whatsapp || S.channels.find((c) => c.name === "whatsapp");
  if (!ch || !["pairing", "connecting"].includes(ch.state)) return;
  S.qrTimer = setInterval(async () => {
    try {
      const d = await api("/channels");
      S.channels = d.channels || [];
      const wa = S.channels.find((c) => c.name === "whatsapp");
      if (wa) S.whatsapp = wa;
      updateWhatsAppPanel();
      if (!wa || !["pairing", "connecting"].includes(wa.state)) {
        clearInterval(S.qrTimer); S.qrTimer = null;
        if (wa?.connected) toast("WhatsApp connected", "The channel is live.", "success");
      }
    } catch {}
  }, 2600);
}

export function updateWhatsAppPanel() {
  if (S.view !== "settings" || S.settingsTab !== "channels") return;
  if (!S.channel) {
    const cards = $("#channel-cards");
    if (cards) cards.innerHTML = channelCards();
    return;
  }
  if (S.channel !== "whatsapp") return;
  const el = $("#wa-panel");
  if (!el) return;
  const active = document.activeElement;
  if (active && (active.id === "wa-allowlist" || (typeof active.closest === "function" && active.closest("#wa-panel") && active.matches("input,textarea,select")))) return;
  el.innerHTML = whatsappPanel();
}

export async function saveWaSettings(patch) {  const current = S.whatsappSettings || { enabled: true, autoStart: true, groupPolicy: "mention", readReceipts: true, sendTyping: true, allowedNumbers: [] };
  const next = { ...current, ...patch };
  try {
    const d = await api("/channels/whatsapp/settings", { method: "PUT", body: next });
    S.whatsappSettings = d.settings;
    if (d.channel) {
      const i = S.channels.findIndex((c) => c.name === "whatsapp");
      if (i >= 0) S.channels[i] = d.channel; else S.channels.push(d.channel);
      S.whatsapp = d.channel;
    }
    updateWhatsAppPanel();
    return true;
  } catch (e) {
    toast("Could not save WhatsApp settings", e.message, "error");
    renderSettings();
    return false;
  }
}

export async function waAction(action) {
  try {
    if (action === "connect") {
      await api("/channels/whatsapp/connect", { method: "POST" });
      toast("Connecting WhatsApp", "A QR code will appear in a moment if pairing is needed.", "info");
      S.whatsapp = { ...(S.whatsapp || {}), state: "connecting" };
      renderSettings();
      startQRTimer();
    } else if (action === "disconnect") {
      await api("/channels/whatsapp/disconnect", { method: "POST" });
      await refreshChannels();
      renderSettings();
    } else if (action === "logout") {
      confirmModal("Unlink WhatsApp?", "This removes the linked device from NusaMation. Your WhatsApp account is not deleted.", async () => {
        await api("/channels/whatsapp/logout", { method: "POST" });
        await refreshChannels();
        renderSettings();
        toast("WhatsApp unlinked", "", "success");
      }, "Unlink");
    }
  } catch (e) {
    toast("WhatsApp action failed", e.message, "error");
    await refreshChannels();
  }
}

export async function refreshChannels() {
  try {
    const d = await api("/channels");
    S.channels = d.channels || [];
    const wa = S.channels.find((c) => c.name === "whatsapp");
    if (wa) S.whatsapp = wa;
    renderSidebar();
    updateWhatsAppPanel();
  } catch (e) { console.warn(e); }
}

// loadWaGroups refreshes the joined-group list the picker renders from; the
// section itself re-renders from S.waGroups, so no DOM is patched by hand.
export async function loadWaGroups() {
  S.waGroupsLoading = true;
  updateWhatsAppPanel();
  try {
    const d = await api("/channels/whatsapp/groups");
    S.waGroups = d.groups || [];
    S.waGroupsLoading = false;
    updateWhatsAppPanel();
  } catch (e) {
    S.waGroupsLoading = false;
    updateWhatsAppPanel();
    toast("Could not load groups", e.message, "error");
  }
}

// ensureWaGroups loads the joined list once per session when the account is
// linked, so the picker is ready the moment the channels tab opens. A silent
// skip keeps the stored groups renderable when offline.
export async function ensureWaGroups() {
  const ch = S.whatsapp;
  if (S.view !== "settings" || S.settingsTab !== "channels" || S.channel !== "whatsapp") return;
  if (!ch?.loggedIn || S.waGroupsTried || S.waGroupsLoading || Array.isArray(S.waGroups)) return;
  S.waGroupsTried = true;
  await loadWaGroups();
}

// toggleWaGroup adds or removes one group from the group allowlist and saves
// immediately — the picker has no separate save step.
export async function toggleWaGroup(jid, name) {
  if (!jid) return;
  const cur = (S.whatsappSettings?.allowedGroups || []).slice();
  const has = cur.includes(jid);
  const next = has ? cur.filter((g) => g !== jid) : [...cur, jid];
  if (await saveWaSettings({ allowedGroups: next })) {
    toast(has ? "Group removed from allowlist" : "Group added to allowlist", name || jid, "success");
  }
}

// allowWaGroup whitelists the group a blocked message came from; the sender's
// number would be the wrong object for group traffic.
export async function allowWaGroup(jid) {
  if (!jid || !String(jid).endsWith("@g.us")) return;
  const cur = (S.whatsappSettings?.allowedGroups || []).slice();
  if (cur.includes(jid)) return;
  if (await saveWaSettings({ allowedGroups: [...cur, jid] })) {
    toast("Group added to allowlist", jid, "success");
  }
}

// allowWaSender appends a blocked sender to the allowlist and saves.
export async function allowWaSender(sender) {
  if (!sender) return;
  const current = (S.whatsappSettings?.allowedNumbers || []).slice();
  if (!current.includes(sender)) current.push(sender);
  await saveWaSettings({ allowedNumbers: current });
}
