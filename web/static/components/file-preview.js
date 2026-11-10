import { $, esc, icon } from './util.js';
import { S } from './state.js';
import { modalHead, openModal } from './modal.js';
import { enhanceMarkdown, renderMarkdown } from './markdown.js';
import { resourceKind, decodeFileText, prepareResources } from './resources.js';

let disposePreview = () => {};

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
    const response = await fetch(url, { credentials: 'same-origin', signal: controller.signal });
    if (!response.ok) {
      let message = `Could not open file (${response.status}).`;
      if ((response.headers.get('content-type') || '').includes('application/json')) { try { message = (await response.json()).error || message; } catch {} }
      throw new Error(message);
    }
    if (!root.isConnected) return;
    const type = response.headers.get('content-type') || '';
    const kind = resourceKind(name, type) || resourceKind(url, type);
    if (['image','audio','video'].includes(kind)) { await response.body?.cancel(); showMedia(kind, url); return; }
    const blob = await response.blob();
    if (!root.isConnected) return;
    if (kind === 'pdf') {
      const source = URL.createObjectURL(new Blob([blob], { type: 'application/pdf' }));
      objectURLs.push(source);
      const frame = document.createElement('iframe');
      frame.title = name; frame.src = source;
      root.replaceChildren(frame);
    } else {
      let text;
      try { text = decodeFileText(await blob.arrayBuffer(), type); }
      catch { throw new Error('Preview is unavailable for this file type. Use Download or Open original.'); }
      if (kind === 'html' && !context.line) {
        const frame = document.createElement('iframe');
        frame.title = name; frame.setAttribute('sandbox', ''); frame.srcdoc = prepareResources(text, context);
        root.replaceChildren(frame);
      } else if (kind === 'markdown' && !context.line) {
        const doc = document.createElement('div'); doc.className = 'markdown';
        doc.innerHTML = renderMarkdown(text, 'file-preview', 'markdown', context);
        root.replaceChildren(doc); enhanceMarkdown(root);
        if (context.anchor) doc.querySelector(`[data-anchor="${CSS.escape(context.anchor)}"]`)?.scrollIntoView({ block: 'start' });
      } else {
        const pre = document.createElement('pre');
        if (context.line > 0) {
          text.split('\n').forEach((line, i) => {
            const row = document.createElement('span'); row.className = 'file-line'; row.textContent = line + '\n';
            if (i + 1 === Number(context.line)) { row.classList.add('file-line-selected'); row.dataset.selectedLine = '1'; }
            pre.appendChild(row);
          });
        } else pre.textContent = text;
        root.replaceChildren(pre);
        $('[data-selected-line]', root)?.scrollIntoView({ block: 'center' });
      }
    }
    root.removeAttribute('role');
  } catch (error) {
    if (error.name !== 'AbortError') fail(error.message || 'Could not open this file. Use Open original to open it directly.');
  } finally { clearTimeout(deadline); }
}
