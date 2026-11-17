<template>
<!-- 卡片高度不再用 calc(100vh - 130px) 魔法数字：布局层已让页面根撑满剩余视口
     （MainLayout .content），这里整条链用 flex 填满即可，顶栏/边距怎么改都不会再留空档。 -->
  <a-card class="plugin-frame-card" :body-style="{ padding: 0 }">
    <iframe :src="frameSrc" :key="frameSrc" />
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
/* 卡片纵向 flex，body 与 iframe 逐级撑满（全 flex 无百分比链，随主题底色） */
.plugin-frame-card { display: flex; flex-direction: column; }
.plugin-frame-card :deep(.ant-card-body) {
  flex: 1 0 auto;
  display: flex;
  flex-direction: column;
  background: transparent;
}
.plugin-frame-card :deep(iframe) { flex: 1 0 auto; width: 100%; border: 0; }
/* 插件 iframe 加载期间容器跟随主题底色：深色主题下不再白底闪眼 */
html[data-theme='dark'] .plugin-frame-card { background: #141414; }
html[data-theme='dark'] .plugin-frame-card :deep(.ant-card-head) { background: #141414; }
</style>
