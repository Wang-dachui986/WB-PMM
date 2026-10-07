import { useLayoutEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { createPortal } from 'react-dom'

/** Non-modal: the highlighted control remains clickable while the guide is open. */
export default function GuidePopover({ targets, step, title, busy, onSkip, children }: {
  targets: string[]; step: number; title: string; busy: boolean; onSkip: () => void; children: ReactNode
}) {
  const panel = useRef<HTMLElement>(null)
  const skip = useRef(onSkip)
  skip.current = onSkip
  const [position, setPosition] = useState({ left: 12, top: 12, maxHeight: 480, ready: false,
    target: null as { left: number; top: number; width: number; height: number } | null,
    arrow: 'none', arrowOffset: 0 })
  const selectors = targets.join(',')

  useLayoutEffect(() => {
    const previous = document.activeElement
    return () => { if (previous instanceof HTMLElement && previous.isConnected) previous.focus({ preventScroll: true }) }
  }, [])

  useLayoutEffect(() => {
    if (position.ready) panel.current?.focus({ preventScroll: true })
  }, [step, position.ready])

  useLayoutEffect(() => {
    let frame = 0
    let target: HTMLElement | null = null
    const observer = new ResizeObserver(() => schedule())
    const update = () => {
      frame = 0
      const card = panel.current
      if (!card) return
      const next = selectors.split(',').map(selector => document.querySelector<HTMLElement>(selector))
        .find(node => node && node.getClientRects().length > 0) ?? null
      if (next !== target) {
        if (target) observer.unobserve(target)
        target = next
        if (target) {
          observer.observe(target)
          target.scrollIntoView({ block: 'nearest', inline: 'nearest', behavior: 'instant' })
        }
      }
      const w = window.innerWidth, h = window.innerHeight, gap = 14, margin = 12
      const width = card.getBoundingClientRect().width
      const height = Math.min(card.scrollHeight, h - 2 * margin)
      const rect = target?.getBoundingClientRect()
      let visible = rect ? { left: Math.max(0, rect.left), top: Math.max(0, rect.top),
        right: Math.min(w, rect.right), bottom: Math.min(h, rect.bottom) } : null
      // A sidebar can scroll independently of the document. Clip its hidden part too.
      for (let parent = target?.parentElement; visible && parent; parent = parent.parentElement) {
        const style = getComputedStyle(parent), bounds = parent.getBoundingClientRect()
        if (/(auto|scroll|hidden|clip)/.test(style.overflowX)) { visible.left = Math.max(visible.left, bounds.left); visible.right = Math.min(visible.right, bounds.right) }
        if (/(auto|scroll|hidden|clip)/.test(style.overflowY)) { visible.top = Math.max(visible.top, bounds.top); visible.bottom = Math.min(visible.bottom, bounds.bottom) }
      }
      if (visible && (visible.right <= visible.left || visible.bottom <= visible.top)) visible = null
      let left = w - width - margin, top = margin, maxHeight = h - 2 * margin, arrow = 'none', arrowOffset = 0
      const clamp = (value: number, low: number, high: number) => Math.max(low, Math.min(value, high))
      if (visible) {
        const box = visible, cx = (box.left + box.right) / 2, cy = (box.top + box.bottom) / 2
        if (w - box.right >= width + gap + margin || box.left >= width + gap + margin) {
          const right = w - box.right >= width + gap + margin
          left = right ? box.right + gap : box.left - width - gap
          top = clamp(cy - height / 2, margin, h - height - margin)
          arrow = right ? 'left' : 'right'; arrowOffset = clamp(cy - top, 22, height - 22)
        } else {
          const below = h - box.bottom - gap - margin, above = box.top - gap - margin
          const bottom = below >= height || below >= above
          maxHeight = Math.max(100, bottom ? below : above)
          left = clamp(cx - width / 2, margin, w - width - margin)
          top = bottom ? box.bottom + gap : box.top - gap - Math.min(height, maxHeight)
          top = clamp(top, margin, h - Math.min(height, maxHeight) - margin)
          arrow = bottom ? 'top' : 'bottom'; arrowOffset = clamp(cx - left, 22, width - 22)
        }
      }
      const value = { left, top, maxHeight, ready: true, arrow, arrowOffset,
        target: visible ? { left: visible.left, top: visible.top, width: visible.right - visible.left, height: visible.bottom - visible.top } : null }
      setPosition(old => JSON.stringify(old) === JSON.stringify(value) ? old : value)
    }
    function schedule() { if (!frame) frame = requestAnimationFrame(update) }
    if (panel.current) observer.observe(panel.current)
    const mutations = new MutationObserver(schedule)
    mutations.observe(document.getElementById('root')!, { childList: true, subtree: true })
    document.addEventListener('scroll', schedule, true)
    window.addEventListener('resize', schedule)
    update()
    return () => {
      cancelAnimationFrame(frame); observer.disconnect(); mutations.disconnect()
      document.removeEventListener('scroll', schedule, true); window.removeEventListener('resize', schedule)
    }
  }, [selectors, step])

  useLayoutEffect(() => {
    const escape = (event: KeyboardEvent) => {
      if (event.key === 'Escape' && !busy && !document.querySelector('dialog[open]')) {
        event.preventDefault(); skip.current()
      }
    }
    document.addEventListener('keydown', escape)
    return () => document.removeEventListener('keydown', escape)
  }, [busy])

  return createPortal(<>
    {position.target && <div className="guide-highlight" style={position.target} aria-hidden="true" />}
    <section ref={panel} className="guide-popover" role="dialog" aria-modal="false"
      aria-labelledby="guide-title" aria-describedby="guide-description" tabIndex={-1}
      style={{ left: position.left, top: position.top, maxHeight: position.maxHeight, visibility: position.ready ? 'visible' : 'hidden' }}>
      <div className="guide-head"><span className="guide-counter">首次使用 · {step + 1}/7</span>
        <button className="btn btn-ghost btn-sm" disabled={busy} onClick={onSkip} aria-label="跳过使用引导">跳过 ×</button></div>
      <h2 id="guide-title">{title}</h2>
      <div id="guide-description" className="guide-body">{children}</div>
      <p className="guide-hint">可直接操作高亮位置 · 随时从仪表盘右上角重新打开</p>
    </section>
    {position.target && position.arrow !== 'none' && <span className={`guide-arrow guide-arrow-${position.arrow}`} aria-hidden="true"
      style={{ left: position.arrow === 'left' ? position.left - 5 : position.arrow === 'right' ? position.left + Math.min(360, window.innerWidth - 24) - 5 : position.left + position.arrowOffset - 5,
        top: position.arrow === 'top' ? position.top - 5 : position.arrow === 'bottom' ? position.top + Math.min(panel.current?.scrollHeight ?? 0, position.maxHeight) - 5 : position.top + position.arrowOffset - 5 }} />}
  </>, document.body)
}
