// Resource classification is shared by Markdown, attachments and the preview.

// Map filenames to the highlight.js language ids the vendored common build
// registers. Unknown or unregistered languages fall through to '' — the
// preview then renders plain text rather than miscoloured guesses.
const HIGHLIGHT_BY_EXT = {
  go:'go', js:'javascript', mjs:'javascript', cjs:'javascript', jsx:'javascript',
  ts:'typescript', tsx:'typescript', mts:'typescript', cts:'typescript',
  py:'python', pyw:'python', rs:'rust',
  c:'c', h:'c', cpp:'cpp', cc:'cpp', cxx:'cpp', hpp:'cpp', hh:'cpp', hxx:'cpp',
  java:'java', kt:'kotlin', kts:'kotlin', rb:'ruby', gemspec:'ruby', php:'php',
  swift:'swift', cs:'csharp', vb:'vbnet', lua:'lua', pl:'perl', pm:'perl', r:'r',
  mm:'objectivec', objc:'objectivec',
  sh:'bash', bash:'bash', zsh:'bash', ksh:'bash',
  css:'css', scss:'scss', less:'less',
  xml:'xml', xsl:'xml', plist:'xml', html:'xml', htm:'xml',
  json:'json', jsonc:'json', jsonl:'json',
  yaml:'yaml', yml:'yaml', toml:'ini', ini:'ini', conf:'ini', env:'ini', cfg:'ini', properties:'ini',
  md:'markdown', mkd:'markdown',
  sql:'sql', diff:'diff', patch:'diff',
  mk:'makefile', mak:'makefile', make:'makefile',
  gql:'graphql', graphql:'graphql',
  wat:'wasm', wast:'wasm',
  txt:'plaintext', text:'plaintext', log:'plaintext', csv:'plaintext', tsv:'plaintext',
};
const HIGHLIGHT_BY_BASENAME = { makefile:'makefile', gnumakefile:'makefile' };
const HIGHLIGHT_BY_MIME = {
  'application/json':'json', 'application/javascript':'javascript', 'text/javascript':'javascript',
  'text/css':'css', 'application/xml':'xml', 'text/xml':'xml',
  'application/x-yaml':'yaml', 'application/yaml':'yaml', 'text/yaml':'yaml', 'text/x-yaml':'yaml',
  'application/x-sh':'bash', 'application/x-shellscript':'bash',
  'text/x-python':'python', 'text/x-go':'go',
};

export function highlightLanguage(name = '', mime = '') {
  const clean = String(name).split(/[?#]/)[0].replace(/:\d+(?::\d+)?$/, '').toLowerCase();
  const base = clean.split('/').pop();
  if (HIGHLIGHT_BY_BASENAME[base]) return HIGHLIGHT_BY_BASENAME[base];
  if (HIGHLIGHT_BY_EXT[base.split('.').pop()]) return HIGHLIGHT_BY_EXT[base.split('.').pop()];
  const type = mime.split(';')[0].trim().toLowerCase();
  return HIGHLIGHT_BY_MIME[type] || '';
}

export function resourceKind(name = '', mime = '') {
  const type = mime.split(';')[0].trim().toLowerCase();
  if (/^(image|video|audio)\//.test(type)) return type.split('/')[0];
  if (type === 'application/pdf') return 'pdf';
  const ext = String(name).split(/[?#]/)[0].replace(/:\d+(?::\d+)?$/, '').split('.').pop().toLowerCase();
  if (['png','jpg','jpeg','gif','webp','svg','avif','bmp','ico'].includes(ext)) return 'image';
  if (['mp4','webm','mov','m4v','ogv'].includes(ext)) return 'video';
  if (['mp3','wav','ogg','oga','m4a','aac','flac','opus'].includes(ext)) return 'audio';
  if (ext === 'pdf') return 'pdf';
  if (['md','markdown'].includes(ext)) return 'markdown';
  if (['html','htm'].includes(ext) || type === 'text/html') return 'html';
  if (type.startsWith('text/') || ['application/json','application/xml','application/javascript'].includes(type) || ['txt','log','csv','json','jsonl','xml','yaml','yml','toml','ini','conf','js','mjs','cjs','ts','tsx','jsx','css','scss','go','py','sh','rs','c','cpp','h','java','kt','sql','diff','patch'].includes(ext)) return 'text';
  return '';
}

export function resolveResource(raw, context = {}) {
  const value = String(raw || '').trim();
  const editorPath = !value.includes('://') && /\.[^\\/:]+:\d+(?::\d+)?$/.test(value);
  const scheme = /^[a-z][a-z\d+.-]*:/i.test(value) && !/^[a-z]:[\\/]/i.test(value) && !editorPath;
  const local = !!value && !value.startsWith('#') && (!scheme || /^file:/i.test(value) || /^sandbox:\//i.test(value)) && !value.startsWith('//') && !/^\/(?:api|static|c)(?:\/|$)/.test(value);
  let name = value.split(/[\\/]/).pop() || 'File';
  try { name = decodeURIComponent(name); } catch {}
  const base = { url: value, name, local: false, path: value, line: 0 };
  if (!local || !context.conversationId) return base;
  let path = value.replace(/^sandbox:(?=\/)/i, '');
  let line = 0;
  let anchor = '';
  const hash = path.indexOf('#');
  if (hash >= 0) {
    anchor = path.slice(hash + 1);
    const lineAnchor = /^L?(\d+)(?:C\d+|:\d+|-L?\d+)?$/i.exec(anchor);
    if (lineAnchor) line = Number(lineAnchor[1]);
    path = path.slice(0, hash);
  }
  const colon = /:(\d+)(?::\d+)?$/.exec(path);
  if (colon) line = Number(colon[1]);
  if (!/^file:/i.test(path)) {
    try { path = decodeURIComponent(path); } catch {}
    if (context.basePath && !/^(?:[\\/]|[a-z]:[\\/]|~[\\/])/i.test(path)) path = (/[\\/]/.test(context.basePath) ? context.basePath.replace(/[\\/][^\\/]*$/, '') : '.') + '/' + path;
  }
  return { url: `/api/conversations/${encodeURIComponent(context.conversationId)}/files?path=${encodeURIComponent(path)}`, path, name, local: true, line, anchor };
}

// Only media and anchor tags become live markup in prose. Other HTML remains visible as
// source. Attributes are copied explicitly before the normal DOMPurify pass.
export function mediaHTML(raw, escape) {
  if (!/^\s*<\/?(?:img|video|audio|source|track|picture|a)\b/i.test(raw)) return escape(raw);
  const template = document.createElement('template');
  template.innerHTML = raw;
  const tags = new Set(['IMG','VIDEO','AUDIO','SOURCE','TRACK','PICTURE','A']);
  const attrs = new Set(['src','alt','title','controls','poster','preload','width','height','type','loop','muted','autoplay','kind','srclang','label','default','srcset','sizes','href','target','rel','download']);
  for (const el of template.content.querySelectorAll('*')) {
    if (!tags.has(el.tagName)) { el.replaceWith(document.createTextNode(el.outerHTML)); continue; }
    for (const attr of [...el.attributes]) if (!attrs.has(attr.name)) el.removeAttribute(attr.name);
  }
  return template.innerHTML;
}

export function prepareResources(html, context = {}) {
  const template = document.createElement('template');
  template.innerHTML = html;
  const headings = new Map();
  const counts = new Map();
  for (const heading of template.content.querySelectorAll('h1,h2,h3,h4,h5,h6')) {
    const slug = heading.textContent.toLowerCase().trim().replace(/[^\p{L}\p{N}\s-]/gu, '').replace(/\s+/g, '-');
    const count = counts.get(slug) || 0; counts.set(slug, count + 1);
    const anchor = slug + (count ? '-' + count : '');
    heading.id = 'heading-' + String(context.scope || 'preview').replace(/[^a-zA-Z0-9_-]/g, '-') + '-' + anchor;
    heading.dataset.anchor = anchor; headings.set(anchor, heading.id);
  }
  for (const a of template.content.querySelectorAll('a[href]')) {
    const ref = resolveResource(a.getAttribute('href'), context);
    const anchor = ref.url.startsWith('#') ? ref.url.slice(1) : '';
    a.setAttribute('href', headings.has(anchor) ? '#' + headings.get(anchor) : ref.url);
    // Browser/page anchors and web pages keep normal navigation. File links
    // use one popup regardless of whether they came from Markdown or HTML.
    if (ref.local || /^\/api\/uploads\//.test(ref.url) || (/^(?:https?:)?\/\//i.test(ref.url) && resourceKind(ref.url))) {
      a.dataset.action = 'preview-resource';
      a.dataset.url = ref.url;
      a.dataset.name = ref.name;
      a.dataset.path = ref.path;
      a.dataset.conversation = context.conversationId || '';
      if (ref.line) a.dataset.line = ref.line;
      if (ref.anchor) a.dataset.anchor = ref.anchor;
    } else if (/^(?:https?:)?\/\//i.test(ref.url)) { a.target = '_blank'; a.rel = 'noopener noreferrer'; }
  }
  for (const image of template.content.querySelectorAll('img[src]')) {
    const kind = resourceKind(image.getAttribute('src'));
    if (kind === 'video' || kind === 'audio') {
      const media = document.createElement(kind);
      media.setAttribute('src', image.getAttribute('src'));
      media.setAttribute('aria-label', image.alt || kind);
      image.replaceWith(media);
    }
  }
  for (const el of template.content.querySelectorAll('img, video, audio, source, track')) {
    for (const attr of ['src','poster']) {
      if (!el.hasAttribute(attr)) continue;
      const ref = resolveResource(el.getAttribute(attr), context);
      el.setAttribute(attr, ref.url);
    }
    // srcset permits multiple descriptors; the browser splits candidates.
    if (el.hasAttribute('srcset')) {
      const srcset = el.getAttribute('srcset');
      if (!srcset.trim().startsWith('data:')) el.setAttribute('srcset', srcset.split(',').map(candidate => {
        const [url, ...descriptor] = candidate.trim().split(/\s+/);
        return [resolveResource(url, context).url, ...descriptor].join(' ');
      }).join(', '));
    }
    if (['VIDEO','AUDIO'].includes(el.tagName)) { el.controls = true; el.preload = 'metadata'; if (el.tagName === 'VIDEO') el.setAttribute('playsinline', ''); }
    if (el.tagName === 'IMG') { el.loading = 'lazy'; el.dataset.action = 'preview-resource'; el.dataset.url = el.getAttribute('src') || ''; el.dataset.name = el.alt || 'Image'; el.dataset.kind = 'image'; }
  }
  return template.innerHTML;
}

export function snapshotMedia(root) {
  return [...root.querySelectorAll('.markdown img, .markdown video, .markdown audio')].map(node => ({ node, html: node._mediaMarkup || node.outerHTML, playing: node.tagName !== 'IMG' && !node.paused && !node.ended }));
}

export function restoreMedia(root, saved = []) {
  const remaining = saved.slice();
  for (const node of root.querySelectorAll('.markdown img, .markdown video, .markdown audio')) {
    const i = remaining.findIndex(item => item.html === node.outerHTML);
    if (i >= 0) {
      const previous = remaining.splice(i, 1)[0];
      node.replaceWith(previous.node);
      // Detaching a media element may pause it even when the same node is
      // reinserted; resume only players that were already playing.
      if (previous.playing) previous.node.play().catch(() => {});
    }
  }
}

export function enhanceMedia(root) {
  for (const el of root.querySelectorAll('.markdown img, .markdown video, .markdown audio')) {
    if (!el._mediaMarkup) {
      el._mediaMarkup = el.outerHTML;
      el.addEventListener('error', () => { el._mediaFailed = true; showError(); });
    }
    const showError = () => {
      if (!el.isConnected || el.nextElementSibling?.classList.contains('media-load-error')) return;
      const a = document.createElement('a');
      a.className = 'media-load-error'; a.textContent = 'Media unavailable — open file';
      a.href = el.currentSrc || el.src || el.querySelector('source')?.src || '';
      a.dataset.action = 'preview-resource'; a.dataset.url = a.href; a.dataset.name = el.alt || 'Media'; a.dataset.kind = el.tagName.toLowerCase() === 'img' ? 'image' : el.tagName.toLowerCase();
      el.after(a);
    };
    if (el._mediaFailed || (el.tagName === 'IMG' && el.complete && !el.naturalWidth) || el.error) showError();
  }
}

export function decodeFileText(bytes, mime = '') {
  const data = new Uint8Array(bytes);
  const encoding = data[0] === 255 && data[1] === 254 ? 'utf-16le' : data[0] === 254 && data[1] === 255 ? 'utf-16be' : (/charset=["']?([^;"'\s]+)/i.exec(mime)?.[1] || 'utf-8');
  const text = new TextDecoder(encoding, { fatal: true }).decode(bytes);
  if (text.includes('\0')) throw new TypeError('Binary file');
  return text;
}
