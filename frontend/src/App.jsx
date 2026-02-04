import { useEffect, useMemo, useRef, useState } from 'react'

const defaultStatus = {
  settings: { mode: 'ai_vs_human' },
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
