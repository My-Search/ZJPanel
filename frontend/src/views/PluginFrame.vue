<template>
  <a-card :body-style="{ padding: 0, height: 'calc(100vh - 130px)' }" class="plugin-frame-card">
    <iframe :src="frameSrc" style="width: 100%; height: 100%; border: 0" :key="frameSrc" />
  </a-card>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useRoute } from 'vue-router'

const route = useRoute()
const frameSrc = computed(() => {
  const id = route.params.id as string
  const sub = (route.query.path as string) || '/'
  return `/plugin-assets/${id}${sub}`
})
</script>

<style scoped>
/* 插件 iframe 加载期间容器跟随主题底色：深色主题下不再白底闪眼 */
.plugin-frame-card :deep(.ant-card-body) {
  background: transparent;
}
html[data-theme='dark'] .plugin-frame-card { background: #141414; }
html[data-theme='dark'] .plugin-frame-card :deep(.ant-card-head) { background: #141414; }
</style>
