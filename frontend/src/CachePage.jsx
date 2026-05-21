import { useEffect, useMemo, useState } from 'react'

const pageSize = 10
const fallbackHeuristicHash = '0x0000000000000000'

function colorFromHash(hash) {
  const normalized = (hash || fallbackHeuristicHash).toLowerCase()
  let seed = 0
  for (let i = 0; i < normalized.length; i += 1) {
    seed = (seed * 131 + normalized.charCodeAt(i)) % 360
  }
  return {
    border: `hsl(${seed} 72% 56%)`,
    soft: `hsl(${seed} 72% 56% / 0.18)`,
    chip: `hsl(${seed} 72% 56% / 0.26)`
  }
}

export default function CachePage() {
  const [items, setItems] = useState([])
  const [total, setTotal] = useState(0)
  const [offset, setOffset] = useState(0)
  const [loading, setLoading] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState('')
  const [deletingHash, setDeletingHash] = useState('')
  const [clearingAll, setClearingAll] = useState(false)

  const loadEntries = async (nextOffset, append) => {
    if (append) {
      setLoadingMore(true)
    } else {
      setLoading(true)
    }
    setError('')
    try {
      const res = await fetch(`/api/cache/tt/entries?offset=${nextOffset}&limit=${pageSize}`)
      if (!res.ok) {
        throw new Error(`failed: ${res.status}`)
      }
      const data = await res.json()
      const nextItems = data.items || []
      setItems((prev) => (append ? [...prev, ...nextItems] : nextItems))
      setOffset((append ? nextOffset : 0) + nextItems.length)
      setTotal(data.total || 0)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'unknown error')
    } finally {
      setLoading(false)
      setLoadingMore(false)
    }
  }

  useEffect(() => {
    loadEntries(0, false)
  }, [])

  const hasMore = offset < total

  const onLoadMore = () => {
    if (loadingMore || loading) {
      return
    }
    loadEntries(offset, true)
  }

  const onDelete = async (hash) => {
    if (!hash || deletingHash) {
      return
    }
    setDeletingHash(hash)
    setError('')
    try {
      const res = await fetch(`/api/cache/tt/entries/${encodeURIComponent(hash)}`, {
        method: 'DELETE'
      })
      if (!res.ok) {
        throw new Error(`failed: ${res.status}`)
      }
      const data = await res.json()
      if (data.deleted) {
        setItems((prev) => prev.filter((entry) => entry.hash !== hash))
        setTotal((prev) => (prev > 0 ? prev - 1 : 0))
        setOffset((prev) => (prev > 0 ? prev - 1 : 0))
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : 'unknown error')
    } finally {
      setDeletingHash('')
    }
  }

  const onClearAll = async () => {
    if (clearingAll) {
      return
    }
    const ok = window.confirm(
      'This will prune the whole cache (TT + related caches). This cannot be undone. Continue?'
    )
    if (!ok) {
      return
    }
    setClearingAll(true)
    setError('')
    try {
      const res = await fetch('/api/cache/tt', {
        method: 'DELETE'
      })
      if (!res.ok) {
        throw new Error(`failed: ${res.status}`)
      }
      setItems([])
      setTotal(0)
      setOffset(0)
      await loadEntries(0, false)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'unknown error')
    } finally {
      setClearingAll(false)
    }
  }

  const statusLabel = useMemo(() => {
    if (loading) {
      return 'Loading TT cache...'
    }
    return `Showing ${items.length} / ${total} entries`
  }, [loading, items.length, total])

  const heuristicUsage = useMemo(() => {
    const byHash = new Map()
    for (const entry of items) {
      const hash = entry.heuristic_hash || fallbackHeuristicHash
      const current = byHash.get(hash) || { hash, count: 0 }
      current.count += 1
      byHash.set(hash, current)
    }
    return [...byHash.values()].sort((a, b) => b.count - a.count)
  }, [items])

  return (
    <div className="page cache-page">
      <header className="app-header">
        <div>
          <h1>TT Cache</h1>
          <p>{statusLabel}</p>
        </div>
        <div className="header-links">
          <a className="cache-link" href="/">
            Back to game
          </a>
          <a className="cache-link" href="/trainer">
            Trainer
          </a>
          <a className="cache-link" href="/minmax">
            Minmax demo
          </a>
        </div>
      </header>
      <section className="panel cache-panel">
        {error && <div className="history-empty">Error: {error}</div>}
        <div className="cache-hash-legend">
          {heuristicUsage.map((usage) => {
            const tone = colorFromHash(usage.hash)
            return (
              <div
                className="cache-hash-chip"
                key={usage.hash}
                style={{
                  borderColor: tone.border,
                  background: tone.chip
                }}
              >
                <span className="cache-hash-chip-dot" style={{ background: tone.border }} />
                <span className="cache-hash-chip-hash">{usage.hash}</span>
                <span className="cache-hash-chip-count">{usage.count}</span>
              </div>
            )
          })}
          {heuristicUsage.length > 0 && (
            <div className="cache-hash-legend-note">counts are for currently loaded entries</div>
          )}
        </div>
        <div className="cache-list">
          {items.map((entry, index) => {
            const heuristicHash = entry.heuristic_hash || fallbackHeuristicHash
            const tone = colorFromHash(heuristicHash)
            return (
            <article
              className="cache-item"
              key={`${entry.hash}-${heuristicHash}-${entry.gen_written}-${index}`}
              style={{
                borderColor: tone.border,
                background: `linear-gradient(90deg, ${tone.soft}, rgba(255,255,255,0.03) 20%)`
              }}
            >
              <div className="cache-item-main">
                <div className="cache-item-row">
                  <span className="cache-hash">{entry.hash}</span>
                  <span className="cache-flag">{entry.flag}</span>
                </div>
                <div className="cache-item-row">
                  <span className="cache-heuristic-hash">heuristic={heuristicHash}</span>
                </div>
                <div className="cache-item-row">
                  <span>hits={entry.hits}</span>
                  <span>depth={entry.depth}</span>
                  <span>score={entry.score}</span>
                  <span>
                    best=({entry.best_move.x},{entry.best_move.y})
                  </span>
                </div>
                <div className="cache-item-row">
                  <span>
                    growth=({entry.growth_left},{entry.growth_right},{entry.growth_top},{entry.growth_bottom})
                  </span>
                  <span>
                    walls=({String(entry.hit_left)},{String(entry.hit_right)},{String(entry.hit_top)},
                    {String(entry.hit_bottom)})
                  </span>
                  <span>
                    frame={entry.frame_w}x{entry.frame_h}
                  </span>
                </div>
                <div className="cache-item-row">
                  <span>gen=({entry.gen_written}/{entry.gen_last_used})</span>
                </div>
              </div>
              <button
                type="button"
                className="danger"
                disabled={deletingHash === entry.hash}
                onClick={() => onDelete(entry.hash)}
              >
                {deletingHash === entry.hash ? 'Removing...' : 'Remove'}
              </button>
            </article>
          )})}
          {!loading && items.length === 0 && <div className="history-empty">No TT entry found.</div>}
        </div>
        <div className="actions">
          <button type="button" onClick={() => loadEntries(0, false)} disabled={loading || loadingMore}>
            Refresh
          </button>
          <button type="button" onClick={onLoadMore} disabled={!hasMore || loading || loadingMore}>
            {loadingMore ? 'Loading...' : hasMore ? 'Load more' : 'No more entries'}
          </button>
          <button type="button" className="danger" onClick={onClearAll} disabled={clearingAll || loading || loadingMore}>
            {clearingAll ? 'Pruning...' : 'Prune whole cache'}
          </button>
        </div>
      </section>
    </div>
  )
}
