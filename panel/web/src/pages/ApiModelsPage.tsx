import { useCallback, useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { api } from '../api'
import { desktopCommand, hasDesktopBridge } from '../desktopBridge'
import type { DesktopConfigView, Model } from '../types'
import { Alert, modelLabel, modelName, reasoningEffortLabel, Spinner } from '../ui'

export function apiExample(baseURL: string, model: string): string {
  return `curl ${baseURL}/chat/completions \\\n  -H 'Authorization: Bearer YOUR_API_KEY' \\\n  -H 'Content-Type: application/json' \\\n  -d '${JSON.stringify({ model, messages: [{ role: 'user', content: '你好' }], stream: false }).split("'").join("'\\''")}'`
}

export default function ApiModelsPage() {
  const [config, setConfig] = useState<DesktopConfigView | null>(null)
  const [models, setModels] = useState<Model[]>([])
  const [selected, setSelected] = useState('')
  const [refreshed, setRefreshed] = useState<Date | null>(null)
  const [busy, setBusy] = useState(false)
  const [keyBusy, setKeyBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const rotation = useRef<AbortController | null>(null)
  useEffect(() => () => { rotation.current?.abort(); rotation.current = null }, [])
  const load = useCallback(async () => {
    setBusy(true); setError(null)
    const [configuration, list] = await Promise.allSettled([api.desktopConfig(), api.models()])
    if (configuration.status === 'fulfilled') setConfig(configuration.value)
    if (list.status === 'fulfilled') {
      const data = list.value.data ?? []
      setModels(data); setSelected((old) => data.some((model) => model.id === old) ? old : (data[0]?.id ?? ''))
      setRefreshed(new Date())
    }
    const failed = [configuration, list].find((result) => result.status === 'rejected')
    if (failed?.status === 'rejected') setError(failed.reason instanceof Error ? failed.reason.message : '接入信息读取失败')
    setBusy(false)
  }, [])
  useEffect(() => { if (hasDesktopBridge()) void load() }, [load])
  const copy = async (text: string) => {
    try { await navigator.clipboard.writeText(text); setNotice('已复制') } catch { setError('复制失败，请手动选择并复制文本') }
  }
  const copyKey = async () => {
    setKeyBusy(true); setError(null); setNotice(null)
    try { await desktopCommand('api.copyKey'); setNotice('API 密钥已由原生应用复制到剪贴板') }
    catch (err) { setError(err instanceof Error ? err.message : '密钥复制失败') }
    finally { setKeyBusy(false) }
  }
  const rotate = async () => {
    if (!config || rotation.current) return
    const controller = new AbortController(); rotation.current = controller
    setKeyBusy(true); setError(null); setNotice(null)
    try {
      const result = await desktopCommand('api.rotateKey', { revision: config.revision }, { signal: controller.signal })
      if (rotation.current !== controller) return
      const next = result.config as unknown as DesktopConfigView
      if (!next?.revision || !next.editable) throw new Error('轮换结果未确认，请重新读取接入信息。')
      setConfig(next); setNotice('API 密钥已轮换。使用旧密钥的外部客户端需要重新配置。')
    } catch (err) { if (rotation.current === controller) setError(err instanceof Error ? err.message : '密钥轮换失败') }
    finally { if (rotation.current === controller) { rotation.current = null; setKeyBusy(false) } }
  }
  const localCheck = async () => {
    setBusy(true); setError(null); setNotice(null)
    try {
      const reply = await desktopCommand('desktop.getStatus')
      const status = reply.status as Record<string, unknown> | undefined
      if (reply.protocol !== 1 || !status || typeof status.gateway_state !== 'string') throw new Error('本机状态响应无效')
      setNotice(`本机控制通道正常。当前网关状态：${status.gateway_state}。此检查仅读取本机状态快照，未重新检测网关 HTTP 或上游，不能证明上游模型可调用。`)
    } catch (err) { setError(`本地检查未通过：${err instanceof Error ? err.message : '服务不可达'}。未访问上游。`) }
    finally { setBusy(false) }
  }
  if (!hasDesktopBridge()) return <Alert kind="info">接入信息与密钥操作由 macOS 桌面应用提供。</Alert>
  const base = config ? `http://127.0.0.1:${config.editable.port}/v1` : null
  const model = models.find((item) => item.id === selected)
  return <>
    <div className="page-head"><div><h1>API 与模型</h1><p>本机 OpenAI 兼容接入信息。密钥仅通过原生剪贴板复制。</p></div>
      <button className="btn" disabled={busy || keyBusy} onClick={() => void load()}>{busy && <Spinner />}重新读取</button></div>
    {error && <Alert kind="error">{error}</Alert>}{notice && <Alert kind="info">{notice}</Alert>}
    <div className="card"><div className="card-head"><h2>连接信息</h2></div>
      <dl className="api-connection-info">
        {[
          { label: 'Base URL', suffix: '', button: '复制 Base URL' },
          { label: '完整对话端点', suffix: '/chat/completions', button: '复制完整对话端点' },
          { label: 'Responses 端点', suffix: '/responses', button: '复制 Responses 端点' },
          { label: 'Anthropic Messages 端点', suffix: '/messages', button: '复制 Messages 端点' },
        ].map(({ label, suffix, button }) => <div key={suffix}>
          <dt>{label}：</dt><dd className="api-connection-address"><span className="mono" title={base ? `${base}${suffix}` : undefined}>{base ? `${base}${suffix}` : '未读取'}</span>
            <button className="btn" disabled={!base} onClick={() => base && void copy(`${base}${suffix}`)}>{button}</button></dd>
        </div>)}
        <div><dt>API 密钥：</dt><dd>{config?.key_configured ? '••••••••（已配置）' : '未配置'}</dd></div>
      </dl>
      <div className="page-actions">
        <button className="btn" disabled={!config?.key_configured || keyBusy} onClick={() => void copyKey()}>复制密钥</button>
        <button className="btn btn-danger" disabled={!config || keyBusy || busy} onClick={() => void rotate()}>{keyBusy && <Spinner />}轮换密钥</button>
        {rotation.current && <button className="btn" onClick={() => rotation.current?.abort()}>取消轮换</button>}</div>
      <p className="text-faint">轮换需要原生确认，旧密钥随后失效；不会在网页中显示密钥。</p>
      <h3>WorkBuddy 地址填写说明</h3>
      <p>未开启「自定义协议」：填写上方 Base URL，以 <span className="mono">/v1</span> 结尾。</p>
      <p>已开启「自定义协议」：填写上方完整对话端点，以 <span className="mono">/v1/chat/completions</span> 结尾。</p>
      <p className="hint">国内版与国际版都按「自定义协议」开关选择地址。开启时仅填 /v1 会返回 404；请直接复制对应地址，不要重复追加路径。</p>
      <p className="hint">其他 Agent 请按所选协议使用对应端点，三种协议共用本机 API 密钥。若客户端自动拼接 /v1/messages（例如 Anthropic SDK 的 base_url），请填服务根地址 {base ? base.slice(0, -3) : '未读取'}。</p>
      <p className="hint">Responses 与 Messages 支持文本、流式回复及函数工具往返。多轮对话由客户端携带完整历史；Responses 的服务端历史引用、后台任务与平台内置工具暂不支持。</p>
    </div>
    <div className="card"><div className="card-head"><h2>模型列表</h2><span className="hint">最后成功刷新：{refreshed ? refreshed.toLocaleString() : '尚未成功读取'}</span></div>
      {models.length === 0 ? <p>当前没有可显示的模型。请检查 API 服务状态后重新读取。</p> : <>
        <div className="field"><label htmlFor="api-model">模型</label><select id="api-model" value={selected} onChange={(e) => setSelected(e.target.value)}>{models.map((item) => <option value={item.id} key={item.id}>{modelLabel(item)}</option>)}</select></div>
        <button className="btn" disabled={!selected} onClick={() => void copy(selected)}>复制完整模型 ID</button>
        <dl><dt>完整模型 ID</dt><dd className="mono">{model?.id}</dd><dt>显示名称</dt><dd>{model ? modelName(model) : '未提供'}</dd><dt>模型说明</dt><dd>{model?.description || '未提供'}</dd><dt>区域元数据</dt><dd>未提供；global: 前缀仅保留为请求 ID</dd><dt>提供方</dt><dd>{model?.owned_by || '未提供'}</dd>
          <dt>上下文长度</dt><dd>{model?.context_length && model.context_length > 0 ? model.context_length.toLocaleString() : '未提供'}</dd>
          <dt>最大输出 Token</dt><dd>{model?.max_output_tokens && model.max_output_tokens > 0 ? model.max_output_tokens.toLocaleString() : '未提供'}</dd>
          <dt>可选推理强度</dt><dd>{model?.reasoning_supported_efforts?.map(reasoningEffortLabel).join('、') || '未提供'}</dd>
          <dt>默认推理强度</dt><dd>{model?.reasoning_default_effort ? reasoningEffortLabel(model.reasoning_default_effort) : '未提供'}</dd>
          <dt>流式能力元数据</dt><dd>未提供</dd></dl><p>请保留完整 ID，包括国际模型的 global: 前缀。</p>
      </>}
    </div>
    <div className="card"><div className="card-head"><h2>HTTP 请求示例</h2></div>
      {base && selected ? <><pre className="mono">{apiExample(base, selected)}</pre><button className="btn" onClick={() => void copy(apiExample(base, selected))}>复制占位密钥示例</button></> : <p>连接信息与模型读取成功后显示示例。</p>}
      <p>YOUR_API_KEY 为占位符，运行前请在自己的客户端填写。执行对话请求可能消耗上游配额或费用。</p>
    </div>
    <div className="card"><div className="card-head"><h2>连接验证</h2></div><p>本地检查仅读取原生应用的本机运行状态，不访问上游。真实对话需在聊天测试页手动发送，可能消耗上游配额或费用。</p>
      <div className="page-actions"><button className="btn" disabled={busy || keyBusy} onClick={() => void localCheck()}>本地连通检查</button><Link className="btn" to="/playground">前往真实对话测试</Link></div>
    </div>
  </>
}
