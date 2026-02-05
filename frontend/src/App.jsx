import { useEffect, useMemo, useRef, useState } from 'react'

const defaultStatus = {
  settings: { mode: 'ai_vs_human', human_player: 1 },
  config: {
    ghost_mode: true,
    log_depth_scores: false,
    ai_depth: 5,
    ai_timeout_ms: 0,
    ai_top_candidates: 6,
    ai_quick_win_exit: true,
    ai_tt_max_entries: 200000
  },
  board: [],
  next_player: 1,
  winner: 0,
  move_count: 0,
  board_size: 19,
  status: 'not_started',
  ai_thinking: false,
  history: [],
  captured_black: 0,
  captured_white: 0
}

function wsUrl(path) {
  const protocol = window.location.protocol === 'https:' ? 'wss' : 'ws'
  return `${protocol}://${window.location.host}${path}`
}

export default function App() {
  const [ping, setPing] = useState('pending')
  const [status, setStatus] = useState(defaultStatus)
  const [startBusy, setStartBusy] = useState(false)
  const [settingsBusy, setSettingsBusy] = useState(false)
  const [moveBusy, setMoveBusy] = useState(false)
  const [cellSize, setCellSize] = useState(24)
  const [cellGap, setCellGap] = useState(2)
  const [board, setBoard] = useState([])
  const wsRef = useRef(null)
  const ghostWsRef = useRef(null)
  const boardPanelRef = useRef(null)
  const pageRef = useRef(null)

  useEffect(() => {
    fetch('/api/ping')
      .then((res) => res.json())
      .then((data) => setPing(data.ok ? 'ok' : 'error'))
      .catch(() => setPing('error'))

    fetch('/api/status')
      .then((res) => res.json())
      .then((data) => {
        setStatus(data)
        setBoard(buildBoardFromHistory(data.history || [], data.board_size || 19))
      })
      .catch(() => {})
  }, [])

  useEffect(() => {
    const panel = boardPanelRef.current
    if (!panel) return

    const updateSize = () => {
      const rect = panel.getBoundingClientRect()
      const style = window.getComputedStyle(panel)
      const padX = parseFloat(style.paddingLeft) + parseFloat(style.paddingRight)
      const padY = parseFloat(style.paddingTop) + parseFloat(style.paddingBottom)
      const availableWidth = Math.max(0, rect.width - padX)
      const availableHeight = Math.max(0, rect.height - padY)
      const size = status.board_size || 19
      const gap = 2
      const maxByWidth = Math.floor((availableWidth - gap * (size - 1)) / size)
      const maxByHeight = Math.floor((availableHeight - gap * (size - 1)) / size)
      const maxSize = Math.max(10, Math.min(maxByWidth, maxByHeight))
      setCellGap(gap)
      setCellSize(Math.min(32, Math.max(12, maxSize)))
    }

    updateSize()
    const observer = new ResizeObserver(updateSize)
    observer.observe(panel)
    window.addEventListener('resize', updateSize)
    return () => {
      observer.disconnect()
      window.removeEventListener('resize', updateSize)
    }
  }, [status.board_size])

  useEffect(() => {
    const page = pageRef.current
    if (!page) return

    const updatePage = () => {
      const rect = page.getBoundingClientRect()
      document.documentElement.style.setProperty('--page-height', `${rect.height}px`)
      document.documentElement.style.setProperty('--page-top', `${rect.top}px`)
    }

    updatePage()
    const observer = new ResizeObserver(updatePage)
    observer.observe(page)
    window.addEventListener('resize', updatePage)
    return () => {
      observer.disconnect()
      window.removeEventListener('resize', updatePage)
    }
  }, [])

  useEffect(() => {
    const ws = new WebSocket(wsUrl('/ws/'))
    wsRef.current = ws

    ws.onmessage = (event) => {
      const msg = JSON.parse(event.data)
      if (msg.type === 'status') {
        setStatus(msg.payload)
        setBoard(buildBoardFromHistory(msg.payload.history || [], msg.payload.board_size || 19))
      }
      if (msg.type === 'history') {
        setStatus((prev) => ({
          ...prev,
          history: [...prev.history, ...(msg.payload.history || [])],
          move_count: prev.move_count + (msg.payload.history ? msg.payload.history.length : 0),
          next_player: (() => {
            if (msg.payload.history && msg.payload.history.length > 0) {
              const last = msg.payload.history[msg.payload.history.length - 1]
              return last.player === 1 ? 2 : 1
            }
            return prev.next_player
          })()
        }))
        setBoard((prev) => applyHistoryChanges(prev, msg.payload.history || [], status.board_size || 19))
      }
      if (msg.type === 'reset') {
        setStatus((prev) => ({
          ...prev,
          next_player: msg.payload.next_player,
          winner: msg.payload.winner,
          status: msg.payload.status,
          history: msg.payload.history || [],
          move_count: msg.payload.history ? msg.payload.history.length : 0,
          board_size: msg.payload.board_size || prev.board_size
        }))
        setBoard(buildBoardFromHistory(msg.payload.history || [], msg.payload.board_size || status.board_size || 19))
      }
      if (msg.type === 'settings') {
        setStatus((prev) => ({
          ...prev,
          settings: msg.payload.settings || prev.settings,
          config: msg.payload.config || prev.config
        }))
      }
    }

    ws.onopen = () => {
      ws.send(JSON.stringify({ type: 'request_status' }))
    }

    return () => {
      ws.close()
    }
  }, [])

  useEffect(() => {
    if (!status.config.ghost_mode) {
      if (ghostWsRef.current) {
        ghostWsRef.current.close()
        ghostWsRef.current = null
      }
      return
    }
    const ghostWs = new WebSocket(wsUrl('/ws/ghost'))
    ghostWsRef.current = ghostWs
    ghostWs.onmessage = () => {}
    ghostWs.onerror = () => {}
    return () => {
      ghostWs.close()
      ghostWsRef.current = null
    }
  }, [status.config.ghost_mode])

  useEffect(() => {
  }, [])

  const humanPlayer =
    status.settings.human_player && status.settings.human_player > 0
      ? status.settings.human_player
      : 1
  const boardRows = useMemo(() => {
    if (!board || board.length === 0) {
      return null
    }
    const isHumanTurn =
      status.settings.mode === 'human_vs_human' ||
      (status.settings.mode === 'ai_vs_human' && status.next_player === humanPlayer)
    return board.map((row, rowIndex) => (
        <div className="board-row" key={`row-${rowIndex}`}>
        {row.map((cell, colIndex) => (
          <div
            className={`board-cell player-${cell} ${
              cell === 0 && status.winner === 0 && isHumanTurn ? 'playable' : ''
            }`}
            key={`cell-${rowIndex}-${colIndex}`}
            role="button"
            tabIndex={0}
            onClick={() => handleCellClick(colIndex, rowIndex)}
          >
            {cell === 0 ? '' : cell}
          </div>
        ))}
      </div>
    ))
  }, [board, status.settings.mode, status.next_player, status.winner, humanPlayer])

  const canStart = status.status !== 'running'
  const canPlay =
    status.status === 'running' &&
    status.winner === 0 &&
    (status.settings.mode === 'human_vs_human' ||
      (status.settings.mode === 'ai_vs_human' && status.next_player === humanPlayer))

  const formatDuration = (msValue) => {
    if (msValue == null) return ''
    if (msValue < 1000) return `${msValue.toFixed(0)} ms`
    const seconds = msValue / 1000
    if (seconds < 60) return `${seconds.toFixed(2)} s`
    const minutes = seconds / 60
    if (minutes < 60) return `${minutes.toFixed(2)} min`
    const hours = minutes / 60
    return `${hours.toFixed(2)} h`
  }

  const captured = useMemo(() => {
    let black = 0
    let white = 0
    for (const entry of status.history || []) {
      if (entry.captured_count) {
        if (entry.player === 1) {
          black += entry.captured_count
        } else if (entry.player === 2) {
          white += entry.captured_count
        }
      }
    }
    return { black, white }
  }, [status.history])

  const handleStart = async () => {
    if (!canStart || startBusy) {
      return
    }
    setStartBusy(true)
    try {
      const res = await fetch('/api/start', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ settings: status.settings })
      })
      if (res.ok) {
        const data = await res.json()
        setStatus(data)
      }
    } finally {
      setStartBusy(false)
    }
  }

  const handleSettingsChange = (field, value) => {
    setStatus((prev) => ({
      ...prev,
      config: {
        ...prev.config,
        [field]: value
      }
    }))
  }

  const handleModeChange = (value) => {
    setStatus((prev) => ({
      ...prev,
      settings: {
        ...prev.settings,
        mode: value,
        human_player: value === 'ai_vs_human' ? prev.settings.human_player || 1 : 0
      }
    }))
  }

  const handleApplySettings = async () => {
    if (settingsBusy) {
      return
    }
    setSettingsBusy(true)
    try {
      const res = await fetch('/api/settings', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          settings: status.settings,
          config: status.config
        })
      })
      if (res.ok) {
        const data = await res.json()
        setStatus(data)
      }
    } finally {
      setSettingsBusy(false)
    }
  }

  const handleStop = async () => {
    if (startBusy) {
      return
    }
    setStartBusy(true)
    try {
      const res = await fetch('/api/stop', { method: 'POST' })
      if (res.ok) {
        const data = await res.json()
        setStatus(data)
      }
    } finally {
      setStartBusy(false)
    }
  }

  const handleCellClick = async (x, y) => {
    if (!canPlay || moveBusy) {
      return
    }
    if (!board || board.length === 0) {
      return
    }
    if (board[y][x] !== 0) {
      return
    }
    setMoveBusy(true)
    try {
      const res = await fetch('/api/move', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ x, y, player: status.next_player })
      })
      if (res.ok) {
        const data = await res.json()
        setStatus(data)
      }
    } finally {
      setMoveBusy(false)
    }
  }

  return (
    <div className="page" ref={pageRef}>
      <div className="layout">
        <section className="panel settings-panel">
          <h2>Settings</h2>
          <div className="panel-scroll">
            <div className="grid">
              <div>
                <strong>Status:</strong> {status.status}
              </div>
              <div>
                <strong>Next Player:</strong> {status.next_player}
              </div>
              <div>
                <strong>Winner:</strong> {status.winner || 'None'}
              </div>
            <div>
              <strong>Captured (B/W):</strong> {captured.black} / {captured.white}
            </div>
            </div>
            <div className="settings-grid">
              <label>
                Mode
                <select
                  value={status.settings.mode}
                  onChange={(event) => handleModeChange(event.target.value)}
                  disabled={status.status === 'running'}
                >
                  <option value="ai_vs_ai">AI vs AI</option>
                  <option value="ai_vs_human">AI vs Human</option>
                  <option value="human_vs_human">Human vs Human</option>
                </select>
              </label>
              <label>
                AI depth
                <input
                  type="number"
                  min="1"
                  value={status.config.ai_depth}
                  onChange={(event) => handleSettingsChange('ai_depth', Number(event.target.value))}
                />
              </label>
              <label>
                Top candidates
                <input
                  type="number"
                  min="1"
                  value={status.config.ai_top_candidates}
                  onChange={(event) => handleSettingsChange('ai_top_candidates', Number(event.target.value))}
                />
              </label>
              <label>
                Timeout (ms)
                <input
                  type="number"
                  min="0"
                  value={status.config.ai_timeout_ms}
                  onChange={(event) => handleSettingsChange('ai_timeout_ms', Number(event.target.value))}
                />
              </label>
              <label>
                Cache size
                <input
                  type="number"
                  min="0"
                  value={status.config.ai_tt_max_entries}
                  onChange={(event) => handleSettingsChange('ai_tt_max_entries', Number(event.target.value))}
                />
              </label>
              <label className="toggle">
                <input
                  type="checkbox"
                  checked={status.config.ghost_mode}
                  onChange={(event) => handleSettingsChange('ghost_mode', event.target.checked)}
                />
                Ghost mode
              </label>
              <label className="toggle">
                <input
                  type="checkbox"
                  checked={status.config.ai_quick_win_exit}
                  onChange={(event) => handleSettingsChange('ai_quick_win_exit', event.target.checked)}
                />
                Quick win exit
              </label>
              <label className="toggle">
                <input
                  type="checkbox"
                  checked={status.config.log_depth_scores}
                  onChange={(event) => handleSettingsChange('log_depth_scores', event.target.checked)}
                />
                Log depth scores
              </label>
            </div>
            <div className="actions">
              <button type="button" onClick={handleApplySettings} disabled={settingsBusy}>
                {settingsBusy ? 'Saving…' : 'Apply settings'}
              </button>
              {status.status === 'running' ? (
                <button type="button" className="danger" onClick={handleStop} disabled={startBusy}>
                  {startBusy ? 'Stopping…' : 'Stop game'}
                </button>
              ) : (
                <button type="button" onClick={handleStart} disabled={!canStart || startBusy}>
                  {startBusy ? 'Starting…' : 'Start game'}
                </button>
              )}
            </div>
          </div>
        </section>

        <section className="board-panel" ref={boardPanelRef}>
          <div
            className="board"
            style={{
              '--cell-size': `${cellSize}px`,
              '--cell-gap': `${cellGap}px`
            }}
          >
            {boardRows}
          </div>
        </section>

        <section className="panel history-panel">
          <h2>History</h2>
          <div className="history-list">
            {status.history.length === 0 && <div className="history-empty">No moves yet.</div>}
            {status.history.map((entry, index) => (
              <div className="history-item" key={`history-${index}`}>
                <span className={`history-pill player-${entry.player}`}>{entry.player === 1 ? 'B' : 'W'}</span>
                <span>
                  ({entry.x}, {entry.y})
                </span>
            <span className="history-time">{formatDuration(entry.elapsed_ms)}</span>
            <span className="history-depth">Depth {entry.depth || '-'}</span>
            <span className="history-type">{entry.is_ai ? 'AI' : 'Human'}</span>
                {entry.captured_count > 0 && (
                  <span className="history-capture">+{entry.captured_count}</span>
                )}
              </div>
            ))}
          </div>
        </section>
      </div>
    </div>
  )
}

function buildBoardFromHistory(history, size) {
  const board = Array.from({ length: size }, () => Array(size).fill(0))
  for (const entry of history) {
    board[entry.y][entry.x] = entry.player
    if (entry.captured_positions) {
      for (const captured of entry.captured_positions) {
        if (captured.y >= 0 && captured.y < size && captured.x >= 0 && captured.x < size) {
          board[captured.y][captured.x] = 0
        }
      }
    }
  }
  return board
}

function applyHistoryChanges(board, entries, sizeFallback) {
  if (!board || board.length === 0) {
    return buildBoardFromHistory(entries, sizeFallback)
  }
  const size = board.length
  const next = board.map((row) => row.slice())
  for (const entry of entries) {
    if (!entry.changes) continue
    for (const change of entry.changes) {
      if (change.y >= 0 && change.y < size && change.x >= 0 && change.x < size) {
        next[change.y][change.x] = change.value
      }
    }
  }
  return next
}
