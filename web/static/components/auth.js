import { $, brandImage, esc, icon } from "./util.js";
import { S, composers, setSelectedProject, switchComposer } from "./state.js";
import { REQUEST_TIMEOUT_MS, api } from "./api.js";
import { closeModal } from "./modal.js";
import { closeWS } from "./ws.js";
import { startApp } from "./boot.js";
import { resetBrowserPane } from "./browser.js";
import { resetConvMeta } from "./convmeta.js";
import { resetSidebarState } from "./sidebar.js";

export function renderLogin(error = "") {
  S.booted = false;
  resetBrowserPane();
  document.title = "Sign in · NusaMation";
  $("#modal-root").innerHTML = "";
  $("#app").className = "";
  $("#app").innerHTML = `
    <div class="login">
      <div class="login-art">
        <div class="login-brand">${brandImage("brand-mark")}<span>NusaMation</span></div>
        <div class="login-copy">
          <h1>Your agent.<br>Everywhere.<br><em>One mesh.</em></h1>
          <p>ACP-native personal agents with full-fidelity web controls and a concise, human-sized presence on WhatsApp. Thoughts, tools, approvals and models — all in one place.</p>
        </div>
        <div class="login-points">
          <span><i class="dot"></i> Agent Client Protocol</span>
          <span><i class="dot"></i> WhatsApp + Web</span>
          <span><i class="dot"></i> Local-first</span>
        </div>
      </div>
      <div class="login-panel">
        <div class="login-card">
          <div class="login-mobile-brand">${brandImage("brand-mark")}<span>NusaMation</span></div>
          <h2>Welcome back</h2>
          <p class="lead">Sign in to your control center.</p>
          ${error ? `<div class="login-error">${icon("alert", "sm")}<span>${esc(error)}</span></div>` : ""}
          ${!S.authInitialized ? `<div class="login-error">${icon("info", "sm")}<span>First-run setup has not completed. Restart NusaMation and check the terminal for the bootstrap password.</span></div>` : ""}
          <form data-form="login" autocomplete="on">
            <div class="field">
              <label for="login-password">Password</label>
              <div class="password-wrap">
                <input class="input" id="login-password" name="password" type="password" placeholder="Your admin password" autocomplete="current-password" autofocus required>
                <button class="password-toggle" type="button" data-action="toggle-password" aria-label="Show password">${icon("key", "sm")}</button>
              </div>
            </div>
            <div class="login-row">
              <label class="checkbox"><input type="checkbox" name="remember" checked> Keep me signed in</label>
            </div>
            <button class="btn btn-primary btn-block" type="submit" data-login-submit>Sign in ${icon("chevron-right", "sm")}</button>
          </form>
          <div class="login-foot">NusaMation${S.appVersion ? ` ${esc(S.appVersion)}` : ""}</div>
        </div>
      </div>
    </div>`;
}

export async function doLogin(form) {
  const btn = form.querySelector("[data-login-submit]");
  const pw = form.querySelector("[name=password]").value;
  const remember = form.querySelector("[name=remember]").checked;
  if (!pw) return;
  btn.disabled = true;
  btn.innerHTML = `<span class="spinner"></span> Signing in…`;
  try {
    const data = await api("/auth/login", { method: "POST", body: { password: pw, remember }, noAuthRedirect: true, timeout: REQUEST_TIMEOUT_MS });
    S.csrf = data.csrf;
    await startApp();
  } catch (e) {
    btn.disabled = false;
    btn.innerHTML = `Sign in ${icon("chevron-right", "sm")}`;
    const card = form.closest(".login-card");
    let box = card.querySelector(".login-error");
    if (!box) {
      box = document.createElement("div");
      box.className = "login-error";
      card.querySelector("form").before(box);
    }
    box.innerHTML = `${icon("alert", "sm")}<span>${esc(e.message || "Sign in failed.")}</span>`;
    form.querySelector("[name=password]").select();
  }
}

export async function doLogout() {
  try { await api("/auth/logout", { method: "POST" }); } catch {}
  closeModal(); closeWS();
  S.csrf = ""; S.current = null; S.messages = []; S.conversations = []; S.approvals = []; S.approvalsById.clear();
  S.projects = []; S.expandedProjects = new Set(); setSelectedProject(""); resetSidebarState();
  composers.clear(); resetConvMeta(); switchComposer("");
  S.sendDelivery = "steer"; S.turns = {};
  renderLogin();
}
