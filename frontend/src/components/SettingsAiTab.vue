<template>
  <a-row :gutter="16">
    <a-col :xs="24" :md="10">
      <a-card size="small" title="模型配置（OpenAI 兼容）" class="ai-card" style="margin-bottom: 16px">
        <a-form layout="vertical">
          <a-form-item label="API 地址 BaseURL">
            <a-input v-model:value="llm.baseURL" placeholder="https://api.deepseek.com" />
          </a-form-item>
          <a-form-item :label="llm.apiKeySet ? 'API Key（留空沿用）' : 'API Key'">
            <a-input-password v-model:value="llmApiKey" :placeholder="llm.apiKeySet ? '••••••' : 'sk-…（本地模型如 Ollama 可留空）'" />
          </a-form-item>
          <a-form-item label="模型名">
            <a-input v-model:value="llm.model" placeholder="deepseek-chat / qwen-plus / glm-4-flash …" />
          </a-form-item>
          <a-space>
            <a-button :loading="testing" @click="doTest">测试连通</a-button>
            <a-button type="primary" :loading="savingLLM" @click="saveLLM">保存模型配置</a-button>
          </a-space>
          <p class="ai-hint">兼容 DeepSeek / 通义 / GLM / Kimi / OpenRouter / one-api / Ollama 等 OpenAI 协议服务；带版本路径请填完整（如 GLM 填 https://open.bigmodel.cn/api/paas/v4）。</p>
        </a-form>
      </a-card>

      <a-card size="small" title="长期记忆" class="ai-card">
        <div class="ai-mem-bar">
          <a-input-search v-model:value="memQuery" placeholder="搜索记忆" size="small" @search="loadMemories(memQuery)" />
        </div>
        <div class="ai-mem-list">
          <div v-for="m in memories" :key="m.id" class="ai-mem-row">
            <div class="ai-mem-main">
              <div class="ai-mem-key">{{ m.key }} <a-tag v-if="m.scope === 'host'" style="margin-right: 0">主机#{{ m.refId }}</a-tag></div>
              <div class="ai-mem-content">{{ m.content }}</div>
            </div>
            <a-popconfirm title="删除这条记忆？" @confirm="deleteMemory(m.id)">
              <a class="ai-mem-del"><DeleteOutlined /></a>
            </a-popconfirm>
          </div>
          <div v-if="!memories.length" class="ai-mem-empty">暂无记忆（Agent 会话中会自动积累）</div>
        </div>
      </a-card>
    </a-col>

    <a-col :xs="24" :md="14">
      <a-card size="small" title="管理 Agent" class="ai-card">
        <a-alert
          type="info"
          show-icon
          message="事件触发：勾选的事件发生时，Agent 会自动创建会话分析并按防御模式行动。主动防御默认关闭，请按需开启。"
          style="margin-bottom: 14px"
        />
        <a-form layout="vertical">
          <a-form-item label="事件自动分析">
            <a-switch v-model:checked="agent.eventEnabled" />
            <span class="ai-switch-hint">开启后按下方勾选的事件自动创建会话</span>
          </a-form-item>
          <a-form-item label="监听事件">
            <div class="ai-ev-list">
              <a-checkbox
                v-for="e in settings?.events || []"
                :key="e.topic"
                :checked="!!agent.events[e.topic]"
                @change="(ev: any) => (agent.events[e.topic] = !!ev.target.checked)"
              >{{ e.desc }}<span class="ai-ev-topic">{{ e.topic }}</span></a-checkbox>
            </div>
          </a-form-item>
          <a-form-item label="防御模式">
            <a-radio-group v-model:value="agent.defenseMode">
              <a-radio value="observe">仅观察（分析+通知+建议）</a-radio>
              <a-radio value="confirm">执行前确认（处置需人工确认）</a-radio>
              <a-radio value="auto">自动处置（全程审计）</a-radio>
            </a-radio-group>
          </a-form-item>
          <a-form-item>
            <a-switch v-model:checked="agent.allowSSHExec" />
            <span class="ai-switch-hint">允许 Agent 通过 SSH 在主机上执行任意命令（高危，默认关闭；开启后仍默认走「执行前确认」）</span>
          </a-form-item>
          <a-button type="primary" :loading="savingAgent" @click="saveAgent">保存 Agent 配置</a-button>
        </a-form>
      </a-card>
    </a-col>
  </a-row>
</template>

<script setup lang="ts">
import { onMounted, ref, toRefs } from 'vue'
import { message } from 'ant-design-vue'
import { DeleteOutlined } from '@ant-design/icons-vue'
import { useOpsAgent } from '../store/opsagent'

const api = useOpsAgent()
// 模板一律消费 toRefs 拆出的顶层 ref（自动解包的类型检查路径最稳），
// store 对象只用于脚本内调方法，避免模板上下文类型退化
const { memories, settings } = toRefs(api)
const loadMemories = (q?: string) => api.loadMemories(q)
const deleteMemory = (id: number) => api.deleteMemory(id)

const llm = ref<{ baseURL: string; model: string; apiKeySet: boolean }>({ baseURL: '', model: '', apiKeySet: false })
const llmApiKey = ref('')
const agent = ref<{ defenseMode: string; eventEnabled: boolean; events: Record<string, boolean>; allowSSHExec: boolean }>({
  defenseMode: 'observe', eventEnabled: false, events: {}, allowSSHExec: false,
})
const savingLLM = ref(false)
const savingAgent = ref(false)
const testing = ref(false)
const memQuery = ref('')

const load = async () => {
  try {
    await api.loadSettings()
    if (!settings.value) return
    llm.value = { baseURL: settings.value.llm.baseURL, model: settings.value.llm.model, apiKeySet: settings.value.llm.apiKeySet }
    agent.value = {
      defenseMode: settings.value.agent.defenseMode || 'observe',
      eventEnabled: !!settings.value.agent.eventEnabled,
      events: { ...(settings.value.agent.events || {}) },
      allowSSHExec: !!settings.value.agent.allowSSHExec,
    }
    api.loadMemories()
  } catch { /* ignore */ }
}
const saveLLM = async () => {
  if (!llm.value.baseURL.trim() || !llm.value.model.trim()) {
    message.warning('BaseURL 与模型名必填'); return
  }
  savingLLM.value = true
  try {
    await api.saveSettings({ llm: { baseURL: llm.value.baseURL, apiKey: llmApiKey.value, model: llm.value.model } })
    llmApiKey.value = ''
    message.success('模型配置已保存')
  } catch (e: any) {
    message.error(e?.response?.data?.error || '保存失败')
  } finally { savingLLM.value = false }
}
const saveAgent = async () => {
  savingAgent.value = true
  try {
    await api.saveSettings({ agent: agent.value })
    message.success('Agent 配置已保存')
  } catch (e: any) {
    message.error(e?.response?.data?.error || '保存失败')
  } finally { savingAgent.value = false }
}
const doTest = async () => {
  testing.value = true
  try {
    const r = await api.testLLM()
    r.ok ? message.success('模型连接正常') : message.error(r.error || '连接失败')
  } catch (e: any) {
    message.error(e?.response?.data?.error || '连接失败')
  } finally { testing.value = false }
}

onMounted(load)
</script>

<style scoped>
.ai-card { margin-bottom: 16px; }
.ai-hint { font-size: 12px; color: var(--color-slate); margin: 10px 0 0; }
.ai-switch-hint { font-size: 12px; color: var(--color-slate); margin-left: 8px; }
.ai-ev-list { display: flex; flex-direction: column; gap: 6px; border: 1px solid rgba(128,128,128,.2); border-radius: 8px; padding: 8px 12px; max-height: 220px; overflow: auto; }
.ai-ev-topic { font-size: 11px; color: var(--color-slate); font-family: ui-monospace, Consolas, monospace; margin-left: 6px; }
.ai-mem-bar { margin-bottom: 8px; }
.ai-mem-list { max-height: 260px; overflow: auto; border: 1px solid rgba(128,128,128,.2); border-radius: 8px; padding: 2px 12px; }
.ai-mem-row { display: flex; align-items: flex-start; gap: 8px; padding: 7px 0; border-bottom: 1px solid rgba(128,128,128,.12); }
.ai-mem-row:last-child { border-bottom: none; }
.ai-mem-main { flex: 1; min-width: 0; }
.ai-mem-key { font-size: 12.5px; font-weight: 600; font-family: ui-monospace, Consolas, monospace; }
.ai-mem-content { font-size: 12px; color: var(--color-slate); word-break: break-all; margin-top: 2px; }
.ai-mem-del { font-size: 12px; color: var(--color-slate); }
.ai-mem-del:hover { color: #ef4444; }
.ai-mem-empty { text-align: center; color: var(--color-slate); font-size: 12px; padding: 20px 0; }
</style>
