<template>
  <a-card title="Compose 项目导入" size="small">
    <template #extra>
      <a-button type="primary" @click="scan">扫描目录</a-button>
    </template>

    <a-table :data-source="projects" :columns="cols" size="small" row-key="dir" :pagination="false">
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'services'">
          <a-tag v-for="s in record.services" :key="s.name" color="blue" style="margin-bottom: 4px">
            {{ s.name }}{{ s.ports?.length ? ' · ' + s.ports.join(', ') : '' }}
          </a-tag>
        </template>
        <template v-else-if="column.key === 'status'">
          <a-tag v-if="!record.exists" color="red">目录已消失</a-tag>
          <a-tag v-else-if="record.imported" color="green">已导入（站点 #{{ record.siteId }}）</a-tag>
          <a-tag v-else color="orange">未导入</a-tag>
        </template>
        <template v-else-if="column.key === 'action'">
          <a-space>
            <a-button v-if="!record.imported && record.exists" size="small" type="primary" @click="openImport(record)">导入为网站</a-button>
            <a-button size="small" :disabled="!record.exists" @click="action(record, 'up')">up</a-button>
            <a-button size="small" :disabled="!record.exists" @click="action(record, 'down')">down</a-button>
            <a-button size="small" :disabled="!record.exists" @click="action(record, 'restart')">restart</a-button>
            <a-button size="small" :disabled="!record.id" @click="refresh(record)">刷新解析</a-button>
          </a-space>
        </template>
      </template>
    </a-table>

    <a-modal v-model:open="importOpen" title="导入为网站" @ok="doImport">
      <a-form layout="vertical">
        <a-form-item label="项目目录"><a-input :value="importForm.dir" disabled /></a-form-item>
        <a-form-item label="选择服务">
          <a-select v-model:value="importForm.service" :options="serviceOptions" placeholder="选择服务" />
        </a-form-item>
        <a-form-item label="反代端口（该服务发布到宿主机的端口）">
          <a-select v-model:value="importForm.port" :options="portOptions" placeholder="如 8080（发布端口）" />
        </a-form-item>
        <a-form-item label="站点域名"><a-input v-model:value="importForm.domain" placeholder="app.example.com" /></a-form-item>
        <a-form-item label="HTTPS"><a-switch v-model:checked="importForm.ssl" /></a-form-item>
      </a-form>
    </a-modal>

    <a-modal v-model:open="outOpen" :title="outTitle" :footer="null" :width="720">
      <pre style="max-height: 400px; overflow: auto; background: #111; color: #0f0; padding: 12px; font-size: 12px">{{ outContent || '(无输出)' }}</pre>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import { get, post } from '../api'

const projects = ref<any[]>([])
const importOpen = ref(false)
const importForm = ref<any>({})
const outOpen = ref(false)
const outTitle = ref('')
const outContent = ref('')

const cols = [
  { title: '项目', dataIndex: 'name', key: 'name', width: 140 },
  { title: '目录', dataIndex: 'dir', key: 'dir' },
  { title: '服务 / 端口', key: 'services' },
  { title: '状态', key: 'status', width: 130 },
  { title: '操作', key: 'action', width: 360 },
]

const serviceOptions = computed(() => (importForm.value.raw?.services || []).map((s: any) => ({ label: s.name, value: s.name })))
const portOptions = computed(() => {
  const ports: string[] = []
  for (const s of importForm.value.raw?.services || []) for (const p of s.ports || []) ports.push(p)
  return [...new Set(ports)].map(p => ({ label: p, value: p }))
})

const scan = async () => {
  projects.value = await get('/api/compose/scan')
  message.success(`扫描到 ${projects.value.length} 个项目`)
}
const openImport = (r: any) => {
  importForm.value = { dir: r.dir, raw: r, service: r.services?.[0]?.name, port: r.services?.[0]?.ports?.[0]?.split(':')[0], domain: '', ssl: false }
  importOpen.value = true
}
const doImport = async () => {
  await post('/api/compose/import', {
    dir: importForm.value.dir, service: importForm.value.service,
    port: importForm.value.port, domain: importForm.value.domain, ssl: importForm.value.ssl,
  })
  message.success('已导入并创建站点')
  importOpen.value = false
  scan()
}
const action = async (r: any, act: string) => {
  const res = await post(`/api/compose/${r.id}/action`, { action: act })
  outTitle.value = `${r.name} · ${act}`
  outContent.value = (res.output || '') + (res.error ? '\n错误: ' + res.error : '')
  outOpen.value = true
}
const refresh = async (r: any) => {
  const res = await post(`/api/compose/${r.id}/refresh`, {})
  if (res.error) message.error(res.error)
  else message.success(res.message || '已刷新')
  scan()
}

onMounted(scan)
</script>
