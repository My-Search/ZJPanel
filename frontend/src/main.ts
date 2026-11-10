import { createApp } from 'vue'
import { createPinia } from 'pinia'
import Antd from 'ant-design-vue'
import 'ant-design-vue/dist/reset.css'
// xterm 终端基础样式（缺省时内部 helper textarea 会以原生输入框形态暴露、终端渲染错乱）
import '@xterm/xterm/css/xterm.css'
import App from './App.vue'
import router from './router'

const app = createApp(App)
app.use(createPinia())
app.use(router)
app.use(Antd)
app.mount('#app')
