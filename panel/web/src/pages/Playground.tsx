// Playground.tsx 聊天测试台：验证网关的 OpenAI 兼容接口、流式输出与会话粘性。
import { useEffect, useRef, useState } from 'react'
import { api, ApiError, streamChat } from '../api'
import { subscribeDesktopLifecycle } from '../desktopBridge'
import type { ChatMessage, ChatUsage, Model } from '../types'
import { Alert, Badge, fmtNum, modelLabel, reasoningEffortLabel, Spinner, Modal } from '../ui'

interface Bubble {
  id: number
  role: 'user' | 'assistant'
  content: string
  reasoning?: string
  state?: 'pending' | 'completed' | 'stopped' | 'interrupted'
  usage?: ChatUsage
  stats?: { elapsed_ms?: number; ttfb_ms?: number }
  error?: string
}

export default function Playground() {
  const [models, setModels] = useState<Model[]>([])
  const [model, setModel] = useState('')
  const [input, setInput] = useState('你好，请用一句话自我介绍')
  const [systemPrompt, setSystemPrompt] = useState('')
  const [messages, setMessages] = useState<Bubble[]>([])
  const [stream, setStream] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [temperature, setTemperature] = useState<string>('')
  const [maxTokens, setMaxTokens] = useState<string>('')
  const [reasoningEffort, setReasoningEffort] = useState('')
  const [stickyEnabled, setStickyEnabled] = useState(true)
  const [conversationId, setConversationId] = useState(() => `playground-${Math.random().toString(36).slice(2, 10)}`)
  const [showRaw, setShowRaw] = useState<string | null>(null)

  const abortRef = useRef<(() => void) | null>(null)
  const requestId = useRef(0)
  const activeRequest = useRef<number | null>(null)
  const followBottom = useRef(true)
  const composing = useRef(false)
  const logRef = useRef<HTMLDivElement>(null)
  const currentModel = models.find((m) => m.id === model)
  const supportedEfforts = currentModel?.reasoning_supported_efforts ?? []
  const selectedEffort = supportedEfforts.includes(reasoningEffort) ? reasoningEffort : ''
  useEffect(() => { setReasoningEffort('') }, [model])

  const loadModels = async () => {
    try {
      const res = await api.models()
      const list = res.data ?? []
      setModels(list)
      setModel(old => list.some(item => item.id === old) ? old : (list[0]?.id ?? ''))
      setError(null)
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '加载模型列表失败')
    }
  }

  useEffect(() => { void loadModels() }, [])
  useEffect(() => {
    if (followBottom.current && logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight
  }, [messages])
  useEffect(() => () => {
    activeRequest.current = null
    abortRef.current?.()
  }, [])

  useEffect(() => subscribeDesktopLifecycle(({ kind }) => {
    const id = activeRequest.current
    if (id === null) return
    // Invalidate first: abort and late transport callbacks must not relabel interruption as stop/success.
    activeRequest.current = null
    const abort = abortRef.current
    abortRef.current = null
    setMessages(previous => previous.map(bubble => bubble.id === id && bubble.role === 'assistant'
      ? { ...bubble, state: 'interrupted', error: kind === 'sleep' ? '系统进入睡眠，对话已中断；请手动重试' : '系统已唤醒，旧对话已中断；请手动重试' } : bubble))
    setBusy(false)
    abort?.()
  }), [])

  const send = async () => {
    const draft = input
    const text = draft.trim()
    if (!text || activeRequest.current !== null) return
    if (!model) { setError('请先选择模型'); return }
    const extra: Record<string, unknown> = {}
    if (temperature.trim()) {
      const value = Number(temperature)
      if (!Number.isFinite(value) || value < 0 || value > 2) { setError('temperature 必须是 0 到 2 的数字'); return }
      extra.temperature = value
    }
    if (maxTokens.trim()) {
      const value = Number(maxTokens)
      if (!Number.isSafeInteger(value) || value <= 0) { setError('max_tokens 必须是正整数'); return }
      if (currentModel?.max_output_tokens && value > currentModel.max_output_tokens) {
        setError(`最大输出不能超过该模型的 ${fmtNum(currentModel.max_output_tokens)} Token`); return
      }
      extra.max_tokens = value
    }
    if (selectedEffort) extra.reasoning_effort = selectedEffort
    const history: ChatMessage[] = []
    if (systemPrompt.trim()) history.push({ role: 'system', content: systemPrompt.trim() })
    // 只有完整的一问一答进入下一轮；停止或截断的片段仅留在本页。
    for (let i = 0; i < messages.length; i += 2) {
      const user = messages[i]
      const answer = messages[i + 1]
      if (answer?.state !== 'completed') continue
      history.push({ role: 'user', content: user.content }, { role: 'assistant', content: answer.content })
    }
    history.push({ role: 'user', content: text })
    const payload = { model, messages: history, extra, conversationId: stickyEnabled ? conversationId : undefined }
    const id = ++requestId.current
    activeRequest.current = id
    setError(null)
    setMessages(prev => [...prev, { id, role: 'user', content: text }, { id, role: 'assistant', content: '', state: 'pending' }])
    setBusy(true)
    const start = performance.now()
    let firstToken: number | undefined
    let finishReason: string | undefined
    const update = (fn: (bubble: Bubble) => Bubble) => {
      if (activeRequest.current !== id) return
      setMessages(prev => prev.map(bubble => bubble.id === id && bubble.role === 'assistant' ? fn(bubble) : bubble))
    }
    const finish = (state: 'completed' | 'stopped' | 'interrupted') => {
      if (activeRequest.current !== id) return
      const incomplete = state === 'completed' && finishReason !== undefined && finishReason !== 'stop'
      update(bubble => ({
        ...bubble, state: incomplete ? 'interrupted' : state,
        error: incomplete ? finishReason === 'length' ? '回答达到输出上限，内容未完成' : `回答结束（${finishReason}），未作为完整对话加入下一轮` : bubble.error,
        stats: { ...bubble.stats, elapsed_ms: Math.round(performance.now() - start), ttfb_ms: firstToken },
      }))
      activeRequest.current = null
      abortRef.current = null
      setBusy(false)
      if (state === 'completed' && !incomplete) setInput(current => current === draft ? '' : current)
    }

    if (stream) {
      abortRef.current = streamChat(payload,
        d => {
          if (activeRequest.current !== id) return
          if (firstToken === undefined && (d.content || d.reasoning)) firstToken = Math.round(performance.now() - start)
          if (d.finish_reason) finishReason = d.finish_reason
          update(bubble => ({
            ...bubble, content: bubble.content + (d.content ?? ''),
            reasoning: (bubble.reasoning ?? '') + (d.reasoning ?? ''),
            usage: d.usage ?? bubble.usage,
          }))
        },
        message => update(bubble => ({ ...bubble, error: message })),
        finish,
      )
    } else {
      const controller = new AbortController()
      abortRef.current = () => { controller.abort(); finish('stopped') }
      try {
        const res = await api.chat(payload, controller.signal)
        if (activeRequest.current !== id) return
        finishReason = res.result.finish_reason
        update(bubble => ({ ...bubble, content: res.result.content, reasoning: res.result.reasoning_content, usage: res.result.usage }))
        finish('completed')
      } catch (err) {
        if (activeRequest.current !== id) return
        if (controller.signal.aborted) finish('stopped')
        else {
          update(bubble => ({ ...bubble, error: err instanceof ApiError ? err.message : '连接中断：请检查网络后手动重试' }))
          finish('interrupted')
        }
      }
    }
  }

  const stop = () => abortRef.current?.()
  const clearChat = () => {
    stop()
    setMessages([])
    setShowRaw(null)
    followBottom.current = true
    setConversationId(`playground-${Math.random().toString(36).slice(2, 10)}`)
  }

  return (
    <>
      <div className="page-head">
        <div>
          <h1>聊天测试</h1>
          <p>当前测试对话仅保存在本页内存中，离开页面或退出应用后清空</p>
        </div>
        <div className="page-actions">
          <button className="btn" onClick={() => void loadModels()}>
            刷新模型列表
          </button>
          <button className="btn" onClick={clearChat} disabled={messages.length === 0}>
            清空对话
          </button>
        </div>
      </div>

      {error && (
        <Alert kind="error" onClose={() => setError(null)}>
          {error}
        </Alert>
      )}

      <div className="playground-layout">
        <div className="card">
          <div className="card-head">
            <h2>会话设置</h2>
            {currentModel && (
              <span className="hint">
                上下文上限 {currentModel.context_length > 0 ? `${fmtNum(currentModel.context_length)} Token` : '未提供'}
                {currentModel.max_output_tokens ? ` · 最大输出 ${fmtNum(currentModel.max_output_tokens)}` : ''}
              </span>
            )}
          </div>

          <div className="field">
            <label htmlFor="chat-model">模型</label>
            <select id="chat-model" value={model} onChange={(e) => setModel(e.target.value)}>
              {models.length === 0 && <option value="">（加载中或网关不可用）</option>}
              {models.map((m) => (
                <option key={m.id} value={m.id}>
                  {modelLabel(m)}
                </option>
              ))}
            </select>
            {currentModel?.description && <div className="desc">{currentModel.description}</div>}
          </div>

          <div className="field">
            <label htmlFor="chat-effort">推理强度</label>
            <select id="chat-effort" value={selectedEffort} onChange={(e) => setReasoningEffort(e.target.value)} disabled={supportedEfforts.length === 0}>
              <option value="">模型默认{currentModel?.reasoning_default_effort ? ` · ${reasoningEffortLabel(currentModel.reasoning_default_effort)}` : ''}</option>
              {supportedEfforts.map(effort => <option key={effort} value={effort}>{reasoningEffortLabel(effort)}</option>)}
            </select>
            <div className="desc">{supportedEfforts.length === 0 ? '模型未提供可选推理档位，使用默认设置。' : '只列出该模型提供的档位；切换模型后恢复默认。'}</div>
          </div>

          <div className="field">
            <label htmlFor="chat-system">System Prompt（可选）</label>
            <textarea id="chat-system" rows={2} value={systemPrompt} onChange={(e) => setSystemPrompt(e.target.value)} placeholder="给模型的系统指令…" />
          </div>

          <div className="row">
            <div className="field" style={{ flex: 1 }}>
              <label htmlFor="chat-temperature">temperature（留空用默认）</label>
              <input id="chat-temperature" type="text" value={temperature} onChange={(e) => setTemperature(e.target.value)} placeholder="0 ~ 2" />
            </div>
            <div className="field" style={{ flex: 1 }}>
              <label htmlFor="chat-max-tokens">最大输出 Token（留空用默认）</label>
              <input id="chat-max-tokens" type="text" value={maxTokens} onChange={(e) => setMaxTokens(e.target.value)} placeholder="例如 1024" />
            </div>
          </div>
          <div className="desc">上下文上限由模型决定；最大输出限制回答长度，不改变上下文容量。</div>

          <div className="field">
            <label className="checkbox">
              <input type="checkbox" checked={stream} onChange={(e) => setStream(e.target.checked)} />
              流式输出（SSE）
            </label>
            <div className="desc">关闭后由网关聚合为单个响应返回，适合对比验证。</div>
          </div>

          <div className="field" style={{ marginBottom: 0 }}>
            <label className="checkbox">
              <input type="checkbox" checked={stickyEnabled} onChange={(e) => setStickyEnabled(e.target.checked)} />
              发送会话 ID（触发网关的会话粘性）
            </label>
            <div className="desc">
              带上 <span className="mono">conversation_id</span> 后，同一会话会尽量固定到同一账号，
              多轮对话不跳号。当前 ID：
              <span className="mono">{stickyEnabled ? conversationId : '（未发送）'}</span>
            </div>
          </div>
        </div>

        <div className="card">
          <div className="card-head">
            <h2>对话</h2>
            {busy && <><Spinner /><Badge cls="badge-warn">{stream ? '接收中…' : '请求中…'}</Badge></>}
          </div>

          <div className="chat-log" ref={logRef} onScroll={e => {
            const log = e.currentTarget
            followBottom.current = log.scrollHeight - log.scrollTop - log.clientHeight < 40
          }}>
            {messages.length === 0 && (
              <div className="empty" style={{ margin: 'auto' }}>
                发送一条消息开始测试。
                <div style={{ marginTop: 6, fontSize: 12 }}>请求会经由账号池自动选号转发到上游。</div>
              </div>
            )}
            {messages.map((m) => (
              <div key={`${m.id}-${m.role}`} className={`msg msg-${m.role}`}>
                <div className="msg-role">{m.role === 'user' ? '你' : 'AI'}</div>
                <div className="msg-body">
                  {m.reasoning && (
                    <div className="reasoning">
                      <div className="reasoning-label">推理过程</div>
                      {m.reasoning}
                    </div>
                  )}
                  {m.content && <pre>{m.content}</pre>}
                  {(m.state === 'pending') && !m.content && !m.reasoning && <Badge cls="badge-warn">正在思考…</Badge>}
                  {(m.state === 'pending') && (m.content || m.reasoning) && <><Badge cls="badge-warn">接收中…</Badge><span className="cursor" /></>}
                  {m.error && (
                    <Alert kind="error">
                      {m.error}
                      {m.error.includes('503') && (
                        <div style={{ marginTop: 5, fontSize: 12.5 }}>
                          所有账号可能都在冷却中，请到「仪表盘」查看账号池状态。
                        </div>
                      )}
                    </Alert>
                  )}
                  {m.role === 'assistant' && m.state !== 'pending' && (
                    <div className="text-faint" style={{ fontSize: 11.5, marginTop: 6, display: 'flex', gap: 12, flexWrap: 'wrap' }}>
                      <Badge cls={m.state === 'completed' ? 'badge-ok' : 'badge-danger'}>{m.state === 'completed' ? '已完成' : m.state === 'stopped' ? '已停止' : '已中断'}</Badge>
                      {m.usage?.prompt_tokens !== undefined && <span>输入 {m.usage.prompt_tokens} tok</span>}
                      {m.usage?.completion_tokens !== undefined && <span>输出 {m.usage.completion_tokens} tok</span>}
                      {m.usage?.total_tokens !== undefined && <span>总计 {m.usage.total_tokens} tok</span>}
                      {m.usage?.prompt_tokens === undefined && m.usage?.completion_tokens === undefined && m.usage?.total_tokens === undefined && <span>未返回用量</span>}
                      {m.stats?.ttfb_ms !== undefined && <span>首字 {m.stats.ttfb_ms} ms</span>}
                      {m.stats?.elapsed_ms !== undefined && <span>总耗时 {m.stats.elapsed_ms} ms</span>}
                      {m.content && <button className="btn btn-ghost btn-sm" style={{ padding: '0 4px', fontSize: 11 }} onClick={() => setShowRaw(JSON.stringify(m, null, 2))}>
                        查看原始数据
                      </button>}
                    </div>
                  )}
                </div>
              </div>
            ))}
          </div>

          <div className="field" style={{ marginTop: 13, marginBottom: 0 }}>
            <textarea
              className="chat-composer"
              aria-label="消息"
              rows={3}
              value={input}
              onChange={(e) => setInput(e.target.value)}
              placeholder="输入消息… （⌘ / Ctrl + Enter 发送）"
              onCompositionStart={() => { composing.current = true }}
              onCompositionEnd={() => { composing.current = false }}
              onKeyDown={(e) => {
                if (e.key === 'Enter' && (e.ctrlKey || e.metaKey) && !composing.current && !e.nativeEvent.isComposing && e.keyCode !== 229) {
                  e.preventDefault()
                  void send()
                }
              }}
            />
          </div>
          <div className="page-actions" style={{ marginTop: 11 }}>
            {busy ? (
              <button className="btn btn-danger" onClick={stop}>
                停止
              </button>
            ) : (
              <button className="btn btn-primary" onClick={() => void send()} disabled={!input.trim() || !model}>
                发送
              </button>
            )}
            <span className="hint">⌘ / Ctrl + Enter 快捷发送</span>
          </div>
        </div>
      </div>

      {showRaw && (
        <Modal title="原始响应数据" wide onClose={() => setShowRaw(null)}>
          <div className="muted-box" style={{ maxHeight: 420, overflow: 'auto', whiteSpace: 'pre-wrap' }}>{showRaw}</div>
        </Modal>
      )}
    </>
  )
}
