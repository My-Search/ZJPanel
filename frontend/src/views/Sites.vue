<template>
  <a-card title="网站" size="small">
    <template #extra>
      <a-button type="primary" @click="openCreate">新建站点</a-button>
    </template>
    <a-table :data-source="sortedSites" :columns="cols" size="small" row-key="id" :pagination="false" :scroll="{ x: 1100 }">
      <template #bodyCell="{ column, record }">
        <template v-if="column.key === 'status'">
          <a-tag v-if="record.disabled" color="default" title="所属应用已标记停用：掉线不告警">应用停用</a-tag>
          <a-tag v-else-if="record.maintenance" color="orange">维护中</a-tag>
          <a-tag v-else-if="record.enabled === false" color="default">已停用</a-tag>
          <a-tag v-else color="blue">运行中</a-tag>
        </template>
        <template v-else-if="column.key === 'check'">
          <template v-if="record.check">
            <a-tag v-if="record.disabled && record.check.status !== 'online'" color="default">不告警</a-tag>
            <a-tag v-else :color="record.check.status === 'online' ? 'green' : 'red'">{{ record.check.status }}</a-tag>
            <span v-if="record.check.status === 'online'" class="dim">{{ record.check.latencyMs }}ms</span>
          </template>
          <span v-else class="dim">-</span>
        </template>
        <template v-else-if="column.key === 'ssl'">
          <a-tag v-if="record.ssl" color="green">HTTPS</a-tag><a-tag v-else>HTTP</a-tag>
        </template>
        <template v-else-if="column.key === 'compose'">
          <span v-if="record.composeProjectId">Compose #{{ record.composeProjectId }}</span><span v-else class="dim">-</span>
        </template>
        <template v-else-if="column.key === 'action'">
          <SiteActions :site="record" delete-title="确认删除该站点？" @changed="load" />
        </template>
      </template>
    </a-table>

    <!-- 新建站点（编辑/配置等行内操作见 SiteActions 组件） -->
    <a-modal v-model:open="editOpen" title="新建站点" @ok="saveSite" :width="560">
      <a-form layout="vertical">
        <a-form-item label="域名">
          <a-input v-model:value="form.domain" placeholder="panel.example.com" />
        </a-form-item>
        <a-form-item label="类型">
          <a-radio-group v-model:value="form.type">
            <a-radio value="proxy">反向代理</a-radio>
            <a-radio value="static">静态目录</a-radio>
          </a-radio-group>
        </a-form-item>
        <a-form-item v-if="form.type === 'proxy'" label="反代目标">
          <a-input v-model:value="form.upstream" placeholder="127.0.0.1:8080" />
        </a-form-item>
        <a-form-item v-if="form.type === 'static'" label="静态目录">
          <a-input v-model:value="form.root" placeholder="留空则自动创建 /usr/share/nginx/html/sites/<域名>" />
        </a-form-item>
        <a-form-item label="HTTPS">
          <a-switch v-model:checked="form.ssl" />
          <span class="dim" style="margin-left: 8px">证书可在「证书」页申请后自动部署</span>
        </a-form-item>
        <a-form-item label="备注"><a-input v-model:value="form.remark" /></a-form-item>
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { onActivated, onMounted, computed, ref } from 'vue'
import { message } from 'ant-design-vue'
import { get, post } from '../api'
import { sortSites } from '../utils/siteOrder'
import SiteActions from '../components/SiteActions.vue'

const sites = ref<any[]>([])
const editOpen = ref(false)
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
// 与仪表盘「站点状态」同款排序（共享 utils/siteOrder）
const sortedSites = computed(() => sortSites(sites.value))

const openCreate = () => {
  form.value = { domain: '', type: 'proxy', upstream: '', root: '', ssl: false, remark: '' }
  editOpen.value = true
}
const saveSite = async () => {
  try {
    await post('/api/sites', form.value)
    message.success('已保存（nginx 已重载）')
    editOpen.value = false
    load()
  } catch { /* 失败提示已由 axios 拦截器输出 */ }
}

onMounted(load)
// 保活：重新激活时后台刷新站点数据（首次挂载时两个钩子各拉一次，幂等无害）
onActivated(load)
</script>

<style scoped>
.dim { color: var(--color-slate); font-size: 12px; }
</style>
