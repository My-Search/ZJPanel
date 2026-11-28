<template>
  <template v-if="form.ssl">
    <a-form-item label="证书来源">
      <a-radio-group v-model:value="source">
        <a-radio value="auto">自动（跟随证书任务部署）</a-radio>
        <a-radio value="repo">从证书仓库选择</a-radio>
        <a-radio value="custom">手动指定路径</a-radio>
      </a-radio-group>
    </a-form-item>
    <a-form-item v-if="source === 'repo'" label="选择已申请的证书" required>
      <a-select
        v-model:value="form.sslCertInfoId"
        :options="certOpts"
        :loading="certsLoading"
        placeholder="选择证书（按域名与到期时间）"
        show-search
        option-filter-prop="label"
      />
      <div class="ssl-hint">
        保存时把该证书复制到站点证书目录（{{ nginxPrefix }}/ssl/{{ form.domain || '站点域名' }}/）：
        同域名证书续期后自动更新；跨域名为一次性复制，续期后需回到此处重新选择一次。
      </div>
    </a-form-item>
    <template v-if="source === 'custom'">
      <a-form-item label="证书文件（fullchain.pem）" required>
        <a-input-group compact class="path-input-group">
          <a-input v-model:value="form.sslCertPath" placeholder="如 /etc/nginx/ssl/example.com/fullchain.pem" />
          <a-button @click="pick('sslCertPath')">浏览</a-button>
        </a-input-group>
      </a-form-item>
      <a-form-item label="私钥文件（privkey.pem）" required>
        <a-input-group compact class="path-input-group">
          <a-input v-model:value="form.sslKeyPath" placeholder="如 /etc/nginx/ssl/example.com/privkey.pem" />
          <a-button @click="pick('sslKeyPath')">浏览</a-button>
        </a-input-group>
      </a-form-item>
      <div class="ssl-hint">
        路径为面板所在主机（容器内）路径，nginx 直接读取；证书与私钥必须成对填写，保存时会校验文件存在。
      </div>
    </template>
    <div v-else-if="source === 'auto'" class="ssl-hint">
      证书可在「证书」页申请后自动部署；本站点将引用 {{ nginxPrefix }}/ssl/{{ form.domain || '站点域名' }}/fullchain.pem。
    </div>
    <FileBrowserModal v-model:open="pickerOpen" mode="file" title="选择证书 / 私钥文件" @picked="onPicked" />
  </template>
</template>

<script setup lang="ts">
// SiteSSLSource 站点 HTTPS 证书来源选择块（新建/编辑站点共用）。
// 直接操作父组件传入的响应式 form 对象（ssl/sslCertPath/sslKeyPath/sslCertInfoId）；
// sslSource 为纯前端状态，提交前经 utils/siteSSL.buildSiteSavePayload 转换为后端字段。
import { ref, computed, watch } from 'vue'
import { getQuiet } from '../api'
import FileBrowserModal from './FileBrowserModal.vue'

const props = defineProps<{ form: any }>()

// 证书来源持久记在 form.sslSource 上（提交时由 utils/siteSSL 转换为后端字段）；
// 未初始化时回填：手动路径已存在 → custom，否则 auto。
const source = computed({
  get: () => props.form.sslSource || (props.form.sslCertPath ? 'custom' : 'auto'),
  set: (v: string) => { props.form.sslSource = v },
})

// 提示里的约定路径前缀取真实配置（/api/settings），失败回退默认值
const nginxPrefix = ref('/etc/nginx')
getQuiet<any>('/api/settings').then((s) => { if (s?.nginxPrefix) nginxPrefix.value = s.nginxPrefix }).catch(() => {})

// 证书仓库下拉：首次切到「仓库选择」时懒加载，仅 active 证书
const certInfos = ref<any[]>([])
const certsLoading = ref(false)
const certsLoaded = ref(false)
const loadCerts = async () => {
  if (certsLoaded.value) return
  certsLoading.value = true
  try {
    const list = await getQuiet<any[]>('/api/cert/infos')
    certInfos.value = (list || []).filter((c) => c.status === 'active')
    certsLoaded.value = true
  } finally {
    certsLoading.value = false
  }
}
const parseDomains = (s: string) => { try { return JSON.parse(s) } catch { return [s] } }
const certOpts = computed(() => certInfos.value.map((c) => ({
  label: `${parseDomains(c.domains).join(', ')}（到期 ${String(c.notAfter).slice(0, 10)}）`,
  value: c.id,
})))

// 切换来源时联动清理字段：非 repo 清空证书选择，非 custom 清空手动路径
watch(source, (v) => {
  if (v === 'repo') loadCerts()
  else props.form.sslCertInfoId = null
  if (v !== 'custom') {
    props.form.sslCertPath = ''
    props.form.sslKeyPath = ''
  }
})

// 本机文件浏览（证书/私钥路径选择）
const pickerOpen = ref(false)
const pickerKey = ref('sslCertPath')
const pick = (key: 'sslCertPath' | 'sslKeyPath') => {
  pickerKey.value = key
  pickerOpen.value = true
}
const onPicked = (path: string) => { props.form[pickerKey.value] = path }
</script>

<style scoped>
.ssl-hint { font-size: 12px; line-height: 1.7; color: var(--color-slate); margin-top: -6px; margin-bottom: 8px; word-break: break-all; }
.path-input-group { display: flex; width: 100%; }
.path-input-group .ant-input { flex: 1; }
</style>
