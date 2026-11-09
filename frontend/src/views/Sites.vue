<template>
  <a-card title="网站" size="small">
    <template #extra>
      <a-button type="primary" @click="openCreate">新建站点</a-button>
    </template>
    <a-table :data-source="sites" :columns="cols" size="small" row-key="id" :pagination="false">
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'status'">
          <a-tag v-if="record.maintenance" color="orange">维护中</a-tag>
          <a-tag v-else-if="record.enabled === false" color="default">已停用</a-tag>
          <a-tag v-else color="blue">运行中</a-tag>
        </template>
        <template v-else-if="column.key === 'check'">
          <span v-if="record.check">
            <a-tag :color="record.check.status === 'online' ? 'green' : 'red'">{{ record.check.status }}</a-tag>
            <span class="dim">{{ record.check.latencyMs }}ms</span>
          </span>
          <span v-else class="dim">-</span>
        </template>
        <template v-else-if="column.key === 'ssl'">
          <a-tag v-if="record.ssl" color="green">HTTPS</a-tag><a-tag v-else>HTTP</a-tag>
        </template>
        <template v-else-if="column.key === 'compose'">
          <span v-if="record.composeProjectId">Compose #{{ record.composeProjectId }}</span><span v-else class="dim">-</span>
        </template>
        <template v-else-if="column.key === 'action'">
          <a-space>
            <a-button v-if="record.enabled !== false && !record.maintenance" size="small" @click="setState(record, 'maintenance')">维护</a-button>
            <a-button v-if="record.maintenance" size="small" type="primary" ghost @click="setState(record, 'enable')">恢复</a-button>
            <a-button v-else-if="record.enabled !== false" size="small" @click="setState(record, 'disable')">停用</a-button>
            <a-button v-else size="small" type="primary" ghost @click="setState(record, 'enable')">启用</a-button>
            <a-button size="small" @click="editSite(record)">编辑</a-button>
            <a-button size="small" @click="editConf(record)">配置</a-button>
            <a-popconfirm title="确认删除该站点？" @confirm="removeSite(record)">
              <a-button size="small" danger>删除</a-button>
            </a-popconfirm>
          </a-space>
        </template>
      </template>
    </a-table>

    <!-- 新建/编辑 -->
    <a-modal v-model:open="editOpen" :title="form.id ? '编辑站点' : '新建站点'" @ok="saveSite" :width="560">
      <a-form layout="vertical">
        <a-form-item label="域名">
          <a-input v-model:value="form.domain" :disabled="!!form.id" placeholder="panel.example.com" />
        </a-form-item>
        <a-form-item label="类型">
          <a-radio-group v-model:value="form.type" :disabled="!!form.id">
            <a-radio value="proxy">反向代理</a-radio>
            <a-radio value="static">静态目录</a-radio>
          </a-radio-group>
        </a-form-item>
        <a-form-item v-if="form.type === 'proxy'" label="反代目标">
          <a-input v-model:value="form.upstream" placeholder="127.0.0.1:8080" />
        </a-form-item>
        <a-form-item v-if="form.type === 'static'" label="静态目录">
          <a-input v-model:value="form.root" :placeholder="form.id ? form.root : '留空则自动创建 /usr/share/nginx/html/sites/<域名>'" />
        </a-form-item>
        <a-form-item label="HTTPS">
          <a-switch v-model:checked="form.ssl" />
          <span class="dim" style="margin-left: 8px">证书可在「证书」页申请后自动部署</span>
        </a-form-item>
        <a-form-item label="备注"><a-input v-model:value="form.remark" /></a-form-item>
        <a-form-item v-if="form.id" label="站点维护页 HTML（留空使用全局模板）">
          <a-textarea v-model:value="form.maintenanceHtml" :rows="4" placeholder="自定义维护页（支持 {{domain}} {{time}} {{message}} 变量）" />
        </a-form-item>
      </a-form>
    </a-modal>

    <!-- 配置编辑 -->
    <a-modal v-model:open="confOpen" title="站点配置（高级模式）" :width="900" @ok="saveConf" ok-text="保存并重载">
      <div style="height: 480px">
        <AceEditor v-model="confContent" mode="nginx" />
      </div>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import { get, post, put, del } from '../api'
import AceEditor from '../components/AceEditor.vue'

const sites = ref<any[]>([])
const editOpen = ref(false)
const confOpen = ref(false)
const confContent = ref('')
const confSiteId = ref(0)
const form = ref<any>({})

const cols = [
  { title: '域名', dataIndex: 'domain', key: 'domain' },
  { title: '类型', dataIndex: 'type', key: 'type', width: 80 },
  { title: '目标', dataIndex: 'upstream', key: 'upstream', width: 160 },
  { title: 'SSL', key: 'ssl', width: 90 },
  { title: '状态', key: 'status', width: 90 },
  { title: '探测', key: 'check', width: 130 },
  { title: '来源', key: 'compose', width: 110 },
  { title: '操作', key: 'action', width: 280 },
]

const load = async () => { sites.value = await get('/api/sites') }

const openCreate = () => {
  form.value = { domain: '', type: 'proxy', upstream: '', root: '', ssl: false, remark: '' }
  editOpen.value = true
}
const editSite = (r: any) => {
  form.value = { id: r.id, domain: r.domain, type: r.type, upstream: r.upstream, root: r.root, ssl: r.ssl, remark: r.remark, maintenanceHtml: r.maintenanceHtml || '' }
  editOpen.value = true
}
const saveSite = async () => {
  if (form.value.id) await put(`/api/sites/${form.value.id}`, form.value)
  else await post('/api/sites', form.value)
  message.success('已保存（nginx 已重载）')
  editOpen.value = false
  load()
}
const setState = async (r: any, state: string) => {
  await post(`/api/sites/${r.id}/state`, { state })
  message.success('状态已切换')
  load()
}
const removeSite = async (r: any) => {
  await del(`/api/sites/${r.id}`)
  message.success('已删除')
  load()
}
const editConf = async (r: any) => {
  const d = await get(`/api/sites/${r.id}/conf`)
  confContent.value = d.content
  confSiteId.value = r.id
  confOpen.value = true
}
const saveConf = async () => {
  await put(`/api/sites/${confSiteId.value}/conf`, { content: confContent.value })
  message.success('配置已保存并重载')
  confOpen.value = false
}

onMounted(load)
</script>

<style scoped>
.dim { color: #999; font-size: 12px; }
</style>
