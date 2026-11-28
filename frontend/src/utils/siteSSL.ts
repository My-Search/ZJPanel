// 站点 HTTPS 证书来源提交规则（新建/编辑站点共用，见 components/SiteSSLSource.vue）。
// sslSource 是纯前端状态（auto | repo | custom），后端 SaveReq 没有该字段；
// 提交前转换成后端字段：auto → 清空证书字段走约定目录；repo → 只带 sslCertInfoId
// （后端保存时一次性复制到站点约定 SSL 目录）；custom → 成对携带手动路径。
export const buildSiteSavePayload = (form: any): { payload?: any; error?: string } => {
  const { sslSource: rawSource, ...rest } = form
  const payload: any = { ...rest }
  // 未显式点过来源单选时按回填推断：已有手动路径视为 custom，否则 auto
  const sslSource: string = rawSource || (form.sslCertPath ? 'custom' : 'auto')
  if (!form.ssl) {
    payload.sslCertPath = ''
    payload.sslKeyPath = ''
    payload.sslCertInfoId = null
    return { payload }
  }
  if (sslSource === 'repo') {
    if (!form.sslCertInfoId) return { error: '请选择要使用的证书' }
    payload.sslCertInfoId = form.sslCertInfoId
    payload.sslCertPath = ''
    payload.sslKeyPath = ''
    return { payload }
  }
  if (sslSource === 'custom') {
    const cert = String(form.sslCertPath || '').trim()
    const key = String(form.sslKeyPath || '').trim()
    if (!cert || !key) return { error: '手动指定证书时，证书与私钥路径必须成对填写' }
    payload.sslCertPath = cert
    payload.sslKeyPath = key
    payload.sslCertInfoId = null
    return { payload }
  }
  // auto：跟随证书任务部署，清空手动字段
  payload.sslCertPath = ''
  payload.sslKeyPath = ''
  payload.sslCertInfoId = null
  return { payload }
}
