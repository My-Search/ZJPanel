<template>
  <a-card size="small">
    <a-alert type="info" show-icon style="margin-bottom: 12px"
      :message="`MCP 端点（聚合核心 + 插件工具）：POST ${origin}/mcp`"
      description="鉴权：请求头 X-Api-Key 或 Authorization: Bearer <API Key>。协议：Streamable HTTP（JSON-RPC 2.0，支持 initialize / tools/list / tools/call）。" />

    <a-card title="API Keys" size="small" style="margin-bottom: 12px">
      <template #extra>
        <a-button type="primary" size="small" @click="createKey">生成新 Key</a-button>
      </template>
      <a-alert v-if="newKey" type="success" show-icon style="margin-bottom: 12px"
        :message="`新 Key（仅展示一次，请立即保存）：${newKey}`" />
      <a-table :data-source="keys" :columns="keyCols" size="small" row-key="id" :pagination="false">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'prefix'"><code>{{ record.prefix }}…</code></template>
          <template v-else-if="column.key === 'action'">
            <a-popconfirm title="吊销该 Key？" @confirm="revoke(record)"><a-button size="small" danger>吊销</a-button></a-popconfirm>
          </template>
        </template>
      </a-table>
    </a-card>

    <a-card title="工具清单" size="small" style="margin-bottom: 12px">
      <a-table :data-source="tools" :columns="toolCols" size="small" row-key="name" :pagination="false">
        <template #bodyCell="{ column, record }">
          <template v-if="column.key === 'owner'">
            <a-tag :color="record.owner === 'core' ? 'blue' : 'purple'">{{ record.owner === 'core' ? '核心' : '插件 ' + record.owner }}</a-tag>
          </template>
        </template>
      </a-table>
    </a-card>

    <a-card title="快速接入（Claude / 任意 MCP 客户端）" size="small">
      <pre class="code-block">{
  "mcpServers": {
    "zjpanel": {
      "url": "{{ origin }}/mcp",
      "headers": { "X-Api-Key": "<你的 API Key>" }
    }
  }
}</pre>
    </a-card>
  </a-card>
</template>

<script setup lang="ts">
import { onActivated, onMounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import { get, post, del, fmtTime } from '../api'

const keys = ref<any[]>([])
const tools = ref<any[]>([])
const newKey = ref('')
const origin = location.origin

const keyCols = [
  { title: '名称', dataIndex: 'name', key: 'name', width: 180 },
  { title: '前缀', key: 'prefix', width: 160 },
  { title: '创建时间', dataIndex: 'createdAt', key: 'createdAt', width: 170, customRender: (o: any) => fmtTime(o.value) },
  { title: '最近使用', dataIndex: 'lastUsedAt', key: 'lastUsedAt', width: 170, customRender: (o: any) => o.value ? fmtTime(o.value) : '未使用' },
  { title: '操作', key: 'action', width: 80 },
]
const toolCols = [
  { title: '工具名', dataIndex: 'name', key: 'name', width: 260 },
  { title: '来源', key: 'owner', width: 120 },
  { title: '说明', dataIndex: 'description', key: 'description' },
]

const load = async () => {
  keys.value = await get('/api/mcp/keys')
  tools.value = await get('/api/mcp/tools')
}
const createKey = async () => {
  const r = await post('/api/mcp/keys', { name: 'key-' + new Date().toISOString().slice(0, 10) })
  newKey.value = r.key
  message.success('Key 已生成')
  load()
}
const revoke = async (k: any) => { await del(`/api/mcp/keys/${k.id}`); load() }

onMounted(load)
onActivated(load)
</script>
