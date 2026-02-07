import { useEffect, useMemo, useState } from 'react'

const savedKey = 'gomoku_analyse_board'
const defaultBoardSize = 19
const defaultDepth = 5
const boardSizeOptions = [9, 11, 13, 15, 17, 19, 21]

function createBoard(size) {
  const board = []
  for (let y = 0; y < size; y++) {
    const row = new Array(size).fill(0)
    board.push(row)
  }
  return board
}

function resizeBoard(previous, size) {
  const next = []
  for (let y = 0; y < size; y++) {
    const row = []
    for (let x = 0; x < size; x++) {
      if (y < previous.length && x < previous[y].length) {
        row.push(previous[y][x])
      } else {
        row.push(0)
      }
    }
    next.push(row)
  }
  return next
}

function formatNumber(value) {
  return new Intl.NumberFormat().format(value)
}

export default function AnalysePage() {
  const [boardSize, setBoardSize] = useState(defaultBoardSize)
  const [board, setBoard] = useState(() => createBoard(defaultBoardSize))
  const [depth, setDepth] = useState(defaultDepth)
  const [nextPlayer, setNextPlayer] = useState(1)
  const [useTempCache, setUseTempCache] = useState(true)
  const [status, setStatus] = useState('idle')
  const [error, setError] = useState('')
  const [result, setResult] = useState(null)

  useEffect(() => {
    const saved = localStorage.getItem(savedKey)
    if (!saved) {
      return
    }
    try {
      const parsed = JSON.parse(saved)
      if (parsed.board && Array.isArray(parsed.board)) {
        const size = parsed.board.length
        if (size > 0) {
          setBoardSize(size)
          setBoard(resizeBoard(parsed.board, size))
        }
      }
      if (parsed.depth) {
        setDepth(parsed.depth)
      }
      if (parsed.nextPlayer) {
        setNextPlayer(parsed.nextPlayer)
      }
      if (typeof parsed.useTempCache === 'boolean') {
        setUseTempCache(parsed.useTempCache)
      }
    } catch (err) {
      // ignore
    }
  }, [])

  useEffect(() => {
    setBoard((prev) => resizeBoard(prev, boardSize))
  }, [boardSize])

  const bestMove = result?.best_move?.valid ? result.best_move : null
  const boardStyle = {
    '--cell-size': `${Math.max(16, Math.min(32, Math.floor(520 / boardSize)))}px`,
    '--cell-gap': '2px',
  }

  const boardRows = useMemo(() => {
    return board.map((row, rowIndex) => (
      <div className="board-row" key={`row-${rowIndex}`}>
        {row.map((cell, colIndex) => {
          const isBest = bestMove && bestMove.x === colIndex && bestMove.y === rowIndex
          const classes = [
            'board-cell',
            `player-${cell}`,
            cell === 0 ? 'playable' : '',
            isBest ? 'best-move' : '',
          ]
          return (
            <div
              key={`cell-${rowIndex}-${colIndex}`}
              className={classes.join(' ').trim()}
              onClick={() => {
                setBoard((prev) => {
                  const next = prev.map((r) => r.slice())
                  next[rowIndex][colIndex] = (next[rowIndex][colIndex] + 1) % 3
                  return next
                })
              }}
            >
              {cell === 1 ? 'X' : cell === 2 ? 'O' : ''}
            </div>
          )
        })}
      </div>
    ))
  }, [board, bestMove, boardSize])

  const stats = result
    ? [
        { label: 'Depth used', value: result.depth_used, suffix: '' },
        { label: 'Total time', value: `${result.duration_ms} ms`, suffix: '' },
        { label: 'Nodes evaluated', value: formatNumber(result.nodes), suffix: '' },
        { label: 'Heuristic calls', value: formatNumber(result.heuristic_calls), suffix: '' },
        { label: 'Heuristic time', value: `${result.heuristic_time_ms} ms`, suffix: '' },
        { label: 'Avg heuristic', value: `${result.avg_heuristic_ms.toFixed(2)} ms`, suffix: '' },
        { label: 'Board gen time', value: `${result.board_gen_time_ms} ms`, suffix: '' },
        { label: 'Board gen ops', value: formatNumber(result.board_gen_ops), suffix: '' },
        { label: 'TT probes', value: formatNumber(result.tt_probes), suffix: '' },
        { label: 'TT hits', value: formatNumber(result.tt_hits), suffix: '' },
        { label: 'TT stores', value: formatNumber(result.tt_stores), suffix: '' },
        { label: 'TT overwrites', value: formatNumber(result.tt_overwrites), suffix: '' },
      ]
    : []

  const handleBoardSizeChange = (value) => {
    const size = parseInt(value, 10)
    if (!Number.isNaN(size) && size >= 3 && size <= 25) {
      setBoardSize(size)
    }
  }

  const handleAnalyse = async () => {
    setStatus('running')
    setError('')
    setResult(null)
    try {
      const response = await fetch('/api/analyse', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          board,
          depth,
          next_player: nextPlayer,
          use_temp_cache: useTempCache,
        }),
      })
      const data = await response.json()
      if (!response.ok) {
        throw new Error(data.error || 'Analysis failed')
      }
      setResult(data)
      setStatus('done')
    } catch (err) {
      setError(err.message)
      setStatus('error')
    }
  }

  const handleSave = () => {
    localStorage.setItem(
      savedKey,
      JSON.stringify({
        board,
        depth,
        nextPlayer,
        useTempCache,
      })
    )
  }

  return (
    <div className="page analyse-page">
      <header className="analyse-header">
        <div>
          <h1>Board analysis</h1>
          <p>Drop stones, pick a depth, and inspect the full stats.</p>
        </div>
        <a className="analyse-link" href="/">
          ← Back to game
        </a>
      </header>

      <div className="analysis-layout">
        <section className="panel analysis-panel">
          <div className="settings-grid">
            <label>
              Board size
              <select value={boardSize} onChange={(event) => handleBoardSizeChange(event.target.value)}>
                {boardSizeOptions.map((size) => (
                  <option key={size} value={size}>
                    {size}×{size}
                  </option>
                ))}
              </select>
            </label>
            <label>
              Depth
              <input
                type="number"
                min="1"
                max="20"
                value={depth}
                onChange={(event) => setDepth(Math.max(1, Number(event.target.value)))}
              />
            </label>
            <label>
              Next player
              <select value={nextPlayer} onChange={(event) => setNextPlayer(Number(event.target.value))}>
                <option value={1}>Blue</option>
                <option value={2}>Red</option>
              </select>
            </label>
            <label className="toggle">
              <input type="checkbox" checked={useTempCache} onChange={(event) => setUseTempCache(event.target.checked)} />
              Use temporary cache
            </label>
          </div>

          <div className="actions">
            <button type="button" onClick={handleAnalyse} disabled={status === 'running'}>
              {status === 'running' ? 'Analyzing…' : 'Run analysis'}
            </button>
            <button type="button" onClick={handleSave}>
              Save board
            </button>
            <button type="button" onClick={() => setBoard(createBoard(boardSize))}>
              Clear board
            </button>
          </div>

          <div className="analysis-status">
            <div>Status: {status}</div>
            {error && <div className="pill error">{error}</div>}
            {result && (
              <div className="pill ok">
                Best move: {result.best_move.valid ? `(${result.best_move.x}, ${result.best_move.y})` : 'none'} | Score:{' '}
                {result.best_move.score.toFixed(2)}
              </div>
            )}
          </div>
        </section>

        <section className="panel board-panel">
          <div className="board" style={boardStyle}>
            {boardRows}
          </div>
        </section>

        <section className="panel stats-panel">
          <h2>Analysis stats</h2>
          <div className="analysis-stats">
            {stats.map((entry) => (
              <div className="stat-card" key={entry.label}>
                <strong>{entry.label}</strong>
                <span>{entry.value}</span>
              </div>
            ))}
            {!result && <div className="stat-help">Run an analysis to populate the stats.</div>}
          </div>
        </section>
      </div>
    </div>
  )
}
