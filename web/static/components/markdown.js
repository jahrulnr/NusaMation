import { $, $$, esc, icon } from "./util.js?v=35";

/* ---------------- markdown ---------------- */
export const markdownCache = new Map();

// CommonMark lazy continuation glues a top-level line following a list into
// the last <li>. In chat the line is its own paragraph, so close the list
// with a blank line before it reaches marked.
export function relaxListEndings(src) {
  const quoteIndent = /^((?:[ \t]*>[ \t]*)*)([ \t]*)/;
  const bullet = /^(?:[-*+]|\d{1,9}[.)])(?=[ \t]|$)/;
  const lines = src.split("\n");
  const out = [];
  let fence = "", inList = false;
  for (const line of lines) {
    const trimmed = line.trim();
    if (fence) {
      if (trimmed.startsWith(fence)) fence = "";
      out.push(line);
      continue;
    }
    if (trimmed.startsWith("```") || trimmed.startsWith("~~~")) {
      fence = trimmed.slice(0, 3);
      inList = false;
      out.push(line);
      continue;
    }
    const m = line.match(quoteIndent);
    const quote = m[1], indent = m[2];
    const content = line.slice(quote.length + indent.length);
    if (!content.trim()) {
      inList = false;
      out.push(line);
      continue;
    }
    if (indent.length <= 3 && bullet.test(content)) {
      inList = true;
      out.push(line);
      continue;
    }
    if (inList && indent.length === 0) out.push(quote.trimEnd());
    if (indent.length === 0) inList = false;
    out.push(line);
  }
  return out.join("\n");
}

export function renderMarkdown(text, scope = "preview", format = "markdown") {
  const source = relaxListEndings(String(text || ""));
  const cached = markdownCache.get(scope);
  if (cached?.source === source && cached.format === format) return cached.html;
  if (window.marked && window.DOMPurify) {
    try {
      const parser = new window.marked.Marked({ gfm: true, breaks: true });
      if (window.NusaMarkdown) parser.use(window.NusaMarkdown.latex(), window.NusaMarkdown.math(), window.NusaMarkdown.markedFootnote({ prefixId: "footnote-" + scope.replace(/[^a-zA-Z0-9_-]/g, "-") + "-" }), window.NusaMarkdown.emoji());
      if (format === "whatsapp") parser.use({
        walkTokens(token) {
          if (token.type === "em" && token.raw.startsWith("*")) token.type = "strong";
        },
        extensions: [{
          name: "whatsappMonospaceBlock", level: "block",
          tokenizer(src) {
            const match = /^```([^\n`]*)\n((?:(?!```)[\s\S])*?)```(?=[ \t]*(?:\n|$))/.exec(src);
            if (match && !/\n[ \t]*$/.test(match[2])) return { type: "whatsappMonospaceBlock", raw: match[0], text: (match[1] ? match[1] + "\n" : "") + match[2] };
          },
          renderer(token) { return `<pre><code>${esc(token.text)}</code></pre>`; },
        }, {
          name: "whatsappMonospaceInline", level: "inline",
          start(src) { return src.indexOf("```"); },
          tokenizer(src) {
            const match = /^```([^`]*\n[^`]*?)```/.exec(src);
            if (match) return { type: "whatsappMonospaceInline", raw: match[0], text: match[1] };
          },
          renderer(token) { return `<code class="whatsapp-monospace">${esc(token.text)}</code>`; },
        }],
      });
      let html = parser.parse(source);
      html = window.DOMPurify.sanitize(html, { ADD_ATTR: ["target", "rel"] });
      if (!markdownCache.has(scope) && markdownCache.size >= 3000) markdownCache.delete(markdownCache.keys().next().value);
      markdownCache.set(scope, { source, format, html });
      return html;
    } catch (e) {
      console.warn("markdown failed", e);
    }
  }
  return `<p>${esc(text).replace(/\n/g, "<br>")}</p>`;
}

export const plainCache = new Map();

export function plainMarkdown(text) {
  const key = String(text || "");
  const hit = plainCache.get(key);
  if (hit !== undefined) return hit;
  const el = document.createElement("div");
  el.innerHTML = renderMarkdown(text);
  const out = (el.textContent || "").replace(/\s+/g, " ").trim();
  if (plainCache.size >= 500) plainCache.delete(plainCache.keys().next().value);
  plainCache.set(key, out);
  return out;
}

export let mermaidModule;

export let mermaidAttempt = 0;

export let diagramID = 0;

export function setupDiagramZoom(figure, width) {
  const viewport = $(".diagram-viewport", figure);
  const svg = $("svg", viewport);
  let scale = 1;
  const setScale = (next, x, y) => {
    const box = viewport.getBoundingClientRect();
    const before = svg.getBoundingClientRect();
    const anchorX = x ?? box.left + box.width / 2;
    const anchorY = y ?? box.top + box.height / 2;
    const fractionX = before.width ? (anchorX - before.left) / before.width : 0.5;
    const fractionY = before.height ? (anchorY - before.top) / before.height : 0.5;
    scale = Math.max(0.25, Math.min(5, next));
    svg.style.width = (width * scale) + "px";
    const after = svg.getBoundingClientRect();
    viewport.scrollLeft += after.left + fractionX * after.width - anchorX;
    viewport.scrollTop += after.top + fractionY * after.height - anchorY;
    $(".diagram-scale", figure).textContent = Math.round(scale * 100) + "%";
  };
  figure.addEventListener("click", (event) => {
    const action = event.target.closest("[data-diagram-zoom]")?.dataset.diagramZoom;
    if (!action) return;
    event.stopPropagation();
    setScale(action === "reset" ? 1 : scale * (action === "in" ? 1.25 : 0.8));
  });
  viewport.addEventListener("wheel", (event) => {
    event.preventDefault();
    setScale(scale * Math.exp(-event.deltaY * 0.002), event.clientX, event.clientY);
  }, { passive: false });
  let drag = null;
  viewport.addEventListener("pointerdown", (event) => {
    if (event.pointerType === "mouse" && event.button === 0) {
      drag = { x: event.clientX, y: event.clientY, left: viewport.scrollLeft, top: viewport.scrollTop, active: false };
    }
  });
  viewport.addEventListener("pointermove", (event) => {
    if (!drag || event.pointerType !== "mouse") return;
    const dx = event.clientX - drag.x, dy = event.clientY - drag.y;
    if (!drag.active && Math.hypot(dx, dy) < 4) return;
    if (!drag.active) { drag.active = true; viewport.setPointerCapture(event.pointerId); viewport.classList.add("dragging"); }
    viewport.scrollLeft = drag.left - dx;
    viewport.scrollTop = drag.top - dy;
    event.preventDefault();
  });
  const endDrag = () => { drag = null; viewport.classList.remove("dragging"); };
  viewport.addEventListener("pointerup", endDrag);
  viewport.addEventListener("pointercancel", endDrag);
  let initialDistance = 0, initialScale = 1;
  const distance = (touches) => Math.hypot(touches[0].clientX - touches[1].clientX, touches[0].clientY - touches[1].clientY);
  viewport.addEventListener("touchstart", (event) => {
    if (event.touches.length === 2) { initialDistance = distance(event.touches); initialScale = scale; event.preventDefault(); }
  }, { passive: false });
  viewport.addEventListener("touchmove", (event) => {
    if (event.touches.length === 2 && initialDistance) {
      event.preventDefault();
      setScale(initialScale * distance(event.touches) / initialDistance,
        (event.touches[0].clientX + event.touches[1].clientX) / 2,
        (event.touches[0].clientY + event.touches[1].clientY) / 2);
    }
  }, { passive: false });
  viewport.addEventListener("touchend", (event) => { if (event.touches.length < 2) initialDistance = 0; });
  setScale(1);
}

export async function renderDiagram(pre, code) {
  const source = code.textContent;
  const placeholder = document.createElement("div");
  placeholder.className = "diagram-skeleton";
  placeholder.setAttribute("role", "status");
  placeholder.setAttribute("aria-label", "Loading diagram");
  placeholder.innerHTML = `<div class="skeleton-line short"></div><div class="skeleton-diagram-row"><span></span><span></span><span></span></div>`;
  pre.before(placeholder);
  pre.hidden = true;
  $(".diagram-retry", pre)?.remove();
  let moduleLoaded = false;
  try {
    mermaidModule ||= import(`/static/vendor/pele.min.js${mermaidAttempt ? "?retry=" + mermaidAttempt : ""}`);
    const { render } = await mermaidModule;
    moduleLoaded = true;
    // Unique SVG IDs keep markers and accessibility references independent.
    const prefix = "diagram-" + (++diagramID);
    const result = render(source, { idPrefix: prefix, responsive: false, autoDirection: false,
      limit: Number.MAX_SAFE_INTEGER, outputLimit: Number.MAX_SAFE_INTEGER, maxEdges: Number.MAX_SAFE_INTEGER });
    if (!pre.isConnected) return;
    const figure = document.createElement("div");
    figure.className = "mermaid-diagram";
    figure._diagramSource = source;
    figure.innerHTML = `<div class="diagram-toolbar"><button type="button" data-diagram-zoom="out" aria-label="Zoom out">−</button><button type="button" data-diagram-zoom="reset" class="diagram-scale" aria-label="Reset zoom">100%</button><button type="button" data-diagram-zoom="in" aria-label="Zoom in">+</button></div><div class="diagram-viewport"></div><details class="diagram-source"><summary>Diagram source</summary><pre><code></code></pre></details>`;
    $(".diagram-viewport", figure).innerHTML = window.DOMPurify.sanitize(result.svg, { USE_PROFILES: { svg: true, svgFilters: true } });
    $(".diagram-source code", figure).textContent = source;
    if (!$("svg", figure)) throw new Error("Diagram SVG was not rendered");
    placeholder.remove();
    pre.replaceWith(figure);
    setupDiagramZoom(figure, result.width);
  } catch (err) {
    // Keep the original code visible when syntax is incomplete or unsupported.
    placeholder.remove();
    pre.hidden = false;
    if (!moduleLoaded && pre.isConnected) {
      mermaidModule = null;
      mermaidAttempt++;
      const retry = document.createElement("button");
      retry.type = "button";
      retry.className = "diagram-retry";
      retry.textContent = "Retry diagram";
      retry.addEventListener("click", (event) => { event.stopPropagation(); renderDiagram(pre, code); });
      pre.appendChild(retry);
    }
    console.warn("Could not render diagram", err);
  }
}

export const svgWidths = new Map();

export function sizeSVGImage(img) {
  if (img.hasAttribute("width") || img.hasAttribute("height") || !/\.svg(?:[?#]|$)/i.test(img.src)) return;
  let size = svgWidths.get(img.src);
  if (!size) {
    size = fetch(img.src).then((r) => r.ok ? r.text() : "").then((text) => {
      const svg = new DOMParser().parseFromString(text, "image/svg+xml").documentElement;
      const width = svg.getAttribute("width");
      if (width && /^\d+(?:\.\d+)?(?:px)?$/.test(width)) return parseFloat(width);
      const viewBox = (svg.getAttribute("viewBox") || "").trim().split(/[ ,]+/).map(Number);
      return viewBox.length === 4 ? viewBox[2] : 0;
    }).catch(() => 0);
    svgWidths.set(img.src, size);
  }
  size.then((width) => {
    if (img.isConnected && (width || img.naturalWidth)) img.style.width = (width || img.naturalWidth) + "px";
  });
}

export function enhanceMarkdown(root) {
  if (!root) return;
  $$(".markdown img", root).forEach(sizeSVGImage);
  $$("a", root).forEach((a) => {
    if (!a.getAttribute("href")?.startsWith("#")) { a.target = "_blank"; a.rel = "noopener noreferrer"; }
  });
  $$("pre", root).forEach((pre) => {
    if (pre.dataset.enhanced) return;
    pre.dataset.enhanced = "1";
    const diagram = $("code.language-mermaid", pre);
    if (diagram) renderDiagram(pre, diagram);
    pre.style.position = "relative";
    if (!pre.classList.contains("tool-pre")) {
      const btn = document.createElement("button");
      btn.className = "code-copy";
      btn.type = "button";
      btn.dataset.action = "copy-code";
      btn.title = "Copy code";
      btn.innerHTML = icon("copy", "sm");
      pre.appendChild(btn);
    }
    if (window.hljs) {
      $$("code", pre).forEach((code) => {
        if (code.dataset.highlighted) return;
        try {
          window.hljs.highlightElement(code);
          code.dataset.highlighted = "1";
        } catch {}
      });
    }
  });
}
