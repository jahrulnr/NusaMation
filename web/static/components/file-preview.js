import { $, esc, icon } from './util.js';
import { S } from './state.js';
import { modalHead, openModal } from './modal.js';
import { enhanceMarkdown, renderMarkdown } from './markdown.js';
import { resourceKind, decodeFileText, prepareResources, highlightLanguage } from './resources.js';

let disposePreview = () => {};

// Text is decoded to a JS string (~2 bytes/char) plus DOM rows — unbounded
// files would OOM the tab. Same-origin files are fetched through a Range
// request, so the server only ever sends the window we ask for; a truncated
// response renders its first window with a notice. Deep links to a line get a
// larger window so most jumps still land.
const PREVIEW_CAP = 1024 * 1024;      // 1 MB
const PREVIEW_LINE_CAP = 8 * 1024 * 1024; // 8 MB when jumping to a line

const fmtMB = (n) => n >= 1024 * 1024 ? `${(n / 1048576).toFixed(1)} MB` : `${Math.max(1, Math.ceil(n / 1024))} KB`;

// Split highlighted code into .file-line rows at newlines. highlight.js spans
// can straddle line breaks (block comments, raw strings), so each segment
// reopens a clone of the element chain it came from to keep its colours.
function highlightedRows(code) {
  const rows = [];
  let row, wrappers;
  const newRow = () => { row = document.createElement('span'); row.className = 'file-line'; wrappers = []; rows.push(row); };
  const sink = () => wrappers.length ? wrappers[wrappers.length - 1] : row;
  const walk = (src, path) => {
    for (const node of src.childNodes) {
      if (node.nodeType === Node.TEXT_NODE) {
        node.textContent.split('\n').forEach((part, i) => {
          if (i > 0) newRow();
          if (!part) return;
          let k = 0;
          while (k < wrappers.length && k < path.length && wrappers[k]._src === path[k]) k++;
          wrappers.length = k;
          while (k < path.length) {
            const clone = document.createElement('span');
            clone.className = path[k].className;
            clone._src = path[k];
            sink().appendChild(clone);
            wrappers.push(clone);
            k++;
          }
          sink().appendChild(document.createTextNode(part));
        });
      } else if (node.nodeType === Node.ELEMENT_NODE) walk(node, [...path, node]);
    }
  };
  newRow();
  walk(code, []);
  return rows;
}

export async function previewResource(url, name = 'File', context = {}) {
  if (!url) return;
  disposePreview();
  let download;
  try { download = new URL(url, location.href); }
  catch {
    S.modal = { type: 'file-preview' };
    openModal(`${modalHead(name, 'File preview')}<div class="modal-body file-preview-body">Invalid file URL.</div>`, { size: 'file-preview-dialog' });
    return;
  }
  const controller = new AbortController();
  const objectURLs = [];
  const deadline = setTimeout(() => controller.abort(new DOMException("The file took too long to load. Try again.", "TimeoutError")), 20000);
  if (download.origin === location.origin && /^\/api\/(?:uploads\/|conversations\/[^/]+\/files)/.test(download.pathname)) download.searchParams.set('download', '1');
  S.modal = { type: 'file-preview', url };
  openModal(`${modalHead(name, 'File preview')}<div class="modal-body file-preview-body"><div class="file-preview-content" role="status">Loading file…</div></div><div class="modal-foot"><button type="button" class="btn btn-secondary" data-action="preview-resource" data-url="${esc(url)}" data-name="${esc(name)}" data-path="${esc(context.basePath || '')}" data-conversation="${esc(context.conversationId || '')}" data-line="${context.line || 0}" data-anchor="${esc(context.anchor || '')}" data-kind="${esc(context.kind || '')}">${icon('refresh', 'sm')}Retry</button><a class="btn btn-secondary" href="${esc(url)}" target="_blank" rel="noopener">Open original</a><a class="btn btn-primary" href="${esc(download.href)}" download="${esc(name)}">${icon('download','sm')}Download</a></div>`, { size: 'file-preview-dialog' });
  const root = $('#modal-root .file-preview-content');
  const observer = new MutationObserver(() => { if (!root.isConnected) dispose(); });
  const dispose = () => { clearTimeout(deadline); controller.abort(); observer.disconnect(); objectURLs.forEach(URL.revokeObjectURL); };
  disposePreview = dispose;
  observer.observe($('#modal-root'), { childList: true, subtree: true });
  const fail = (message) => { if (root.isConnected) { root.textContent = message; root.classList.add('file-preview-error'); } };
  const showMedia = (kind, source) => {
    const element = document.createElement(kind === 'image' ? 'img' : kind);
    if (kind === 'image') { element.alt = name; element.addEventListener('load', () => root.removeAttribute('role')); }
    else { element.controls = true; element.preload = 'metadata'; if (kind === 'video') element.playsInline = true; }
    element.addEventListener('error', () => fail('This media could not be loaded or its format is not supported by this browser. Use Download or Open original.'));
    root.replaceChildren(element);
    element.src = source;
  };
  try {
    // External media may be playable by the browser even if its host forbids
    // CORS fetches. Local resources use response MIME instead of extensions.
    const remote = download.origin !== location.origin;
    const guess = context.kind || resourceKind(name) || resourceKind(url);
    if (remote && ['image','audio','video'].includes(guess)) { showMedia(guess, url); return; }
    const cap = context.line > 0 ? PREVIEW_LINE_CAP : PREVIEW_CAP;
    const headers = remote ? {} : { Range: `bytes=0-${cap - 1}` };
    const response = await fetch(url, { credentials: 'same-origin', signal: controller.signal, headers });
    if (!response.ok) {
      let message = `Could not open file (${response.status}).`;
      if ((response.headers.get('content-type') || '').includes('application/json')) { try { message = (await response.json()).error || message; } catch {} }
      throw new Error(message);
    }
    if (!root.isConnected) return;
    const type = response.headers.get('content-type') || '';
    const kind = resourceKind(name, type) || resourceKind(url, type);
    if (['image','audio','video'].includes(kind)) { await response.body?.cancel(); showMedia(kind, url); return; }
    // File size from Content-Range (206) or Content-Length (200). A remote
    // host may ignore Range entirely; when it does and the file is heavy,
    // refuse before pulling the whole body.
    const len = Number(response.headers.get('content-length') || 0);
    if (len > cap) {
      await response.body?.cancel();
      throw new Error(`This file is too large to preview (${fmtMB(len)}). Use Download or Open original.`);
    }
    const blob = await response.blob();
    if (!root.isConnected) return;
    const body = blob.size > cap ? blob.slice(0, cap) : blob;
    const rangeTotal = (response.headers.get('content-range') || '').split('/')[1];
    const total = Number(rangeTotal || len || blob.size) || 0;
    // body.size < blob.size means a compressed or length-less response still
    // overran the cap — a fragment must never render as a complete file.
    const truncated = blob.size > cap || total > body.size;
    if (kind === 'pdf') {
      if (truncated) throw new Error(`This PDF is too large to preview${total ? ` (${fmtMB(total)})` : ''}. Use Download or Open original.`);
      const source = URL.createObjectURL(new Blob([body], { type: 'application/pdf' }));
      objectURLs.push(source);
      const frame = document.createElement('iframe');
      frame.title = name; frame.src = source;
      root.replaceChildren(frame);
    } else {
      let text;
      // Non-fatal on a truncated window: the cap can split a multi-byte
      // character, and a replacement glyph at the seam beats a failed preview.
      try { text = decodeFileText(await body.arrayBuffer(), type, !truncated); }
      catch { throw new Error('Preview is unavailable for this file type. Use Download or Open original.'); }
      // A truncated file renders as plain text: partial HTML/PDF renders are
      // broken, and "first window of the file" is the honest view.
      if (kind === 'html' && !context.line && !truncated) {
        const frame = document.createElement('iframe');
        frame.title = name; frame.setAttribute('sandbox', ''); frame.srcdoc = prepareResources(text, context);
        root.replaceChildren(frame);
      } else if (kind === 'markdown' && !context.line && !truncated) {
        const doc = document.createElement('div'); doc.className = 'markdown';
        doc.innerHTML = renderMarkdown(text, 'file-preview', 'markdown', context);
        root.replaceChildren(doc); enhanceMarkdown(root);
        if (context.anchor) doc.querySelector(`[data-anchor="${CSS.escape(context.anchor)}"]`)?.scrollIntoView({ block: 'start' });
      } else {
        const pre = document.createElement('pre');
        const lang = text.length <= 512 * 1024 ? highlightLanguage(name, type) : '';
        if (lang && window.hljs?.getLanguage?.(lang)) {
          const code = document.createElement('code');
          code.className = `language-${lang}`;
          code.textContent = text;
          try { window.hljs.highlightElement(code); } catch { code.textContent = text; code.className = ''; }
          if (context.line > 0) {
            const rows = highlightedRows(code);
            const target = rows[Number(context.line) - 1];
            if (target) { target.classList.add('file-line-selected'); target.dataset.selectedLine = '1'; }
            code.replaceChildren(...rows);
          }
          pre.appendChild(code);
        } else if (context.line > 0) {
          text.split('\n').forEach((line, i) => {
            const row = document.createElement('span'); row.className = 'file-line'; row.textContent = line + '\n';
            if (i + 1 === Number(context.line)) { row.classList.add('file-line-selected'); row.dataset.selectedLine = '1'; }
            pre.appendChild(row);
          });
        } else pre.textContent = text;
        root.replaceChildren(pre);
        $('[data-selected-line]', root)?.scrollIntoView({ block: 'center' });
      }
      if (truncated) {
        const note = document.createElement('div');
        note.className = 'file-preview-truncated';
        const shown = fmtMB(body.size), whole = fmtMB(total);
        note.textContent = (shown === whole ? `Showing the first ${body.size.toLocaleString()} bytes of a ${total.toLocaleString()}-byte file.`
          : `Showing the first ${shown} of a ${whole} file.`) +
          (context.line > 0 && !root.querySelector('[data-selected-line]') ? ` Line ${context.line} is beyond the shown window.` : '');
        root.appendChild(note);
      }
    }
    root.removeAttribute('role');
  } catch (error) {
    if (error.name !== 'AbortError') fail(error.message || 'Could not open this file. Use Open original to open it directly.');
  } finally { clearTimeout(deadline); }
}
