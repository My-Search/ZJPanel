/** 将 WebSocket 消息数据统一解码为文本（兼容 Blob/ArrayBuffer/string，规避 [object Blob]）。 */
export function wsMessageToText(data: any): Promise<string> {
  if (data instanceof Blob) return data.text()
  if (data instanceof ArrayBuffer) return Promise.resolve(new TextDecoder().decode(data))
  return Promise.resolve(String(data ?? ''))
}
