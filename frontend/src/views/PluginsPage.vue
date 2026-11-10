<template>
  <a-card size="small">
    <a-alert type="info" show-icon style="margin-bottom: 12px"
      message="插件机制：内置插件与外部插件（plugin.yaml + main.go，经面板内嵌解释器加载）走同一 SDK。插件可挂菜单/路由/定时任务/MCP 工具（聚合到 /mcp）/证书 DNS 提供商与部署目标。"
    />
    <a-space style="margin-bottom: 12px">
      <a-upload :show-upload-list="false" :custom-request="doInstall" accept=".zip">
        <a-button type="primary">安装插件（zip）</a-button>
      </a-upload>
      <a-button @click="load">刷新</a-button>
    </a-space>

    <a-row :gutter="12">
      <a-col v-for="p in plugins" :key="p.id" :span="8" style="margin-bottom: 12px">
        <a-card size="small" :title="p.name">
          <template #extra>
            <a-tag :color="p.running ? 'green' : p.enabled ? 'orange' : 'default'">{{ p.running ? '运行中' : p.enabled ? '待重启' : '已停用' }}</a-tag>
          </template>
          <p class="dim">{{ p.description || '无描述' }}</p>
          <p class="dim">ID: {{ p.id }} · v{{ p.version || '-' }} · {{ p.builtin ? '内置' : '外部' }}</p>
          <a-space>
            <a-button v-if="!p.enabled" size="small" type="primary" @click="enable(p)">启用</a-button>
            <a-button v-else size="small" @click="disable(p)">停用</a-button>
            <a-button v-if="p.settings?.length" size="small" @click="openSettings(p)">设置</a-button>
            <a-popconfirm v-if="!p.builtin" title="卸载并删除插件目录？" @confirm="uninstall(p)">
              <a-button size="small" danger>卸载</a-button>
            </a-popconfirm>
          </a-space>
        </a-card>
      </a-col>
    </a-row>

    <a-modal v-model:open="settingsOpen" :title="'设置 · ' + (settingsPlugin?.name || '')" @ok="saveSettings">
      <a-form layout="vertical">
        <a-form-item v-for="s in settingsDefs" :key="s.key" :label="s.title" :help="s.help">
          <a-select v-if="s.type === 'select'" v-model:value="settingsValues[s.key]" :options="(s.options || []).map((o: string) => ({ label: o, value: o }))" />
          <a-switch v-else-if="s.type === 'switch'" v-model:checked="settingsValues[s.key]" />
          <a-textarea v-else-if="s.type === 'textarea'" v-model:value="settingsValues[s.key]" :rows="3" />
          <a-input-password v-else-if="s.type === 'password'" v-model:value="settingsValues[s.key]" />
          <a-input v-else v-model:value="settingsValues[s.key]" />
        </a-form-item>
      </a-form>
    </a-modal>
  </a-card>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { message } from 'ant-design-vue'
import { get, post, del, upload } from '../api'
import { usePluginMenu } from '../store/pluginMenu'

const plugins = ref<any[]>([])
const settingsOpen = ref(false)
const settingsPlugin = ref<any>(null)
const settingsDefs = ref<any[]>([])
const settingsValues = ref<Record<string, string>>({})

const load = async () => { plugins.value = await get('/api/plugins') }
// 插件安装/启停/卸载会影响侧栏菜单挂载，变更后联动强制刷新菜单缓存
const menus = usePluginMenu()
const reloadWithMenu = () => { load(); menus.load(true) }
const enable = async (p: any) => { await post(`/api/plugins/${p.id}/enable`); message.success('已启用'); reloadWithMenu() }
const disable = async (p: any) => { await post(`/api/plugins/${p.id}/disable`); message.success('已停用'); reloadWithMenu() }
const uninstall = async (p: any) => { await del(`/api/plugins/${p.id}`); message.success('已卸载'); reloadWithMenu() }
const doInstall = async (opt: any) => {
  try {
    const r = await upload('/api/plugins/install', opt.file)
    message.success(`已安装 ${r.id}，点击启用运行`)
    reloadWithMenu()
  } catch { /* 拦截器提示 */ }
}
const openSettings = async (p: any) => {
  settingsPlugin.value = p
  const d = await get(`/api/plugins/${p.id}/settings`)
  settingsDefs.value = d.settings || []
  settingsValues.value = d.values || {}
  settingsOpen.value = true
}
const saveSettings = async () => {
  await post(`/api/plugins/${settingsPlugin.value.id}/settings`, settingsValues.value)
  message.success('已保存并重启插件')
  settingsOpen.value = false
  load()
}

onMounted(load)
</script>

<style scoped>
.dim { color: #999; font-size: 12px; margin: 4px 0; }
</style>
