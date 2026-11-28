<template>
  <!--
    流式 markdown 渲染器（移植 ZCode Streamdown 的做法）：
    - 按空行切块、围栏代码块整体一块；已完成块经模块级缓存 memo，仅尾块随增量重渲；
    - streaming 时尾块先做 remend（闭合未完围栏/平衡行内标记）再渲染，且不做语法高亮；
      结束后整条转 static（高亮 + 缓存），与 ZCode「流式中不高亮」的取舍一致；
    - 新块淡入（CSS 动画只作用于尾块元素首挂载，DOM 文本一次到位、只动透明度）；
    - 代码块带语言标签 + 复制按钮（事件委托），外链新窗口打开；渲染异常降级纯文本。
  -->
  <div class="om-md" @click="onRootClick">
    <div
      v-for="(html, i) in rendered"
      :key="i"
      class="om-md-block"
      :class="{ 'om-md-fade': streaming && i === rendered.length - 1 }"
      v-html="html"
    />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import MarkdownIt from 'markdown-it'
import hljs from 'highlight.js/lib/common'

const props = withDefaults(defineProps<{ text: string; streaming?: boolean }>(), { streaming: false })

// ---- markdown-it 实例：static（带高亮）与 streaming（纯转义，流式中不高亮） ----

type MD = ReturnType<typeof MarkdownIt>
const mdStatic: MD = new MarkdownIt({ html: false, linkify: true, breaks: true })
const mdStream: MD = new MarkdownIt({ html: false, linkify: true, breaks: true })

function highlightCode(code: string, lang: string): string {
  if (lang && hljs.getLanguage(lang)) {
    try { return hljs.highlight(code, { language: lang, ignoreIllegals: true }).value } catch { /* 降级转义 */ }
  }
  return mdStatic.utils.escapeHtml(code)
}

function fenceRenderer(highlight: boolean) {
  return (tokens: any[], idx: number): string => {
    const token = tokens[idx]
    const lang = (token.info || '').trim().split(/\s+/)[0].toLowerCase()
    const inner = highlight ? highlightCode(token.content, lang) : mdStream.utils.escapeHtml(token.content)
    const label = mdStatic.utils.escapeHtml(lang || 'text')
    return (
      `<div class="om-code"><div class="om-code-head"><span class="om-code-lang">${label}</span>` +
      `<button type="button" class="om-code-copy" data-om-copy>复制</button></div>` +
      `<pre><code class="hljs">${inner}</code></pre></div>`
    )
  }
}

function enhanceLinks(md: MD) {
  const defaultOpen = md.renderer.rules.link_open ??
    ((tokens: any[], idx: number, _options: any, _env: any, self: any) => self.renderToken(tokens, idx))
  md.renderer.rules.link_open = (tokens: any[], idx: number, options: any, env: any, self: any) => {
    tokens[idx].attrSet('target', '_blank')
    tokens[idx].attrSet('rel', 'noopener noreferrer')
    return defaultOpen(tokens, idx, options, env, self)
  }
}

mdStatic.renderer.rules.fence = fenceRenderer(true)
mdStream.renderer.rules.fence = fenceRenderer(false)
enhanceLinks(mdStatic)
enhanceLinks(mdStream)

// ---- 块级缓存：已完成块按文本 memo，避免每帧全量重渲（O(delta) 渲染成本） ----

const blockCache = new Map<string, string>()
const BLOCK_CACHE_MAX = 400

function renderStaticBlock(block: string): string {
  const cached = blockCache.get(block)
  if (cached !== undefined) return cached
  let html: string
  try {
    html = mdStatic.render(block)
  } catch {
    html = `<pre class="om-md-raw">${mdStatic.utils.escapeHtml(block)}</pre>`
  }
  if (blockCache.size >= BLOCK_CACHE_MAX) blockCache.clear()
  blockCache.set(block, html)
  return html
}

// ---- 切块：空行分块，围栏代码块保持整体 ----

function openFenceOf(src: string): string | null {
  let fence: string | null = null
  for (const line of src.split('\n')) {
    const m = /^\s*(`{3,}|~{3,})/.exec(line)
    if (!m) continue
    if (!fence) fence = m[1]
    else if (m[1][0] === fence[0] && m[1].length >= fence.length && line.trim() === m[1]) fence = null
  }
  return fence
}

function splitBlocks(src: string): string[] {
  const blocks: string[] = []
  let cur: string[] = []
  let fence: string | null = null
  for (const line of src.split('\n')) {
    const m = /^\s*(`{3,}|~{3,})/.exec(line)
    if (fence) {
      cur.push(line)
      if (m && m[1][0] === fence[0] && m[1].length >= fence.length && line.trim() === m[1]) {
        fence = null
        blocks.push(cur.join('\n'))
        cur = []
      }
      continue
    }
    if (m) {
      if (cur.length) { blocks.push(cur.join('\n')); cur = [] }
      fence = m[1]
      cur.push(line)
      continue
    }
    if (!line.trim() && cur.length) {
      blocks.push(cur.join('\n'))
      cur = []
      continue
    }
    cur.push(line)
  }
  if (cur.length) blocks.push(cur.join('\n'))
  return blocks
}

/**
 * remend：流式尾块的最小闭合（对齐 Streamdown parseIncompleteMarkdown 的意图）——
 * 补齐未完围栏、平衡行内反引号与粗体标记，避免半截 markdown 渲染出怪异样式。
 */
function remendTail(block: string): string {
  let s = block
  const fence = openFenceOf(s)
  if (fence) return s + '\n' + fence // 围栏未闭：只补围栏，其余标记在代码块内无需平衡
  // 去掉已闭合围栏区域后统计行内标记
  let rest = ''
  let fence2: string | null = null
  for (const line of s.split('\n')) {
    const m = /^\s*(`{3,}|~{3,})/.exec(line)
    if (fence2) {
      if (m && m[1][0] === fence2[0] && m[1].length >= fence2.length && line.trim() === m[1]) fence2 = null
      continue
    }
    if (m) { fence2 = m[1]; continue }
    rest += line + '\n'
  }
  if ((rest.match(/`/g) || []).length % 2 === 1) s += '`'
  if ((rest.match(/\*\*/g) || []).length % 2 === 1) s += '**'
  return s
}

function renderTailBlock(block: string): string {
  try {
    return mdStream.render(remendTail(block))
  } catch {
    return `<pre class="om-md-raw">${mdStream.utils.escapeHtml(block)}</pre>`
  }
}

const rendered = computed<string[]>(() => {
  const blocks = splitBlocks(props.text)
  const tail = blocks.length - 1
  return blocks.map((b, i) =>
    props.streaming && i === tail ? renderTailBlock(b) : renderStaticBlock(b),
  )
})

function onRootClick(e: MouseEvent) {
  const btn = (e.target as HTMLElement).closest?.('[data-om-copy]') as HTMLElement | null
  if (!btn) return
  const code = btn.closest('.om-code')?.querySelector('pre code')?.textContent ?? ''
  navigator.clipboard?.writeText(code).then(() => {
    btn.textContent = '已复制'
    setTimeout(() => { btn.textContent = '复制' }, 1200)
  }).catch(() => { /* 剪贴板不可用：静默 */ })
}
</script>

<style>
/* ---- markdown 排版刻度（对齐 ZCode 消息排版；全局作用域供 v-html 内容命中） ---- */
.om-md { font-size: 13px; line-height: 1.7; color: var(--color-ink, #303133); word-break: break-word; }
.om-md-block > :first-child { margin-top: 0; }
.om-md-block > :last-child { margin-bottom: 0; }
.om-md p { margin: 0.5em 0; }
.om-md h1, .om-md h2, .om-md h3, .om-md h4, .om-md h5, .om-md h6 { margin: 1em 0 0.4em; font-weight: 600; line-height: 1.4; }
.om-md h1 { font-size: 1.3em; } .om-md h2 { font-size: 1.2em; } .om-md h3 { font-size: 1.1em; }
.om-md h4, .om-md h5, .om-md h6 { font-size: 1em; }
.om-md ul, .om-md ol { margin: 0.5em 0; padding-left: 1.5em; }
.om-md li { margin: 0.2em 0; }
.om-md li::marker { color: var(--color-slate, #909399); }
.om-md blockquote {
  margin: 0.6em 0; padding: 2px 12px; border-left: 3px solid rgba(22, 119, 255, 0.4);
  background: rgba(22, 119, 255, 0.05); border-radius: 0 6px 6px 0; color: var(--color-slate, #595959);
}
.om-md hr { border: none; border-top: 1px solid var(--border-card, rgba(128, 128, 128, 0.2)); margin: 1em 0; }
.om-md a { color: #1677ff; text-decoration: none; }
.om-md a:hover { text-decoration: underline; }
.om-md code { font-family: ui-monospace, Consolas, 'Courier New', monospace; }
.om-md :not(pre) > code {
  background: rgba(128, 128, 128, 0.14); border-radius: 5px; padding: 0.1em 0.4em;
  font-size: 0.92em; color: #c2410c;
}
html[data-theme='dark'] .om-md :not(pre) > code { color: #fca5a5; }
.om-md table { border-collapse: collapse; margin: 0.6em 0; font-size: 12.5px; display: block; overflow-x: auto; max-width: 100%; }
.om-md th, .om-md td { border: 1px solid var(--border-card, rgba(128, 128, 128, 0.25)); padding: 5px 10px; text-align: left; }
.om-md th { background: rgba(128, 128, 128, 0.08); font-weight: 600; }
.om-md img { max-width: 100%; border-radius: 8px; }

/* 代码块（带头部 + 复制按钮） */
.om-md .om-code { margin: 0.6em 0; border: 1px solid var(--border-card, rgba(128, 128, 128, 0.18)); border-radius: 8px; overflow: hidden; }
.om-md .om-code-head {
  display: flex; align-items: center; justify-content: space-between; padding: 4px 10px;
  background: rgba(128, 128, 128, 0.08); border-bottom: 1px solid var(--border-card, rgba(128, 128, 128, 0.14));
}
.om-md .om-code-lang { font-size: 11px; color: var(--color-slate, #909399); font-family: ui-monospace, Consolas, monospace; }
.om-md .om-code-copy {
  border: none; background: transparent; cursor: pointer; font-size: 11px; color: var(--color-slate, #909399);
  padding: 1px 6px; border-radius: 4px;
}
.om-md .om-code-copy:hover { color: #1677ff; background: rgba(22, 119, 255, 0.1); }
.om-md .om-code pre { margin: 0; padding: 10px 12px; overflow-x: auto; background: rgba(128, 128, 128, 0.05); }
.om-md .om-code code { font-size: 12px; line-height: 1.6; display: block; }
.om-md .om-md-raw {
  margin: 0.5em 0; white-space: pre-wrap; word-break: break-word;
  font-family: ui-monospace, Consolas, monospace; font-size: 12px;
}

/* 语法高亮 token（自带轻量配色，明暗两套，避免引入整份 hljs 主题 CSS） */
.om-md .hljs-keyword, .om-md .hljs-selector-tag, .om-md .hljs-literal, .om-md .hljs-section, .om-md .hljs-doctag, .om-md .hljs-name { color: #a626a4; }
.om-md .hljs-string, .om-md .hljs-regexp, .om-md .hljs-addition, .om-md .hljs-attribute, .om-md .hljs-meta .hljs-string { color: #50a14f; }
.om-md .hljs-number, .om-md .hljs-symbol, .om-md .hljs-bullet, .om-md .hljs-link, .om-md .hljs-variable { color: #986801; }
.om-md .hljs-title, .om-md .hljs-title.class_, .om-md .hljs-title.function_, .om-md .hljs-built_in { color: #4078f2; }
.om-md .hljs-comment, .om-md .hljs-quote, .om-md .hljs-deletion { color: #a0a1a7; font-style: italic; }
.om-md .hljs-type, .om-md .hljs-class .hljs-title, .om-md .hljs-params { color: #c18401; }
.om-md .hljs-attr, .om-md .hljs-property, .om-md .hljs-selector-attr, .om-md .hljs-selector-class { color: #986801; }
.om-md .hljs-emphasis { font-style: italic; }
.om-md .hljs-strong { font-weight: 600; }
html[data-theme='dark'] .om-md .hljs-keyword, html[data-theme='dark'] .om-md .hljs-selector-tag,
html[data-theme='dark'] .om-md .hljs-literal, html[data-theme='dark'] .om-md .hljs-section,
html[data-theme='dark'] .om-md .hljs-doctag, html[data-theme='dark'] .om-md .hljs-name { color: #c678dd; }
html[data-theme='dark'] .om-md .hljs-string, html[data-theme='dark'] .om-md .hljs-regexp,
html[data-theme='dark'] .om-md .hljs-addition, html[data-theme='dark'] .om-md .hljs-attribute,
html[data-theme='dark'] .om-md .hljs-meta .hljs-string { color: #98c379; }
html[data-theme='dark'] .om-md .hljs-number, html[data-theme='dark'] .om-md .hljs-symbol,
html[data-theme='dark'] .om-md .hljs-bullet, html[data-theme='dark'] .om-md .hljs-link,
html[data-theme='dark'] .om-md .hljs-variable { color: #d19a66; }
html[data-theme='dark'] .om-md .hljs-title, html[data-theme='dark'] .om-md .hljs-title.class_,
html[data-theme='dark'] .om-md .hljs-title.function_, html[data-theme='dark'] .om-md .hljs-built_in { color: #61afef; }
html[data-theme='dark'] .om-md .hljs-comment, html[data-theme='dark'] .om-md .hljs-quote,
html[data-theme='dark'] .om-md .hljs-deletion { color: #5c6370; }
html[data-theme='dark'] .om-md .hljs-type, html[data-theme='dark'] .om-md .hljs-class .hljs-title,
html[data-theme='dark'] .om-md .hljs-params { color: #e5c07b; }
html[data-theme='dark'] .om-md .hljs-attr, html[data-theme='dark'] .om-md .hljs-property,
html[data-theme='dark'] .om-md .hljs-selector-attr, html[data-theme='dark'] .om-md .hljs-selector-class { color: #d19a66; }

/* 新块淡入（只作用于流式尾块首挂载；prefers-reduced-motion 降级） */
@keyframes omMdFadeIn { from { opacity: 0; } to { opacity: 1; } }
.om-md .om-md-fade { animation: omMdFadeIn 0.3s ease; }
@media (prefers-reduced-motion: reduce) {
  .om-md .om-md-fade { animation: none; }
}
</style>
