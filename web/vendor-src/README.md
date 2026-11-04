# Markdown vendor bundle

`markdown.js` bundles the local Marked extensions and KaTeX used by the web UI.
The versions are recorded in `web/static/vendor/NOTICE.md`. To rebuild with Node:

```sh
mkdir -p /tmp/nusamation-render-build
npm install --prefix /tmp/nusamation-render-build katex@0.18.10 marked-katex-extension@5.1.13 marked-footnote@1.4.0 marked-emoji@3.0.0 gemoji@8.1.0 esbuild@0.28.2 --no-audit --no-fund
cp web/vendor-src/markdown.js /tmp/nusamation-render-build/markdown.js
/tmp/nusamation-render-build/node_modules/.bin/esbuild /tmp/nusamation-render-build/markdown.js --bundle --minify --format=iife --platform=browser --outfile=web/static/vendor/markdown-math.min.js
node --test web/tests/*.test.cjs
```

KaTeX's CSS and fonts are vendored separately. Update them and the license files
alongside the bundle when changing versions.
