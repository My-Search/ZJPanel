<template>
  <a-card size="small" :body-style="{ padding: '12px', height: 'calc(100vh - 130px)' }">
    <a-space style="margin-bottom: 10px" wrap>
      <a-dropdown>
        <a-button type="primary">新建 SSH 会话 <DownOutlined /></a-button>
        <template #overlay>
          <a-menu>
            <a-menu-item v-for="h in hosts" :key="h.id" @click="newSsh(h)">
              {{ h.name }}（{{ h.username }}@{{ h.host }}:{{ h.port }}）
            </a-menu-item>
            <a-menu-item v-if="!hosts.length" disabled>暂无 SSH 主机，请先添加</a-menu-item>
          </a-menu>
        </template>
      </a-dropdown>
      <a-button @click="newLocal">本地终端</a-button>
      <a-button @click="showHostMgr = true">管理 SSH 主机</a-button>
      <span class="dim">会话常驻后端 · 刷新页面自动恢复</span>
    </a-space>

    <a-tabs v-model:activeKey="activeTab" type="editable-card" hide-add size="small" @edit="closeTab">
      <a-tab-pane v-for="t in tabs" :key="t.id" :tab="t.label" style="height: 420px">
        <div :ref="el => setTermBox(t.id, el)" style="height: 420px; background: #0d1117"></div>
      </a-tab-pane>
    </a-tabs>

    <!-- SSH 主机管理 -->
    <a-modal v-model:open="showHostMgr" title="SSH 主机管理" :footer="null" :width="860">
      <a-button type="primary" style="margin-bottom: 12px" @click="editHost = { port: 22, authType: 'password' }">添加主机</a-button>
      <a-table :data-source="hosts" :columns="hostCols" size="small" row-key="id" :pagination="false">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'action'">
            <a-space>
              <a-button size="small" @click="editHost = { ...record }">编辑</a-button>
              <a-button size="small" :loading="testingId === record.id" @click="testHost(record)">测试</a-button>
              <a-popconfirm title="删除该主机？" @confirm="delHost(record)"><a-button size="small" danger>删除</a-button></a-popconfirm>
            </a-space>
          </template>
        </template>
      </a-table>
    </a-modal>

    <a-modal v-model:open="hostOpen" :title="editHost.id ? '编辑主机' : '添加主机'" @ok="saveHost">
      <a-form layout="vertical">
        <a-form-item label="名称"><a-input v-model:value="editHost.name" placeholder="生产服务器" /></a-form-item>
        <a-row :gutter="8">
          <a-col :span="12"><a-form-item label="主机"><a-input v-model:value="editHost.host" placeholder="1.2.3.4" /></a-form-item></a-col>
          <a-col :span="12"><a-form-item label="端口"><a-input-number v-model:value="editHost.port" :min="1" :max="65535" style="width: 100%" /></a-form-item></a-col>
        </a-row>
        <a-row :gutter="8">
          <a-col :span="12"><a-form-item label="用户名"><a-input v-model:value="editHost.username" placeholder="root" /></a-form-item></a-col>
          <a-col :span="12">
            <a-form-item label="认证方式">
              <a-select v-model:value="editHost.authType" :options="[{ label: '密码', value: 'password' }, { label: '私钥', value: 'key' }]" />
            </a-form-item>
          </a-col>
        </a-row>
        <a-form-item v-if="editHost.authType === 'password'" label="密码（编辑时留空则不修改）">
          <a-input-password v-model:value="editHost.password" />
        </a-form-item>
        <template v-else>
          <a-form-item label="私钥（编辑时留空则不修改）"><a-textarea v-model:value="editHost.privateKey" :rows="5" /></a-form-item>
          <a-form-item label="私钥口令（可选）"><a-input-password v-model:value="editHost.passphrase" /></a-form-item>
        </template>
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { onMounted, onUnmounted, ref, nextTick } from 'vue'
import { message } from 'ant-design-vue'
import { get, post, del } from '../api'
import { DownOutlined } from '@ant-design/icons-vue'

interface Tab { id: string; label: string; sessionId: string | null; ws: WebSocket | null; term: any; fit: any; created: boolean }

const hosts = ref<any[]>([])
const tabs = ref<Tab[]>([])
const activeTab = ref('')
const showHostMgr = ref(false)
const hostOpen = ref(false)
const editHost = ref<any>({})
const testingId = ref(0)
const termBoxes = new Map<string, HTMLElement>()

const hostCols = [
  { title: '名称', dataIndex: 'name', key: 'name', width: 140 },
  { title: '地址', key: 'addr', width: 220, customRender: (o: any) => `${o.record.username}@${o.record.host}:${o.record.port}` },
  { title: '认证', dataIndex: 'authType', key: 'authType', width: 80 },
  { title: '操作', key: 'action', width: 200 },
]

const setTermBox = (id: string, el: any) => { if (el) termBoxes.set(id, el as HTMLElement) }

const loadHosts = async () => { hosts.value = await get('/api/ssh/hosts') }

// 建立 xterm + ws 连接（新建或 attach 恢复）
const openSession = async (t: Tab, createMsg: Record<string, any> | null) => {
  await nextTick()
  const xterm = await import('@xterm/xterm')
  const fitAddon = await import('@xterm/addon-fit')
  const term = new xterm.Terminal({
    fontSize: 13, cursorBlink: true, scrollback: 5000,
    theme: { background: '#0d1117', foreground: '#c9d1d9' },
  })
  const fit = new fitAddon.FitAddon()
  term.loadAddon(fit)
  const box = termBoxes.get(t.id)
  if (!box) return
  term.open(box)
  fit.fit()
  t.term = term
  t.fit = fit
  setTimeout(() => fit.fit(), 150)

  const proto = location.protocol === 'https:' ? 'wss' : 'ws'
  const ws = new WebSocket(`${proto}://${location.host}/api/ws/terminal`)
  ws.binaryType = 'arraybuffer'
  t.ws = ws
  ws.onopen = () => {
    if (createMsg) ws.send(JSON.stringify(createMsg))
    else ws.send(JSON.stringify({ type: 'attach', sessionId: t.sessionId }))
  }
  ws.onmessage = (ev) => {
    if (ev.data instanceof ArrayBuffer) { term.write(new Uint8Array(ev.data)); return }
    let msg: any
    try { msg = JSON.parse(ev.data) } catch { return }
    switch (msg.type) {
      case 'created':
        t.sessionId = msg.session.id
        term.write('\x1b[36m[已连接 ' + msg.session.label + ']\x1b[0m\r\n')
        break
      case 'attached':
        t.sessionId = msg.sessionId
        if (msg.replay) term.write(msg.replay)
        term.write('\r\n\x1b[36m[已恢复会话]\x1b[0m\r\n')
        break
      case 'error':
        term.write('\x1b[31m[错误] ' + msg.message + '\x1b[0m\r\n')
        break
      case 'closed':
        term.write('\r\n\x1b[31m[会话已关闭]\x1b[0m\r\n')
        break
    }
  }
  term.onData((d: string) => {
    if (ws.readyState === 1) ws.send(new TextEncoder().encode(d))
  })
  term.onResize(({ cols, rows }) => {
    if (ws.readyState === 1) ws.send(JSON.stringify({ type: 'resize', cols, rows }))
  })
  ws.onclose = () => { t.ws = null }
}

const addTab = (label: string) => {
  const t: Tab = { id: Math.random().toString(36).slice(2), label, sessionId: null, ws: null, term: null, fit: null, created: false }
  tabs.value.push(t)
  activeTab.value = t.id
  return t
}

const newSsh = async (h: any) => {
  const t = addTab(`${h.name || h.host}`)
  await openSession(t, { type: 'create', kind: 'ssh', hostId: h.id })
}
const newLocal = async () => {
  const t = addTab('本地终端')
  await openSession(t, { type: 'create', kind: 'local' })
}
const closeTab = async (id: string | number, action: string) => {
  if (action === 'remove') {
    const t = tabs.value.find(x => x.id === id)
    if (!t) return
    t.ws?.close()
    if (t.sessionId) await fetch(`/api/terminal/sessions/${t.sessionId}`, { method: 'DELETE' })
    t.term?.dispose()
    tabs.value = tabs.value.filter(x => x.id !== id)
    if (activeTab.value === t.id && tabs.value.length) activeTab.value = tabs.value[0].id
  } else if (typeof id === 'string') {
    activeTab.value = id
    const t = tabs.value.find(x => x.id === id)
    if (t) setTimeout(() => { t.fit?.fit(); t.term?.focus() }, 100)
  }
}

// 保存/恢复：刷新页面后从后端会话列表恢复标签
const restoreSessions = async () => {
  const list = await get<any[]>('/api/terminal/sessions')
  for (const s of list || []) {
    const t = addTab(s.label)
    t.sessionId = s.id
    await openSession(t, null) // attach 模式
  }
  if (!tabs.value.length) {
    // 空状态提示由下拉按钮引导
  }
}

// SSH 主机 CRUD
const saveHost = async () => {
  await post('/api/ssh/hosts', editHost.value)
  message.success('已保存')
  hostOpen.value = false
  loadHosts()
}
const delHost = async (h: any) => { await del(`/api/ssh/hosts/${h.id}`); loadHosts() }
const testHost = async (h: any) => {
  testingId.value = h.id
  try {
    const r = await post(`/api/ssh/hosts/${h.id}/test`)
    if (r.ok) message.success('连接成功')
    else message.error(r.error)
  } finally { testingId.value = 0 }
}

onMounted(async () => {
  await loadHosts()
  await restoreSessions()
})
onUnmounted(() => tabs.value.forEach(t => t.ws?.close()))
</script>

<style scoped>
.dim { color: #999; font-size: 12px; }
</style>
