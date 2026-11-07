import { $, agentById, agentName, esc, icon } from "./util.js";
import { S } from "./state.js";
import { api } from "./api.js";
import { toast } from "./toast.js";
import { renderSettings, settingsHead } from "./settings.js";

export function automationDate(ms, zone) {
  if (!ms) return "";
  try {
    const parts = Object.fromEntries(new Intl.DateTimeFormat("en-CA", { timeZone: zone, year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hourCycle: "h23" }).formatToParts(new Date(ms)).map((p) => [p.type, p.value]));
    return `${parts.year}-${parts.month}-${parts.day}T${parts.hour}:${parts.minute}`;
  } catch { return ""; }
}

export function displayAutomationTime(ms, zone) {
  try { return new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short", timeZone: zone === "Local" ? S.automationServerZone : zone }).format(new Date(ms)); }
  catch { return new Date(ms).toLocaleString(); }
}

export function automationTimestamp(value, zone) {
  if (!value) return 0;
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(value);
  if (!match) throw new Error("Enter a valid date and time.");
  const wanted = Date.UTC(...[Number(match[1]), Number(match[2]) - 1, Number(match[3]), Number(match[4]), Number(match[5])]);
  let result = wanted;
  for (let i = 0; i < 3; i++) {
    const actual = automationDate(result, zone);
    if (!actual) throw new Error("Enter a valid IANA timezone, for example Asia/Jakarta.");
    const p = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/.exec(actual);
    result += wanted - Date.UTC(Number(p[1]), Number(p[2]) - 1, Number(p[3]), Number(p[4]), Number(p[5]));
  }
  if (automationDate(result, zone) !== value) throw new Error("This local time does not exist in the selected timezone.");
  return result;
}

export function automationsSettings() {
  if (S.automationLoading) return `${settingsHead("Automations", "Scheduled ACP jobs.")}<div class="settings-desc"><span class="spinner"></span> Loading…</div>`;
  if (S.automationError) return `${settingsHead("Automations", "Scheduled ACP jobs.")}<div role="alert">${esc(S.automationError)}</div><button class="btn btn-secondary" data-action="automations-list">Retry</button>`;
  if (S.automationView === "form") return automationFormHTML();
  if (S.automationView === "history") {
    const job = S.automationJob;
    return `${settingsHead(esc(job.name), "Run history", `<button class="btn btn-secondary btn-sm" data-action="automations-list">Back</button>`)}
      <div class="settings-desc">${esc(job.prompt)}</div>
      <div class="settings-section">${S.automationRuns.map((r) => `<div class="settings-row"><div><div class="label">${esc(r.status)} · ${esc(displayAutomationTime(r.startedAt, job.timezone))}</div><div class="desc">Scheduled ${esc(displayAutomationTime(r.scheduledAt, job.timezone))} (${esc(job.timezone)})${r.error ? " · " + esc(r.error) : ""}</div></div>${r.conversationId ? `<button class="btn btn-secondary btn-sm" data-action="automation-room" data-id="${esc(r.conversationId)}">Open chat</button>` : ""}</div>`).join("") || `<div class="empty-settings">No runs yet.</div>`}</div>`;
  }
  return `${settingsHead("Automations", "Run prompts on a schedule.", `<button class="btn btn-primary btn-sm" data-action="automation-add">${icon("plus", "sm")} Add automation</button>`)}
    <div class="agent-list">${S.automations.map((a) => {
      const state = !a.enabled ? "Paused" : a.expiresAt && a.expiresAt < Date.now() ? "Expired" : !a.nextRunAt ? "Dispatched" : "Active";
      const next = a.nextRunAt && state === "Active" ? ` · Next ${displayAutomationTime(a.nextRunAt, a.timezone)}` : "";
      return `<div class="agent-card"><div class="agent-card-main"><div class="agent-card-name">${esc(a.name)} <span class="agent-status">${state}</span></div><div class="agent-card-sub">${esc(agentName(agentById(a.agentId)))} · ${a.schedule === "cron" ? esc(a.cron) + " · " + esc(a.timezone) : "Once"}${esc(next)}</div><div class="agent-card-sub">${a.sessionMode === "same" ? "Dedicated chat" : "New chat each run"}</div></div><div class="agent-card-actions"><button class="btn btn-secondary btn-sm" data-action="automation-history" data-id="${esc(a.id)}">History</button><button class="btn btn-secondary btn-sm" data-action="automation-edit" data-id="${esc(a.id)}">Edit</button><button class="btn btn-secondary btn-sm" data-action="automation-toggle" data-id="${esc(a.id)}">${a.enabled ? "Pause" : "Resume"}</button><button class="icon-btn sm" data-action="automation-delete" data-id="${esc(a.id)}" title="Delete" aria-label="Delete automation">${icon("trash", "sm")}</button></div></div>`;
    }).join("") || `<div class="empty-settings">No automations yet.</div>`}</div>`;
}

export function automationFormHTML() {
  const a = S.automationJob || {};
  const zone = a.timezone && a.timezone !== "Local" ? a.timezone : (S.automationServerZone && S.automationServerZone !== "Local" ? S.automationServerZone : (Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC"));
  const agent = a.agentId || S.agentDefaults.agentId || S.agents.find((x) => x.enabled)?.id || "";
  return `${settingsHead(a.id ? "Edit automation" : "New automation", "Times use the selected timezone.", `<button class="btn btn-secondary btn-sm" data-action="automations-list">Cancel</button>`)}
    <form data-form="automation" data-id="${esc(a.id || "")}"><div class="form-grid">
      <div class="field full"><label>Name</label><input class="input" name="name" value="${esc(a.name || "")}" required></div>
      <div class="field full"><label>Prompt</label><textarea class="textarea" name="prompt" rows="4" required>${esc(a.prompt || "")}</textarea></div>
      <div class="field"><label>ACP agent</label><select class="select" name="agentId" required>${S.agents.filter((x) => x.enabled).map((x) => `<option value="${esc(x.id)}" ${x.id === agent ? "selected" : ""}>${esc(x.name)}</option>`).join("")}</select></div>
      <div class="field"><label>Session</label><select class="select" name="sessionMode"><option value="isolated" ${a.sessionMode !== "same" ? "selected" : ""}>New chat each run</option><option value="same" ${a.sessionMode === "same" ? "selected" : ""}>Reuse dedicated chat</option></select></div>
      <div class="field"><label>Schedule</label><select class="select" name="schedule" data-change="automation-schedule"><option value="once" ${a.schedule !== "cron" ? "selected" : ""}>Run once</option><option value="cron" ${a.schedule === "cron" ? "selected" : ""}>Repeat (cron)</option></select></div>
      <div class="field"><label>Timezone</label><input class="input" name="timezone" value="${esc(zone)}" required></div>
      <div class="field full" data-automation-once ${a.schedule === "cron" ? "hidden" : ""}><label>Run at</label><input class="input" type="datetime-local" name="runAt" value="${esc(automationDate(a.runAt, zone))}"></div>
      <div class="field full" data-automation-cron ${a.schedule === "cron" ? "" : "hidden"}><label>Schedule builder</label><select class="select" data-change="automation-preset"><option value="">Custom expression</option><option value="minutes">Every N minutes</option><option value="hours">Every N hours</option><option value="daily">Every day</option><option value="weekly">Every week</option><option value="monthly">Every month</option></select>
        <div class="automation-builder" hidden><label data-automation-interval>Interval <input class="input" type="number" min="1" max="59" value="5" data-automation-build="interval"></label><label data-automation-time>Time <input class="input" type="time" value="09:00" data-automation-build="time"></label><label data-automation-weekday>Day <select class="select" data-automation-build="weekday"><option value="1">Monday</option><option value="2">Tuesday</option><option value="3">Wednesday</option><option value="4">Thursday</option><option value="5">Friday</option><option value="6">Saturday</option><option value="0">Sunday</option></select></label><label data-automation-monthday>Day of month <input class="input" type="number" min="1" max="31" value="1" data-automation-build="monthday"></label></div>
        <label style="margin-top:10px">Cron expression</label><input class="input mono" name="cron" value="${esc(a.cron || "0 9 * * *")}" placeholder="0 9 * * *" data-input="automation-cron"><small class="muted">minute · hour · day of month · month · day of week</small></div>
      <div class="field full"><label>Expires at <span class="muted">(optional)</span></label><input class="input" type="datetime-local" name="expiresAt" value="${esc(automationDate(a.expiresAt, zone))}"></div>
    </div><div class="settings-desc" style="margin-top:12px">Missed schedules run once when the server comes back online. No run starts after expiry.</div><div class="form-actions automation-actions"><button class="btn btn-secondary" type="button" data-action="automations-list">Cancel</button><button class="btn btn-primary" type="submit">Save automation</button></div></form>`;
}

export async function loadAutomations() {
  S.automationView = "list"; S.automationLoading = true; S.automationError = ""; renderSettings();
  try { const d = await api("/automations"); S.automations = d.automations || []; S.automationServerZone = d.timezone || ""; }
  catch (e) { S.automationError = e.message; }
  finally { S.automationLoading = false; if (S.modal?.type === "settings" && S.settingsTab === "automations") renderSettings(); }
}

export function updateAutomationBuilder(form) {
  const preset = $('[data-change="automation-preset"]', form).value;
  const box = $(".automation-builder", form); box.hidden = !preset;
  const visible = (selector, show) => { $(selector, box).hidden = !show; };
  visible("[data-automation-interval]", ["minutes", "hours"].includes(preset));
  visible("[data-automation-time]", ["daily", "weekly", "monthly"].includes(preset));
  visible("[data-automation-weekday]", preset === "weekly"); visible("[data-automation-monthday]", preset === "monthly");
  if (!preset) return;
  const interval = $('[data-automation-build="interval"]', box); interval.max = preset === "hours" ? "23" : "59";
  const n = Math.max(1, Math.min(Number(interval.max), Number(interval.value) || 1)); interval.value = String(n);
  const [hour, minute] = ($('[data-automation-build="time"]', box).value || "09:00").split(":").map(Number);
  const weekday = $('[data-automation-build="weekday"]', box).value;
  const dayInput = $('[data-automation-build="monthday"]', box); const day = Math.max(1, Math.min(31, Number(dayInput.value) || 1)); dayInput.value = String(day);
  const expressions = { minutes: `*/${n} * * * *`, hours: `0 */${n} * * *`, daily: `${minute} ${hour} * * *`, weekly: `${minute} ${hour} * * ${weekday}`, monthly: `${minute} ${hour} ${day} * *` };
  $('[name="cron"]', form).value = expressions[preset];
}

export async function saveAutomation(form) {
  const fd = new FormData(form); const id = form.dataset.id; const zone = String(fd.get("timezone") || "").trim();
  try {
    const body = { name: String(fd.get("name") || ""), prompt: String(fd.get("prompt") || ""), agentId: String(fd.get("agentId") || ""), schedule: String(fd.get("schedule") || "once"), cron: String(fd.get("cron") || ""), runAt: 0, timezone: zone, sessionMode: String(fd.get("sessionMode") || "isolated"), expiresAt: automationTimestamp(String(fd.get("expiresAt") || ""), zone), enabled: id ? !!S.automationJob.enabled : true, updatedAt: id ? S.automationJob.updatedAt : 0 };
    if (body.schedule === "once") body.runAt = automationTimestamp(String(fd.get("runAt") || ""), zone);
    const btn = form.querySelector('[type="submit"]'); btn.disabled = true;
    try { await api(id ? `/automations/${encodeURIComponent(id)}` : "/automations", { method: id ? "PUT" : "POST", body }); }
    finally { btn.disabled = false; }
    toast("Automation saved", "The scheduler will pick up the change.", "success");
    if (S.modal?.type === "settings" && S.settingsTab === "automations") await loadAutomations();
  } catch (e) { toast("Could not save automation", e.message, "error", 8000); }
}
