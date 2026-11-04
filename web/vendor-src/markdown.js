import markedKatex from 'marked-katex-extension';
import markedFootnote from 'marked-footnote';
import { markedEmoji } from 'marked-emoji';
import { gemoji } from 'gemoji';
import katex from 'katex';
const emojis = Object.fromEntries(gemoji.flatMap(({names, emoji}) => names.map(name => [name, emoji])));
const latex = () => ({ extensions: [
  { name: 'latexBlock', level: 'block', start: src => src.indexOf('\\['),
    tokenizer(src) { const m = /^\\\[([\s\S]+?)\\\](?:\n|$)/.exec(src); if (m) return {type:'latexBlock', raw:m[0], text:m[1]}; },
    renderer: token => katex.renderToString(token.text, {displayMode:true, throwOnError:false}) },
  { name: 'latexInline', level: 'inline', start: src => src.indexOf('\\('),
    tokenizer(src) { const m = /^\\\((.+?)\\\)/.exec(src); if (m) return {type:'latexInline', raw:m[0], text:m[1]}; },
    renderer: token => katex.renderToString(token.text, {displayMode:false, throwOnError:false}) }
]});
const math = () => {
  const extension = markedKatex({ throwOnError: false });
  const inline = extension.extensions.find(item => item.name === 'inlineKatex');
  const tokenize = inline.tokenizer;
  inline.tokenizer = function(...args) {
    const token = tokenize.apply(this, args);
    return token && !/[\$`]/.test(token.text) ? token : undefined;
  };
  return extension;
};
window.NusaMarkdown = { math, markedFootnote, latex, emoji: () => markedEmoji({ emojis, renderer: token => token.emoji }) };
