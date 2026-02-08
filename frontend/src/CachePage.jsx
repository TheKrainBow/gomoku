import { useEffect, useMemo, useState } from 'react'

const pageSize = 10

export default function CachePage() {
  const [items, setItems] = useState([])
  const [total, setTotal] = useState(0)
  const [offset, setOffset] = useState(0)
  const [loading, setLoading] = useState(false)
  const [loadingMore, setLoadingMore] = useState(false)
  const [error, setError] = useState('')
  const [deletingHash, setDeletingHash] = useState('')

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

  const statusLabel = useMemo(() => {
    if (loading) {
      return 'Loading TT cache...'
    }
    return `Showing ${items.length} / ${total} entries`
  }, [loading, items.length, total])

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
          <a className="cache-link" href="/minmax">
            Minmax demo
          </a>
        </div>
      </header>
      <section className="panel cache-panel">
        {error && <div className="history-empty">Error: {error}</div>}
        <div className="cache-list">
          {items.map((entry) => (
            <article className="cache-item" key={entry.hash}>
              <div className="cache-item-main">
                <div className="cache-item-row">
                  <span className="cache-hash">{entry.hash}</span>
                  <span className="cache-flag">{entry.flag}</span>
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
          ))}
          {!loading && items.length === 0 && <div className="history-empty">No TT entry found.</div>}
        </div>
        <div className="actions">
          <button type="button" onClick={() => loadEntries(0, false)} disabled={loading || loadingMore}>
            Refresh
          </button>
          <button type="button" onClick={onLoadMore} disabled={!hasMore || loading || loadingMore}>
            {loadingMore ? 'Loading...' : hasMore ? 'Load more' : 'No more entries'}
          </button>
        </div>
      </section>
    </div>
  )
}
