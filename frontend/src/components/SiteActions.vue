<template>
  <a-space :size="6" wrap>
    <a-button v-if="site.enabled !== false && !site.maintenance" size="small" @click="setState('maintenance')">维护</a-button>
    <a-button v-if="site.maintenance" size="small" type="primary" ghost @click="setState('enable')">恢复</a-button>
    <a-button v-else-if="site.enabled !== false" size="small" @click="setState('disable')">停用</a-button>
    <a-button v-else size="small" type="primary" ghost @click="setState('enable')">启用</a-button>
    <a-button size="small" @click="openEdit">编辑</a-button>
    <a-button size="small" @click="openConf">配置</a-button>
    <a-popconfirm :title="deleteTitle" @confirm="remove">
      <a-button size="small" danger>删除</a-button>
    </a-popconfirm>
  </a-space>

  <!-- 编辑站点 -->
  <a-modal v-model:open="editOpen" title="编辑站点" @ok="saveSite" :width="560">
    <a-form layout="vertical">
      <a-form-item label="域名">
        <a-input v-model:value="form.domain" disabled placeholder="panel.example.com" />
      </a-form-item>
      <a-form-item label="类型">
        <a-radio-group v-model:value="form.type" disabled>
          <a-radio value="proxy">反向代理</a-radio>
          <a-radio value="static">静态目录</a-radio>
        </a-radio-group>
      </a-form-item>
      <a-form-item v-if="form.type === 'proxy'" label="反代目标">
        <a-input v-model:value="form.upstream" placeholder="127.0.0.1:8080" />
      </a-form-item>
      <a-form-item v-if="form.type === 'static'" label="静态目录">
        <a-input v-model:value="form.root" :placeholder="form.root || '留空则自动创建 /usr/share/nginx/html/sites/<域名>'" />
      </a-form-item>
      <a-form-item label="HTTPS">
        <a-switch v-model:checked="form.ssl" />
        <span class="dim" style="margin-left: 8px">证书可在「证书」页申请后自动部署</span>
      </a-form-item>
      <a-form-item label="备注"><a-input v-model:value="form.remark" /></a-form-item>
      <a-form-item label="站点维护页 HTML（留空使用全局模板）">
        <a-textarea v-model:value="form.maintenanceHtml" :rows="4" placeholder="自定义维护页（支持 {{domain}} {{time}} {{message}} 变量）" />
      </a-form-item>
    </a-form>
  </a-modal>

  <!-- 配置编辑（nginx conf 高级模式） -->
  <a-modal v-model:open="confOpen" title="站点配置（高级模式）" :width="900" @ok="saveConf" ok-text="保存并重载">
    <div style="height: 480px">
      <AceEditor v-model="confContent" mode="nginx" />
    </div>
  </a-modal>
</template>

<script setup lang="ts">
// SiteActions 站点行操作（维护/恢复、停用/启用、编辑、配置、删除）。
// 「全部网站」页与「Compose 应用 → 项目 → 网站」共用，保证两处操作集一致。
// 所有操作完成后 emit('changed')，由父组件决定刷新方式。
import { computed, ref, watch } from 'vue'
import { message } from 'ant-design-vue'
import { get, post, put, del } from '../api'
import AceEditor from './AceEditor.vue'

const props = defineProps<{ site: any; deleteTitle?: string }>()
const emit = defineEmits<{ (e: 'changed'): void }>()

const editOpen = ref(false)
const confOpen = ref(false)
const confContent = ref('')
const form = ref<any>({})

const deleteTitle = computed(() => props.deleteTitle || '确认删除该站点？（zjpanel.json 将同步更新）')

watch(() => props.site?.id, () => {
  editOpen.value = false
  confOpen.value = false
})

const setState = async (state: string) => {
  try {
    await post(`/api/sites/${props.site.id}/state`, { state })
    message.success('状态已切换')
    emit('changed')
  } catch { /* 失败提示已由 axios 拦截器输出 */ }
}

const openEdit = () => {
  const s = props.site || {}
  // 回填完整字段：后端 Update 会覆盖 Type/Upstream/Root/SSL/MaintenanceHTML/Remark，
  // 未回填的字段会被清空；composeProjectId 不传，后端保留原关联
  form.value = {
    id: s.id, domain: s.domain, type: s.type, upstream: s.upstream, root: s.root,
    ssl: !!s.ssl, remark: s.remark || '', maintenanceHtml: s.maintenanceHtml || '',
  }
  editOpen.value = true
}

const saveSite = async () => {
  try {
    await put(`/api/sites/${form.value.id}`, form.value)
    message.success('已保存（nginx 已重载）')
    editOpen.value = false
    emit('changed')
  } catch { /* 失败提示已由 axios 拦截器输出 */ }
}

const openConf = async () => {
  try {
    const d = await get(`/api/sites/${props.site.id}/conf`)
    if (!d?.content) { message.error('读取站点配置失败'); return }
    confContent.value = d.content
    confOpen.value = true
  } catch { /* 失败提示已由 axios 拦截器输出 */ }
}

const saveConf = async () => {
  try {
    await put(`/api/sites/${props.site.id}/conf`, { content: confContent.value })
    message.success('配置已保存并重载')
    confOpen.value = false
    emit('changed')
  } catch { /* 失败提示已由 axios 拦截器输出 */ }
}

const remove = async () => {
  try {
    await del(`/api/sites/${props.site.id}`)
    message.success('已删除')
    emit('changed')
  } catch { /* 失败提示已由 axios 拦截器输出 */ }
}
</script>

<style scoped>
.dim { color: var(--color-slate); font-size: 12px; }
</style>
