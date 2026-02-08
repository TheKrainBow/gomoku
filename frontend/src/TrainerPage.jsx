import { useEffect, useMemo, useRef, useState } from 'react'

function wsUrl(path) {
  const protocol = window.location.protocol === 'https:' ? 'wss' : 'ws'
  return `${protocol}://${window.location.host}${path}`
}

export default function TrainerPage() {
  const [status, setStatus] = useState(null)
  const [liveGame, setLiveGame] = useState(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState('')
  const [mode, setMode] = useState('heuristic')
  const [actionBusy, setActionBusy] = useState(false)
  const [selectedChallengerId, setSelectedChallengerId] = useState('')
  const [copyState, setCopyState] = useState('')
  const [etaNowMs, setEtaNowMs] = useState(Date.now())
  const gameWsRef = useRef(null)
  const progressRef = useRef({
    generation: -1,
    startedMs: 0,
    lastDone: 0,
    observedOpeningsPerPair: 0
  })

  const applyTrainerStatus = (data) => {
    setStatus(data)
    const details = data?.challenger_details || []
    if (details.length > 0) {
      setSelectedChallengerId((prev) => (details.some((item) => item.id === prev) ? prev : details[0].id))
    } else {
      setSelectedChallengerId('')
    }
  }

  const loadStatus = async () => {
    try {
      const [trainerRes, gameRes] = await Promise.all([fetch('/api/trainer/status'), fetch('/api/status').catch(() => null)])
      if (!trainerRes.ok) {
        throw new Error(`Trainer API unavailable (${trainerRes.status})`)
      }
      const data = await trainerRes.json()
      applyTrainerStatus(data)
      if (gameRes && gameRes.ok) {
        const game = await gameRes.json()
        setLiveGame(game)
      }
      setError('')
    } catch (err) {
      setStatus(null)
      setError(err instanceof Error ? err.message : 'trainer unavailable')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadStatus()

    const gameWs = new WebSocket(wsUrl('/ws/'))
    gameWsRef.current = gameWs
    gameWs.onmessage = (event) => {
      const msg = JSON.parse(event.data)
      if (msg.type === 'status' && msg.payload) {
        setLiveGame(msg.payload)
        return
      }
      if (msg.type === 'reset' && msg.payload) {
        setLiveGame((prev) => ({
          ...(prev || {}),
          ...msg.payload
        }))
        return
      }
      if (msg.type === 'settings' && msg.payload) {
        setLiveGame((prev) => ({
          ...(prev || {}),
          settings: msg.payload.settings || prev?.settings,
          config: msg.payload.config || prev?.config
        }))
        return
      }
      if (msg.type === 'history' && msg.payload?.history) {
        setLiveGame((prev) => {
          const base = prev || { history: [] }
          const existing = Array.isArray(base.history) ? base.history : []
          const nextHistory = existing.concat(msg.payload.history || [])
          return {
            ...base,
            history: nextHistory
          }
        })
      }
    }
    gameWs.onopen = () => {
      gameWs.send(JSON.stringify({ type: 'request_status' }))
    }
    gameWs.onerror = () => {}

    return () => {
      gameWs.close()
      gameWsRef.current = null
    }
  }, [])

  useEffect(() => {
    const id = setInterval(() => setEtaNowMs(Date.now()), 1000)
    return () => clearInterval(id)
  }, [])

  useEffect(() => {
    if (!status) return
    const now = Date.now()
    const generation = Number(status.generation || 0)
    const done = Number(status.games_played || 0)
    const openingIndex = Number(status?.current_match?.opening_index ?? -1)
    const ref = progressRef.current

    if (ref.generation !== generation) {
      ref.generation = generation
      ref.startedMs = now
      ref.lastDone = done
      ref.observedOpeningsPerPair = openingIndex >= 0 ? openingIndex + 1 : 0
      return
    }

    if (openingIndex >= 0 && openingIndex+1 > ref.observedOpeningsPerPair) {
      ref.observedOpeningsPerPair = openingIndex + 1
    }
    ref.lastDone = done
  }, [status?.generation, status?.games_played, status?.current_match?.opening_index, status])

  const onStart = async () => {
    setActionBusy(true)
    setError('')
    try {
      const res = await fetch('/api/trainer/start', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ mode })
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        throw new Error(data.error || `start failed (${res.status})`)
      }
      await loadStatus()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'start failed')
    } finally {
      setActionBusy(false)
    }
  }

  const onStop = async () => {
    setActionBusy(true)
    setError('')
    try {
      const res = await fetch('/api/trainer/stop', { method: 'POST' })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        throw new Error(data.error || `stop failed (${res.status})`)
      }
      await loadStatus()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'stop failed')
    } finally {
      setActionBusy(false)
    }
  }

  const headerLabel = useMemo(() => {
    if (loading) return 'Loading trainer status...'
    if (!status) return 'Trainer not running'
    return `Mode: ${status.mode} | Phase: ${status.phase || 'unknown'}`
  }, [loading, status])

  const selectedChallenger = useMemo(() => {
    const details = status?.challenger_details || []
    return details.find((item) => item.id === selectedChallengerId) || null
  }, [status, selectedChallengerId])

  const copyJson = async (value, label) => {
    try {
      await navigator.clipboard.writeText(JSON.stringify(value || {}, null, 2))
      setCopyState(`${label} copied`)
      setTimeout(() => setCopyState(''), 1500)
    } catch {
      setCopyState('copy failed')
      setTimeout(() => setCopyState(''), 1500)
    }
  }

  const liveBoard = useMemo(() => {
    const size = liveGame?.board_size || 19
    const history = liveGame?.history || []
    return buildBoardSnapshot(history, size, history.length - 1).board
  }, [liveGame])

  const trainingProgress = useMemo(() => {
    if (!status) return null
    const serverTotal = Number(status.round_matches_total || 0)
    const serverDone = Number(status.games_played || 0)
    const serverEta = Number(status.eta_seconds || 0)
    if (serverTotal > 0) {
      const done = Math.min(serverDone, serverTotal)
      const pct = Math.max(0, Math.min(100, (done * 100) / serverTotal))
      return {
        done,
        total: serverTotal,
        pct,
        etaSeconds: serverEta
      }
    }
    const populationSize = Number(status.population_size || 0)
    if (populationSize < 2) return null
    const openingsPerPair = Number(status.training_openings || status.training_openings_count || progressRef.current.observedOpeningsPerPair || 6)
    const pairings = (populationSize * (populationSize - 1)) / 2
    const historicalCount = Number(status.historical_count || 0)
    const estimatedTotal = pairings * openingsPerPair + populationSize * historicalCount * openingsPerPair
    let done = Number(status.games_played || 0)
    const match = status.current_match
    if (match && match.stage === 'population') {
      const blackIndex = parseContenderIndex(match.black_id)
      const whiteIndex = parseContenderIndex(match.white_id)
      if (blackIndex >= 0 && whiteIndex > blackIndex) {
        let pairBefore = 0
        for (let i = 0; i < blackIndex; i++) {
          pairBefore += populationSize - i - 1
        }
        pairBefore += whiteIndex - blackIndex - 1
        const openingOffset = Math.max(0, Number(match.opening_index || 0))
        const estimatedDoneInRound = pairBefore * openingsPerPair + (openingOffset + 1)
        if (estimatedDoneInRound > done) {
          done = estimatedDoneInRound
        }
      }
    }
    if (estimatedTotal <= 0) return null
    const clampedDone = Math.min(done, estimatedTotal)
    const pct = Math.max(0, Math.min(100, (clampedDone * 100) / estimatedTotal))
    const tracker = progressRef.current
    const remaining = Math.max(0, estimatedTotal - clampedDone)
    let etaSeconds = 0
    if (tracker.startedMs > 0 && clampedDone > 0) {
      const elapsedSec = Math.max(1, (etaNowMs - tracker.startedMs) / 1000)
      const averageSecPerMatch = elapsedSec / clampedDone
      etaSeconds = Math.max(0, Math.round(averageSecPerMatch * remaining))
    }
    return {
      done: clampedDone,
      total: estimatedTotal,
      pct,
      etaSeconds
    }
  }, [status, etaNowMs])

  return (
    <div className="page cache-page trainer-page">
      <header className="app-header">
        <div>
          <h1>Trainer Monitor</h1>
          <p>{headerLabel}</p>
        </div>
        <div className="header-links">
          <a className="cache-link" href="/">
            Back to game
          </a>
          <a className="cache-link" href="/cache">
            TT cache
          </a>
        </div>
      </header>
      <section className="panel cache-panel">
        {loading && <div className="history-empty">Loading...</div>}
        {!loading && !status && (
          <div className="history-empty">
            Trainer is not reachable right now.
            <br />
            Start it with `make trainer-start-heuristic` (or `make trainer-start-cache`).
            {error ? <div>Details: {error}</div> : null}
          </div>
        )}
        {!loading && status && (
          <div className="trainer-grid">
            <div className="trainer-card">
              <h3>Runtime</h3>
              <p>running={String(status.running)}</p>
              <p>generation={status.generation}</p>
              <p>games={status.games_played}</p>
              <p>message={status.message || '-'}</p>
              <p>updated={status.updated_at || '-'}</p>
            </div>
            <div className="trainer-card">
              <h3>Current Match</h3>
              {status.current_match ? (
                <>
                  <p>stage={status.current_match.stage}</p>
                  <p>black={status.current_match.black_id}</p>
                  <p>white={status.current_match.white_id}</p>
                  <p>opening=#{status.current_match.opening_index}</p>
                </>
              ) : (
                <p>No active matchup</p>
              )}
            </div>
            <div className="trainer-card">
              <h3>Validation</h3>
              <p>last_rate={Number(status.last_validation_rate || 0).toFixed(3)}</p>
              <p>threshold={Number(status.validation_threshold || 0).toFixed(3)}</p>
              <p>historical_pool={status.historical_count || 0}</p>
              <p>population={status.population_size || 0}</p>
            </div>
            <div className="trainer-card trainer-progress-card">
              <h3>Round Progress</h3>
              {trainingProgress ? (
                <>
                  <p>
                    {trainingProgress.done}/{trainingProgress.total} ({trainingProgress.pct.toFixed(1)}%)
                  </p>
                  <div className="trainer-progress-bar">
                    <div className="trainer-progress-fill" style={{ width: `${trainingProgress.pct}%` }} />
                  </div>
                  <p>eta={formatEta(trainingProgress.etaSeconds)}</p>
                </>
              ) : (
                <p>Progress unavailable</p>
              )}
            </div>
            <div className="trainer-card trainer-rankings">
              <h3>Top Elo</h3>
              {(status.top_contenders || []).length === 0 && <p>No ranking yet</p>}
              {(status.top_contenders || []).map((item, idx) => (
                <p key={`${item.id}-${idx}`}>
                  #{idx + 1} {item.id}: {Number(item.elo || 0).toFixed(1)}
                </p>
              ))}
            </div>
            <div className="trainer-best-preview">
              <div className="trainer-card trainer-json-card">
                <div className="trainer-json-header">
                  <h3>Current Best Heuristic</h3>
                  <button type="button" onClick={() => copyJson(status.champion_heuristic, 'best heuristic')}>
                    Copy JSON
                  </button>
                </div>
                <pre className="trainer-json">{JSON.stringify(status.champion_heuristic || {}, null, 2)}</pre>
              </div>
              <div className="trainer-card trainer-live-card">
                <h3>Live Match Preview</h3>
                {!liveGame ? (
                  <p>No backend status available.</p>
                ) : (
                  <>
                    <p>
                      state={liveGame.status || 'unknown'} winner={liveGame.winner || 0} moves={(liveGame.history || []).length}
                    </p>
                    <p>
                      next_player={liveGame.next_player || 0} mode={liveGame.settings?.mode || '-'}
                    </p>
                    <div className="trainer-live-board-shell">{renderTrainerMiniBoard(liveBoard)}</div>
                  </>
                )}
              </div>
            </div>
            <div className="trainer-card trainer-challenger-card">
              <h3>Challengers</h3>
              {(status.challenger_details || []).length === 0 && <p>No challengers available</p>}
              {(status.challenger_details || []).length > 0 && (
                <div className="trainer-challenger-layout">
                  <div className="trainer-challenger-list">
                    {status.challenger_details.map((item) => (
                      <button
                        type="button"
                        key={item.id}
                        className={`trainer-challenger-item ${selectedChallengerId === item.id ? 'selected' : ''}`}
                        onClick={() => setSelectedChallengerId(item.id)}
                      >
                        {item.id} ({Number(item.elo || 0).toFixed(1)})
                      </button>
                    ))}
                  </div>
                  <div className="trainer-json-wrap">
                    <div className="trainer-json-header">
                      <p>{selectedChallenger ? `Heuristics for ${selectedChallenger.id}` : 'Select a challenger'}</p>
                      <button type="button" disabled={!selectedChallenger} onClick={() => copyJson(selectedChallenger?.heuristics, 'challenger heuristic')}>
                        Copy JSON
                      </button>
                    </div>
                    <pre className="trainer-json">
                      {JSON.stringify(selectedChallenger?.heuristics || {}, null, 2)}
                    </pre>
                  </div>
                </div>
              )}
            </div>
          </div>
        )}
        <div className="actions">
          <select value={mode} onChange={(e) => setMode(e.target.value)} disabled={actionBusy || (status && status.running)}>
            <option value="heuristic">heuristic</option>
            <option value="cache">cache</option>
          </select>
          <button type="button" onClick={onStart} disabled={loading || actionBusy || (status && status.running)}>
            {actionBusy ? 'Working...' : 'Start Training'}
          </button>
          <button type="button" className="danger" onClick={onStop} disabled={loading || actionBusy || !(status && status.running)}>
            {actionBusy ? 'Working...' : 'Stop Training'}
          </button>
        </div>
        <div className="actions">
          <button type="button" onClick={loadStatus} disabled={loading}>
            Refresh
          </button>
          {copyState ? <span className="history-empty">{copyState}</span> : null}
        </div>
      </section>
    </div>
  )
}

function buildBoardSnapshot(history, size, upToIndex) {
  if (upToIndex < 0) {
    return {
      board: Array.from({ length: size }, () => Array(size).fill(0))
    }
  }
  const board = Array.from({ length: size }, () => Array(size).fill(0))
  const cappedIndex = Math.min(upToIndex, history.length - 1)
  for (let i = 0; i <= cappedIndex; i++) {
    const entry = history[i]
    board[entry.y][entry.x] = entry.player
    if (entry.captured_positions) {
      for (const captured of entry.captured_positions) {
        if (captured.y >= 0 && captured.y < size && captured.x >= 0 && captured.x < size) {
          board[captured.y][captured.x] = 0
        }
      }
    }
  }
  return { board }
}

function renderTrainerMiniBoard(board) {
  if (!board || board.length === 0) {
    return null
  }
  return (
    <div className="board trainer-live-full-board">
      {board.map((row, y) => (
        <div className="board-row" key={`trainer-row-${y}`}>
          {row.map((cell, x) => (
            <div className={`board-cell player-${cell}`} key={`trainer-cell-${y}-${x}`} />
          ))}
        </div>
      ))}
    </div>
  )
}

function parseContenderIndex(id) {
  if (!id) return -1
  const match = String(id).match(/^p(\d+)$/)
  if (!match) return -1
  return Number(match[1])
}

function formatEta(totalSeconds) {
  const sec = Math.max(0, Number(totalSeconds || 0))
  if (sec <= 0) return '-'
  const h = Math.floor(sec / 3600)
  const m = Math.floor((sec % 3600) / 60)
  const s = sec % 60
  if (h > 0) return `${h}h ${m}m`
  if (m > 0) return `${m}m ${s}s`
  return `${s}s`
}
