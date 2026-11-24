<template>
  <div class="te-wrap">
    <div class="te-inner">
      <!-- 终端窗口示意：用真实会话的观感替代空白，一眼看出这里是终端 -->
      <div class="te-window" aria-hidden="true">
        <div class="te-chrome">
          <span class="te-dot te-dot-r"></span>
          <span class="te-dot te-dot-y"></span>
          <span class="te-dot te-dot-g"></span>
          <span class="te-chrome-label">zjpanel · terminal</span>
          <span class="te-chrome-spacer"></span>
        </div>
        <div class="te-screen">
          <div class="te-line">
            <span class="te-ps">$</span>
            <span class="te-cmd">ssh prod-web</span>
            <span class="te-caret"></span>
          </div>
          <div class="te-line te-line-dim">
            <span class="te-ok-dot"></span>
            <span>root@prod-web · 会话常驻后端</span>
          </div>
        </div>
      </div>

      <div class="te-copy">
        <h3 class="te-title">还没有打开的会话</h3>
        <p class="te-sub">连接一台服务器，或打开本地终端开始操作。</p>
      </div>

      <div class="te-actions">
        <!-- 已有主机：主行动是挑一台连上去 -->
        <a-dropdown v-if="hosts.length">
          <a-button type="primary" size="large">
            新建 SSH 会话 <DownOutlined />
          </a-button>
          <template #overlay>
            <a-menu>
              <a-menu-item v-for="h in hosts" :key="h.id" @click="$emit('connect', h)">
                {{ h.name }}（{{ h.username }}@{{ h.host }}:{{ h.port }}）
              </a-menu-item>
            </a-menu>
          </template>
        </a-dropdown>
        <!-- 一台主机都没有：主行动改为先添加主机，避免点开空菜单 -->
        <a-button v-else type="primary" size="large" @click="$emit('manage')">添加 SSH 主机</a-button>
        <a-button size="large" @click="$emit('local')">本地终端</a-button>
        <!-- 已有主机时主按钮是「连一台」，主机管理收为次级入口，避免无会话时无处可管 -->
        <a-button v-if="hosts.length" type="text" size="large" @click="$emit('manage')">管理主机</a-button>
      </div>

      <div v-if="hosts.length" class="te-hosts">
        <span class="te-hosts-label">快速连接</span>
        <div class="te-hosts-list">
          <button v-for="h in hosts" :key="h.id" type="button" class="te-host" :title="`连接到 ${h.name}`"
            @click="$emit('connect', h)">
            <CloudServerOutlined class="te-host-ic" />
            <span class="te-host-name">{{ h.name }}</span>
            <span class="te-host-addr">{{ h.username }}@{{ h.host }}</span>
          </button>
        </div>
      </div>

      <div class="te-meta">
        <span>会话常驻后端</span><span class="te-sep">·</span>
        <span>断线自动重连</span><span class="te-sep">·</span>
        <span>刷新页面自动恢复</span>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { CloudServerOutlined, DownOutlined } from '@ant-design/icons-vue'

defineProps<{ hosts: any[] }>()
defineEmits<{ (e: 'connect', host: any): void; (e: 'local'): void; (e: 'manage'): void }>()
</script>

<style scoped>
/* 空态占满终端区：正常时垂直居中，内容过高（主机多/窄屏）改为滚动，不挤压卡片 */
.te-wrap { flex: 1; min-height: 0; display: flex; padding: 24px; overflow: auto; }
.te-inner { margin: auto; display: flex; flex-direction: column; align-items: center; gap: 20px; width: 100%; }

/* ---------- 终端窗口示意 ---------- */
.te-window {
  width: min(400px, 100%);
  border: 1px solid rgba(148, 163, 184, .26);
  border-radius: 10px;
  background: rgba(148, 163, 184, .07);
  box-shadow: 0 10px 26px rgba(2, 6, 23, .14);
  overflow: hidden;
}
.te-chrome {
  display: flex; align-items: center; gap: 6px;
  height: 30px; padding: 0 12px;
  border-bottom: 1px solid rgba(148, 163, 184, .18);
  background: rgba(148, 163, 184, .07);
}
.te-dot { width: 9px; height: 9px; border-radius: 50%; opacity: .62; }
.te-dot-r { background: #ff5f57; }
.te-dot-y { background: #febc2e; }
.te-dot-g { background: #28c840; }
.te-chrome-label {
  flex: 1; min-width: 0; text-align: center;
  font-size: 11px; letter-spacing: .06em; color: var(--color-slate); opacity: .75;
}
.te-chrome-spacer { flex: none; width: 27px; } /* 与左侧三个圆点等宽，标题真正居中 */

.te-screen {
  padding: 14px 16px 16px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, 'Courier New', monospace;
  font-size: 13px; line-height: 1.95;
}
.te-line { display: flex; align-items: center; gap: 7px; white-space: nowrap; }
.te-ps { color: var(--color-confirm-green); font-weight: 600; }
.te-cmd { color: var(--color-ink); overflow: hidden; text-overflow: ellipsis; }
.te-caret {
  flex: none; width: 7px; height: 14px; border-radius: 1px;
  background: var(--color-confirm-green); opacity: .9;
  animation: te-blink 1.2s steps(1, end) infinite;
}
@keyframes te-blink { 0%, 55% { opacity: .9; } 56%, 100% { opacity: 0; } }
.te-line-dim { margin-top: 2px; font-size: 12px; color: var(--color-slate); }
.te-ok-dot { flex: none; width: 6px; height: 6px; border-radius: 50%; background: var(--color-confirm-green); opacity: .8; }

/* ---------- 文案与操作 ---------- */
.te-copy { display: flex; flex-direction: column; align-items: center; gap: 8px; }
.te-title { margin: 0; font-size: 18px; font-weight: 600; color: var(--color-ink); letter-spacing: .01em; }
.te-sub { margin: 0; max-width: 480px; text-align: center; font-size: 13px; line-height: 1.75; color: var(--color-slate); }
.te-actions { display: flex; align-items: center; justify-content: center; flex-wrap: wrap; gap: 10px; }

/* ---------- 快速连接 ---------- */
.te-hosts { display: flex; flex-direction: column; align-items: center; gap: 10px; width: 100%; }
.te-hosts-label { font-size: 11.5px; letter-spacing: .12em; color: var(--color-slate); opacity: .8; }
.te-hosts-list { display: flex; flex-wrap: wrap; justify-content: center; gap: 8px; max-width: 660px; }
.te-host {
  display: inline-flex; align-items: center; gap: 7px;
  height: 32px; padding: 0 12px; border-radius: var(--radius-buttons, 8px);
  border: 1px solid var(--border-card); background: transparent;
  font-size: 12.5px; color: var(--color-ink); cursor: pointer;
  transition: border-color var(--motion-fast), background var(--motion-fast), color var(--motion-fast);
}
.te-host:hover { border-color: var(--color-action-blue); background: var(--bg-selected); }
.te-host:focus-visible { border-color: var(--color-action-blue); box-shadow: var(--focus-ring); outline: none; }
.te-host-ic { font-size: 13px; color: var(--color-slate); }
.te-host:hover .te-host-ic { color: var(--color-corporate-blue); }
.te-host-name { font-weight: 500; }
.te-host:hover .te-host-name { color: var(--color-corporate-blue); }
.te-host-addr { color: var(--color-slate); font-size: 12px; font-variant-numeric: tabular-nums; }

/* ---------- 底部特性说明 ---------- */
.te-meta { display: flex; align-items: center; justify-content: center; flex-wrap: wrap; gap: 6px; font-size: 12px; color: var(--color-slate); opacity: .85; }
.te-sep { opacity: .45; }

@media (max-width: 640px) {
  .te-wrap { padding: 16px; }
  .te-inner { gap: 16px; }
  .te-chrome-label { display: none; }
  .te-chrome-spacer { display: none; }
}
@media (prefers-reduced-motion: reduce) {
  .te-caret { animation: none; }
}
</style>
