import { $, esc, icon } from "./util.js";
import { S } from "./state.js";
import { api } from "./api.js";
import { toast } from "./toast.js";
import { closeModal, confirmModal } from "./modal.js";
import { renderSidebar } from "./sidebar.js";
import { channelCards, openSettings, renderSettings } from "./settings.js";

export function whatsappPanel() {
  const ch = S.whatsapp || S.channels.find((c) => c.name === "whatsapp");
  if (!ch) {
    return `<div class="empty-settings">WhatsApp is disabled for this process.<br>Remove <code class="mono">NUSAMATION_NO_WHATSAPP</code> and restart to enable it.</div>`;
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
      <div class="settings-row"><div><div class="label">Group policy</div><div class="desc">Mention requires a direct @mention or a reply to the bot.</div></div><select class="select" style="width:150px" data-change="wa-group"><option value="mention" ${settings.groupPolicy === "mention" ? "selected" : ""}>mention only</option><option value="all" ${settings.groupPolicy === "all" ? "selected" : ""}>all messages</option><option value="disabled" ${settings.groupPolicy === "disabled" ? "selected" : ""}>disabled</option></select></div>
    </div>
    <div class="settings-section">
      <div class="settings-title">Allowlist</div>
      <div class="settings-desc">One phone number or JID per line. Empty means anyone who can message this account. Groups need the group JID or a participant number.</div>
      <textarea class="textarea allowlist" id="wa-allowlist" placeholder="+628123456789&#10;1203630...@g.us">${esc((settings.allowedNumbers || []).join("\n"))}</textarea>
      <div class="form-actions" style="justify-content:flex-start"><button class="btn btn-secondary btn-sm" data-action="wa-save-allowlist">Save allowlist</button></div>
    </div>`;
}

export function startQRTimer() {
  if (S.qrTimer) clearInterval(S.qrTimer);
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
  if (S.modal?.type !== "settings" || S.settingsTab !== "channels") return;
  const cards = $("#channel-cards");
  if (cards) cards.innerHTML = channelCards();
  const el = $("#wa-panel");
  if (!el) return;
  const active = document.activeElement;
  if (active && (active.id === "wa-allowlist")) return;
  el.innerHTML = whatsappPanel();
}

export async function saveWaSettings(patch) {
  const current = S.whatsappSettings || { enabled: true, autoStart: true, groupPolicy: "mention", readReceipts: true, sendTyping: true, allowedNumbers: [] };
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
  } catch (e) {
    toast("Could not save WhatsApp settings", e.message, "error");
    renderSettings();
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
        closeModal();
        openSettings("channels");
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
    if (S.modal?.type === "settings" && S.settingsTab === "channels") {
      const cards = $("#channel-cards");
      if (cards) cards.innerHTML = channelCards();
    }
  } catch (e) { console.warn(e); }
}
