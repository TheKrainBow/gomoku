import { useEffect, useMemo, useRef, useState } from 'react'

const defaultStatus = {
  settings: { mode: 'ai_vs_human', human_player: 1 },
  config: {
    ghost_mode: true,
    log_depth_scores: false,
    ai_depth: 5,
    ai_time_budget_ms: 400,
    ai_timeout_ms: 0,
    ai_use_tt_cache: true,
    ai_min_depth: 3,
    ai_quick_win_exit: true,
    ai_tt_max_entries: 200000,
    ai_analitics_top_boards: 10
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
  captured_white: 0,
  win_reason: '',
  winning_line: [],
  winning_capture_pair: [],
  capture_win_stones: 10,
  turn_started_at_ms: 0
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
  const [selectedHistoryIndex, setSelectedHistoryIndex] = useState(-1)
  const [hoveredHistoryIndex, setHoveredHistoryIndex] = useState(null)
  const [activeRightTab, setActiveRightTab] = useState('history')
  const [analiticsQueue, setAnaliticsQueue] = useState([])
  const [analiticsTotalInQueue, setAnaliticsTotalInQueue] = useState(0)
  const [ttCache, setTtCache] = useState({
    count: 0,
    capacity: 0,
    usage: 0,
    full: false,
    used_bytes: 0,
    capacity_bytes: 0,
    max_memory_bytes: 0,
    memory_usage: 0
  })
  const [analiticsNowMs, setAnaliticsNowMs] = useState(Date.now())
  const [turnNowMs, setTurnNowMs] = useState(Date.now())
  const [moveSuggestion, setMoveSuggestion] = useState(null)
  const wsRef = useRef(null)
  const ghostWsRef = useRef(null)
  const analiticsWsRef = useRef(null)
  const boardPanelRef = useRef(null)
  const historyListRef = useRef(null)
  const prevHistoryLenRef = useRef(0)
  const analiticsRefreshBusyRef = useRef(false)
  const autoSaveTimerRef = useRef(null)
  const saveInFlightRef = useRef(false)
  const queuedSettingsPayloadRef = useRef(null)

  const refreshAnaliticsQueue = async () => {
    if (analiticsRefreshBusyRef.current) {
      return
    }
    analiticsRefreshBusyRef.current = true
    try {
      const res = await fetch('/api/analitics/queue')
      if (!res.ok) {
        return
      }
      const data = await res.json()
      setAnaliticsQueue(data.queue || [])
      setAnaliticsTotalInQueue(data.total_in_queue || 0)
    } finally {
      analiticsRefreshBusyRef.current = false
    }
  }

  const refreshTtCache = async () => {
    try {
      const res = await fetch('/api/cache/tt')
      if (!res.ok) {
        return
      }
      const data = await res.json()
      setTtCache({
        count: data.count || 0,
        capacity: data.capacity || 0,
        usage: data.usage || 0,
        full: Boolean(data.full),
        used_bytes: data.used_bytes || 0,
        capacity_bytes: data.capacity_bytes || 0,
        max_memory_bytes: data.max_memory_bytes || 0,
        memory_usage: data.memory_usage || 0
      })
    } catch (_) {}
  }

  useEffect(() => {
    fetch('/api/ping')
      .then((res) => res.json())
      .then((data) => setPing(data.ok ? 'ok' : 'error'))
      .catch(() => setPing('error'))

    fetch('/api/status')
      .then((res) => res.json())
      .then((data) => {
        setStatus(data)
      })
      .catch(() => {})

    refreshAnaliticsQueue().catch(() => {})
    refreshTtCache().catch(() => {})
  }, [])

  useEffect(() => {
    const id = setInterval(() => {
      refreshTtCache().catch(() => {})
    }, 2000)
    return () => clearInterval(id)
  }, [])

  useEffect(() => () => {
    if (autoSaveTimerRef.current) {
      clearTimeout(autoSaveTimerRef.current)
      autoSaveTimerRef.current = null
    }
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
    const ws = new WebSocket(wsUrl('/ws/'))
    wsRef.current = ws

    ws.onmessage = (event) => {
      const msg = JSON.parse(event.data)
      if (msg.type === 'status') {
        setStatus(msg.payload)
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
      }
      if (msg.type === 'reset') {
        setStatus((prev) => ({
          ...prev,
          next_player: msg.payload.next_player,
          winner: msg.payload.winner,
          status: msg.payload.status,
          history: msg.payload.history || [],
          move_count: msg.payload.history ? msg.payload.history.length : 0,
          board_size: msg.payload.board_size || prev.board_size,
          win_reason: msg.payload.win_reason || '',
          winning_line: msg.payload.winning_line || [],
          winning_capture_pair: msg.payload.winning_capture_pair || [],
          capture_win_stones: msg.payload.capture_win_stones || prev.capture_win_stones || 10,
          turn_started_at_ms: msg.payload.turn_started_at_ms || prev.turn_started_at_ms || 0
        }))
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
      setMoveSuggestion(null)
      if (ghostWsRef.current) {
        ghostWsRef.current.close()
        ghostWsRef.current = null
      }
      return
    }
    const ghostWs = new WebSocket(wsUrl('/ws/ghost'))
    ghostWsRef.current = ghostWs
    ghostWs.onmessage = (event) => {
      const msg = JSON.parse(event.data)
      if (msg.type !== 'ghost') {
        return
      }
      const payload = msg.payload || {}
      if (payload.mode !== 'best_move') {
        return
      }
      if (!payload.active || !payload.best) {
        setMoveSuggestion(null)
        return
      }
      setMoveSuggestion({
        x: payload.best.x,
        y: payload.best.y,
        player: payload.best.player,
        depth: payload.depth || 0,
        score: payload.score || 0,
        next_player: payload.next_player || 0,
        history_len: payload.history_len || 0
      })
    }
    ghostWs.onerror = () => {}
    return () => {
      ghostWs.close()
      ghostWsRef.current = null
    }
  }, [status.config.ghost_mode])

  useEffect(() => {
  }, [])

  useEffect(() => {
    const timer = window.setInterval(() => {
      setAnaliticsNowMs(Date.now())
    }, 1000)
    return () => {
      window.clearInterval(timer)
    }
  }, [])

  useEffect(() => {
    const timer = window.setInterval(() => {
      setTurnNowMs(Date.now())
    }, 100)
    return () => {
      window.clearInterval(timer)
    }
  }, [])

  useEffect(() => {
    const ws = new WebSocket(wsUrl('/ws/analitics'))
    analiticsWsRef.current = ws
    ws.onmessage = (event) => {
      const msg = JSON.parse(event.data)
      if (msg.type !== 'analitics') {
        return
      }
      const payload = msg.payload || {}
      const eventType = payload.event
      const entry = payload.entry
      const total = payload.total_in_queue
      if (typeof total === 'number') {
        setAnaliticsTotalInQueue(total)
      }

      if (eventType === 'board_added' || eventType === 'board_hit' || eventType === 'board_left') {
        refreshAnaliticsQueue().catch(() => {})
        return
      }
      if (!entry || !entry.id) {
        return
      }
      setAnaliticsQueue((prev) =>
        prev.map((item) =>
          item.id === entry.id
            ? {
                ...item,
                current_depth: entry.current_depth,
                target_depth: entry.target_depth,
                hits: entry.hits,
                analyzing: entry.analyzing,
                analysis_started_at_ms: entry.analysis_started_at_ms
              }
            : item
        )
      )
    }
    ws.onerror = () => {}
    return () => {
      ws.close()
      analiticsWsRef.current = null
    }
  }, [])

  const history = status.history || []
  const lastHistoryEntry = history.length > 0 ? history[history.length - 1] : null
  const latestHistoryIndex = history.length - 1
  const effectiveHistoryIndex =
    hoveredHistoryIndex != null
      ? hoveredHistoryIndex
      : selectedHistoryIndex >= 0
      ? selectedHistoryIndex
      : latestHistoryIndex

  const liveSnapshot = useMemo(
    () => buildBoardSnapshot(history, status.board_size || 19, latestHistoryIndex),
    [history, status.board_size, latestHistoryIndex]
  )

  const displayedSnapshot = useMemo(
    () => buildBoardSnapshot(history, status.board_size || 19, effectiveHistoryIndex),
    [history, status.board_size, effectiveHistoryIndex]
  )

  useEffect(() => {
    if (selectedHistoryIndex >= history.length) {
      setSelectedHistoryIndex(-1)
    }
    if (hoveredHistoryIndex != null && hoveredHistoryIndex >= history.length) {
      setHoveredHistoryIndex(null)
    }
  }, [history.length, selectedHistoryIndex, hoveredHistoryIndex])

  useEffect(() => {
    const currentLen = history.length
    if (currentLen > prevHistoryLenRef.current) {
      const list = historyListRef.current
      if (list) {
        list.scrollTop = list.scrollHeight
      }
    }
    prevHistoryLenRef.current = currentLen
  }, [history.length])

  const humanPlayer =
    status.settings.human_player && status.settings.human_player > 0
      ? status.settings.human_player
      : 1
  const winningLineSet = useMemo(() => {
    const set = new Set()
    if (
      status.winner === 0 ||
      status.win_reason !== 'alignment' ||
      effectiveHistoryIndex !== latestHistoryIndex
    ) {
      return set
    }
    for (const cell of status.winning_line || []) {
      set.add(`${cell.x},${cell.y}`)
    }
    return set
  }, [status.winner, status.win_reason, status.winning_line, effectiveHistoryIndex, latestHistoryIndex])
  const captureWinPairSet = useMemo(() => {
    const set = new Set()
    if (
      status.winner === 0 ||
      status.win_reason !== 'capture' ||
      effectiveHistoryIndex !== latestHistoryIndex
    ) {
      return set
    }
    let pair = status.winning_capture_pair || []
    if (
      pair.length === 0 &&
      lastHistoryEntry &&
      lastHistoryEntry.player === status.winner &&
      (lastHistoryEntry.captured_positions || []).length > 0
    ) {
      pair = lastHistoryEntry.captured_positions
    }
    for (const cell of pair) {
      set.add(`${cell.x},${cell.y}`)
    }
    return set
  }, [
    status.winner,
    status.win_reason,
    status.winning_capture_pair,
    effectiveHistoryIndex,
    latestHistoryIndex,
    lastHistoryEntry
  ])
  const boardRows = useMemo(() => {
    if (!displayedSnapshot.board || displayedSnapshot.board.length === 0) {
      return null
    }
    const isHumanTurn =
      status.settings.mode === 'human_vs_human' ||
      (status.settings.mode === 'ai_vs_human' && status.next_player === humanPlayer)
    const showSuggestion =
      status.config.ghost_mode &&
      effectiveHistoryIndex === latestHistoryIndex &&
      !!moveSuggestion &&
      moveSuggestion.next_player === status.next_player &&
      moveSuggestion.history_len === history.length
    return displayedSnapshot.board.map((row, rowIndex) => (
        <div className="board-row" key={`row-${rowIndex}`}>
        {row.map((cell, colIndex) => (
          (() => {
            const isWinningLineCell = winningLineSet.has(`${colIndex},${rowIndex}`)
            const isWinningCaptureCell = captureWinPairSet.has(`${colIndex},${rowIndex}`)
            const isFinalCaptureWin =
              status.win_reason === 'capture' &&
              status.winner > 0 &&
              effectiveHistoryIndex === latestHistoryIndex &&
              lastHistoryEntry &&
              lastHistoryEntry.player === status.winner &&
              (lastHistoryEntry.captured_positions || []).length > 0
            const restoredCapturedStone = isFinalCaptureWin && isWinningCaptureCell && cell === 0
            const renderedCell = restoredCapturedStone ? (status.winner === 1 ? 2 : 1) : cell
            const isCaptureWinningMoveCell =
              isFinalCaptureWin &&
              lastHistoryEntry &&
              lastHistoryEntry.x === colIndex &&
              lastHistoryEntry.y === rowIndex &&
              renderedCell !== 0
            const moveNumber = displayedSnapshot.moveNumbers[rowIndex][colIndex]
            const isSuggestionCell =
              showSuggestion &&
              renderedCell === 0 &&
              colIndex === moveSuggestion.x &&
              rowIndex === moveSuggestion.y
            return (
          <div
            className={`board-cell player-${renderedCell} ${
              renderedCell === 0 && status.winner === 0 && isHumanTurn ? 'playable' : ''
            } ${isWinningLineCell && cell !== 0 ? `winning-line reverse-player-${cell}` : ''} ${
              isWinningCaptureCell && renderedCell !== 0
                ? restoredCapturedStone
                  ? 'winning-capture-target winning-capture-fade'
                  : 'winning-capture-target'
                : ''
            } ${isCaptureWinningMoveCell ? 'winning-capture-target winning-capture-fade' : ''} ${
              isCaptureWinningMoveCell ? 'winning-capture-move' : ''
            } ${isSuggestionCell ? `ghost-suggestion ghost-player-${moveSuggestion.player}` : ''} ${
              isSuggestionCell ? 'ghost-suggestion-animated' : ''
            }`}
            key={`cell-${rowIndex}-${colIndex}`}
            role="button"
            tabIndex={0}
            onClick={() => handleCellClick(colIndex, rowIndex)}
          >
            {isSuggestionCell ? '' : renderedCell === 0 || moveNumber <= 0 ? '' : moveNumber}
          </div>
            )
          })()
        ))}
      </div>
    ))
  }, [
    displayedSnapshot,
    status.settings.mode,
    status.next_player,
    status.winner,
    status.win_reason,
    humanPlayer,
    winningLineSet,
    captureWinPairSet,
    effectiveHistoryIndex,
    latestHistoryIndex,
    lastHistoryEntry,
    moveSuggestion,
    history.length,
    status.config.ghost_mode
  ])

  const canStart = status.status !== 'running'
  const canPlay =
    status.status === 'running' &&
    status.winner === 0 &&
    (status.settings.mode === 'human_vs_human' ||
      (status.settings.mode === 'ai_vs_human' && status.next_player === humanPlayer))
  const nextPlayerLabel = status.next_player === 1 ? 'Blue' : 'Red'
  const winnerLabel = status.winner === 1 ? 'Blue' : status.winner === 2 ? 'Red' : ''

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
    let blue = 0
    let red = 0
    for (const entry of history) {
      if (entry.captured_count) {
        if (entry.player === 1) {
          blue += entry.captured_count
        } else if (entry.player === 2) {
          red += entry.captured_count
        }
      }
    }
    return { blue, red }
  }, [history])

  const turnInfo = useMemo(() => {
    if (status.winner === 0) {
      if (status.status === 'draw') {
        return { prefix: 'Game ended in a draw', player: '', suffix: '', playerNumber: 0 }
      }
      return { prefix: 'Player ', player: nextPlayerLabel, suffix: "'s turn to play", playerNumber: status.next_player }
    }
    if (status.win_reason === 'alignment') {
      return { prefix: 'Player ', player: winnerLabel, suffix: ' won (by alignment)', playerNumber: status.winner }
    }
    if (status.win_reason === 'capture') {
      return {
        prefix: 'Player ',
        player: winnerLabel,
        suffix: ` won (with ${status.capture_win_stones || 10} captures)`,
        playerNumber: status.winner
      }
    }
    return { prefix: 'Player ', player: winnerLabel, suffix: ' won', playerNumber: status.winner }
  }, [status.winner, status.status, status.win_reason, status.capture_win_stones, status.next_player, nextPlayerLabel, winnerLabel])

  const statusLabel = useMemo(() => {
    if (status.status === 'black_won') return 'blue_won'
    if (status.status === 'white_won') return 'red_won'
    return status.status
  }, [status.status])

  const currentTurnElapsedMs =
    status.status === 'running' && status.turn_started_at_ms > 0
      ? Math.max(0, turnNowMs - status.turn_started_at_ms)
      : 0

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

  const flushQueuedSettings = async () => {
    if (saveInFlightRef.current) {
      return
    }
    const payload = queuedSettingsPayloadRef.current
    if (!payload) {
      return
    }
    queuedSettingsPayloadRef.current = null
    saveInFlightRef.current = true
    setSettingsBusy(true)
    try {
      const res = await fetch('/api/settings', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload)
      })
      if (res.ok) {
        const data = await res.json()
        setStatus(data)
      }
    } finally {
      saveInFlightRef.current = false
      setSettingsBusy(false)
      if (queuedSettingsPayloadRef.current) {
        flushQueuedSettings()
      }
    }
  }

  const queueAutoSave = (nextSettings, nextConfig) => {
    queuedSettingsPayloadRef.current = {
      settings: nextSettings,
      config: nextConfig
    }
    if (autoSaveTimerRef.current) {
      clearTimeout(autoSaveTimerRef.current)
    }
    autoSaveTimerRef.current = setTimeout(() => {
      autoSaveTimerRef.current = null
      flushQueuedSettings()
    }, 180)
  }

  const handleSettingsChange = (field, value) => {
    setStatus((prev) => {
      const next = {
        ...prev,
        config: {
          ...prev.config,
          [field]: value
        }
      }
      queueAutoSave(next.settings, next.config)
      return next
    })
  }

  const handleModeChange = (value) => {
    setStatus((prev) => {
      const next = {
        ...prev,
        settings: {
          ...prev.settings,
          mode: value,
          human_player: value === 'ai_vs_human' ? prev.settings.human_player || 1 : 0
        }
      }
      queueAutoSave(next.settings, next.config)
      return next
    })
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
    if (!liveSnapshot.board || liveSnapshot.board.length === 0) {
      return
    }
    if (liveSnapshot.board[y][x] !== 0) {
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
    <div className="page">
      <header className="app-header">
        <div>
          <h1>Gomoku</h1>
          <p>Play on the built-in board.</p>
        </div>
        <div className="header-links">
          <a className="cache-link" href="/cache">
            TT cache
          </a>
          <a className="cache-link" href="/minmax">
            Minmax demo
          </a>
        </div>
      </header>
      <div className="layout">
        <section className="panel settings-panel">
          <h2>Settings</h2>
          <div className="panel-scroll">
            <div className="grid">
              <div>
                <strong>Status:</strong> {statusLabel}
              </div>
              <div>
                <strong>Next Player:</strong> {status.next_player === 1 ? 'Blue' : 'Red'}
              </div>
              <div>
                <strong>Winner:</strong> {status.winner === 0 ? 'None' : status.winner === 1 ? 'Blue' : 'Red'}
              </div>
            <div>
              <strong>Captured (Blue/Red):</strong> {captured.blue} / {captured.red}
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
                Timeout (ms)
                <input
                  type="number"
                  min="0"
                  value={status.config.ai_time_budget_ms ?? 0}
                  onChange={(event) => handleSettingsChange('ai_time_budget_ms', Number(event.target.value))}
                />
              </label>
              <label>
                TT cache usage
                <progress
                  value={ttCache.used_bytes}
                  max={Math.max(1, ttCache.max_memory_bytes || ttCache.capacity_bytes || 1)}
                />
                <div>
                  {formatBytes(ttCache.used_bytes)} / {formatBytes(ttCache.max_memory_bytes || ttCache.capacity_bytes)} (
                  {Math.round((ttCache.memory_usage || 0) * 100)}%)
                </div>
              </label>
              <label className="toggle">
                <input
                  type="checkbox"
                  checked={status.config.ghost_mode}
                  onChange={(event) => handleSettingsChange('ghost_mode', event.target.checked)}
                />
                Move suggestion
              </label>
              <label className="toggle">
                <input
                  type="checkbox"
                  checked={status.config.ai_use_tt_cache ?? true}
                  onChange={(event) => handleSettingsChange('ai_use_tt_cache', event.target.checked)}
                />
                Use TT Cache
              </label>
            </div>
            <div className="actions">
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
          <div className="turn-indicator">
            <span>{turnInfo.prefix}</span>
            {turnInfo.player && (
              <span className={`turn-player turn-player-${turnInfo.playerNumber === 1 ? 1 : 2}`}>
                {turnInfo.player}
              </span>
            )}
            <span>{turnInfo.suffix}</span>
          </div>
          {status.status === 'running' && (
            <div className="turn-timer">Current turn: {formatTurnDuration(currentTurnElapsedMs)}</div>
          )}
          <div
            className="board"
            style={{
              '--cell-size': `${cellSize}px`,
              '--cell-gap': `${cellGap}px`
            }}
          >
            {boardRows}
          </div>
          {status.config.ghost_mode && moveSuggestion && (
            <div className="turn-timer">
              Suggestion: ({moveSuggestion.x}, {moveSuggestion.y}) depth {moveSuggestion.depth}
            </div>
          )}
        </section>

        <section className="panel history-panel">
          <div className="tab-header">
            <button
              type="button"
              className={activeRightTab === 'history' ? 'tab-btn active' : 'tab-btn'}
              onClick={() => setActiveRightTab('history')}
            >
              History
            </button>
            <button
              type="button"
              className={activeRightTab === 'analitics' ? 'tab-btn active' : 'tab-btn'}
              onClick={() => setActiveRightTab('analitics')}
            >
              Analitics
            </button>
          </div>
          {activeRightTab === 'history' ? (
            <div
              className="history-list"
              ref={historyListRef}
              onMouseLeave={() => setHoveredHistoryIndex(null)}
            >
              {history.length === 0 && <div className="history-empty">No moves yet.</div>}
              {history.map((entry, index) => (
                (() => {
                  const isAutoFollowSelected = selectedHistoryIndex < 0 && index === history.length - 1
                  const isSelected = selectedHistoryIndex === index || isAutoFollowSelected
                  return (
                    <div
                      className={`history-item ${isSelected ? 'selected' : ''} ${
                        hoveredHistoryIndex === index ? 'hovered' : ''
                      }`}
                      key={`history-${index}`}
                      role="button"
                      tabIndex={0}
                      onMouseEnter={() => setHoveredHistoryIndex(index)}
                      onClick={() => {
                        if (index === history.length - 1) {
                          setSelectedHistoryIndex(-1)
                        } else {
                          setSelectedHistoryIndex(index)
                        }
                      }}
                    >
                      <span className="history-index">#{index + 1}</span>
                      <span className={`history-pill player-${entry.player}`}>{entry.player === 1 ? 'B' : 'R'}</span>
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
                  )
                })()
              ))}
            </div>
          ) : (
            <div className="analitics-list">
              {analiticsQueue.length === 0 && <div className="history-empty">No board in analysis queue.</div>}
              {analiticsQueue.map((entry) => (
                <div className={`analitics-item ${entry.analyzing ? 'running' : ''}`} key={entry.id}>
                  <div className="analitics-preview">{renderMiniBoard(entry.board)}</div>
                  <div className="analitics-meta">
                    <div className="analitics-id">{entry.id}</div>
                    <div>Hits: {entry.hits}</div>
                    <div>
                      Depth: {entry.current_depth || 0}/{entry.target_depth || 0}
                    </div>
                    {entry.analyzing && (
                      <div className="analitics-running">
                        Running for {formatElapsedSince(entry.analysis_started_at_ms, analiticsNowMs)}
                      </div>
                    )}
                  </div>
                </div>
              ))}
              {analiticsTotalInQueue > analiticsQueue.length && (
                <div className="analitics-more">And {analiticsTotalInQueue - analiticsQueue.length} more boards</div>
              )}
            </div>
          )}
        </section>
      </div>
    </div>
  )
}

function buildBoardSnapshot(history, size, upToIndex) {
  if (upToIndex < 0) {
    return {
      board: Array.from({ length: size }, () => Array(size).fill(0)),
      moveNumbers: Array.from({ length: size }, () => Array(size).fill(0))
    }
  }
  const board = Array.from({ length: size }, () => Array(size).fill(0))
  const moveNumbers = Array.from({ length: size }, () => Array(size).fill(0))
  const cappedIndex = Math.min(upToIndex, history.length - 1)
  for (let i = 0; i <= cappedIndex; i++) {
    const entry = history[i]
    board[entry.y][entry.x] = entry.player
    moveNumbers[entry.y][entry.x] = i + 1
    if (entry.captured_positions) {
      for (const captured of entry.captured_positions) {
        if (captured.y >= 0 && captured.y < size && captured.x >= 0 && captured.x < size) {
          board[captured.y][captured.x] = 0
        }
      }
    }
  }
  return { board, moveNumbers }
}

function renderMiniBoard(board) {
  if (!board || board.length === 0) {
    return null
  }
  return (
    <div className="mini-board" style={{ gridTemplateColumns: `repeat(${board.length}, 1fr)` }}>
      {board.flatMap((row, y) =>
        row.map((cell, x) => (
          <span className={`mini-cell player-${cell}`} key={`mini-${y}-${x}`} />
        ))
      )}
    </div>
  )
}

function formatElapsedSince(startMs, nowMs) {
  if (!startMs || startMs <= 0) {
    return '0s'
  }
  const total = Math.max(0, Math.floor((nowMs - startMs) / 1000))
  if (total < 60) {
    return `${total}s`
  }
  const minutes = Math.floor(total / 60)
  const seconds = total % 60
  if (minutes < 60) {
    return `${minutes}m ${seconds}s`
  }
  const hours = Math.floor(minutes / 60)
  const remainMinutes = minutes % 60
  return `${hours}h ${remainMinutes}m`
}

function formatTurnDuration(msValue) {
  const safe = Math.max(0, msValue || 0)
  const totalSeconds = Math.floor(safe / 1000)
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  const tenths = Math.floor((safe % 1000) / 100)
  return `${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}.${tenths}`
}

function formatBytes(value) {
  const bytes = Math.max(0, Number(value) || 0)
  if (bytes < 1024) {
    return `${bytes} B`
  }
  const units = ['KB', 'MB', 'GB', 'TB']
  let current = bytes / 1024
  let idx = 0
  while (current >= 1024 && idx < units.length - 1) {
    current /= 1024
    idx++
  }
  return `${current.toFixed(2)} ${units[idx]}`
}
