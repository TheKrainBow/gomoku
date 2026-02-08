import { useEffect, useMemo, useRef, useState } from 'react'

const size = 7
const alphaBetaStartStep = 5

const nodeDefs = [
  { id: 'n0', parent: null, depth: 0, move: null, player: 1, label: 'Root' },

  { id: 'n1', parent: 'n0', depth: 1, move: { x: 2, y: 3 }, player: 2, label: 'W(2,3)' },
  { id: 'n2', parent: 'n0', depth: 1, move: { x: 3, y: 2 }, player: 2, label: 'W(3,2)' },

  { id: 'n3', parent: 'n1', depth: 2, move: { x: 4, y: 3 }, player: 1, label: 'B(4,3)' },
  { id: 'n4', parent: 'n1', depth: 2, move: { x: 2, y: 2 }, player: 1, label: 'B(2,2)' },
  { id: 'n5', parent: 'n2', depth: 2, move: { x: 3, y: 1 }, player: 1, label: 'B(3,1)' },
  { id: 'n6', parent: 'n2', depth: 2, move: { x: 2, y: 3 }, player: 1, label: 'B(2,3)' },

  { id: 'n7', parent: 'n3', depth: 3, move: { x: 5, y: 3 }, player: 2, label: 'W(5,3)', leafScore: 180 },
  { id: 'n8', parent: 'n3', depth: 3, move: { x: 4, y: 2 }, player: 2, label: 'W(4,2)', leafScore: 120 },
  { id: 'n9', parent: 'n4', depth: 3, move: { x: 1, y: 3 }, player: 2, label: 'W(1,3)', leafScore: -120 },
  { id: 'n10', parent: 'n4', depth: 3, move: { x: 2, y: 4 }, player: 2, label: 'W(2,4)', leafScore: -120 },
  { id: 'n11', parent: 'n5', depth: 3, move: { x: 2, y: 2 }, player: 2, label: 'W(2,2)', leafScore: -20 },
  { id: 'n12', parent: 'n5', depth: 3, move: { x: 4, y: 1 }, player: 2, label: 'W(4,1)', leafScore: 15 },
  { id: 'n13', parent: 'n6', depth: 3, move: { x: 4, y: 3 }, player: 2, label: 'W(4,1)', leafScore: 120 },
  { id: 'n14', parent: 'n6', depth: 3, move: { x: 4, y: 2 }, player: 2, label: 'W(4,2)', leafScore: 0 }
]

const layoutPositions = {
  n0: { x: 50, y: 4 },
  n1: { x: 30, y: 24 },
  n2: { x: 70, y: 24 },
  n3: { x: 14, y: 44 },
  n4: { x: 38, y: 44 },
  n5: { x: 62, y: 44 },
  n6: { x: 86, y: 44 },
  n7: { x: 7, y: 63 },
  n8: { x: 19, y: 63 },
  n9: { x: 31, y: 63 },
  n10: { x: 43, y: 63 },
  n11: { x: 57, y: 63 },
  n12: { x: 69, y: 63 },
  n13: { x: 81, y: 63 },
  n14: { x: 93, y: 63 }
}

const baseStepHelp = [
  'Step 1: Generate the depth-3 tree (no score yet).',
  'Step 2: Evaluate all depth-3 leaves.',
  'Step 3: Go up to depth-2 using MAX (white chooses the biggest score).',
  'Step 4: Go up to depth-1 using MIN (black chooses the smallest score).',
  'Step 5: Go up to root using MAX and pick final move.'
]

const capturedCellsByNode = {
  // B(3,3), W(2,3), B(4,3), W(5,3) => white capture of the two blue stones.
  n7: new Set(['3,3', '4,3'])
}

function buildMap(defs) {
  const map = new Map()
  for (const node of defs) {
    map.set(node.id, { ...node, children: [] })
  }
  for (const node of defs) {
    if (!node.parent) {
      continue
    }
    map.get(node.parent).children.push(node.id)
  }
  return map
}

function collectMoves(nodeId, nodes) {
  const stack = []
  let cur = nodes.get(nodeId)
  while (cur) {
    if (cur.move) {
      stack.push({ ...cur.move, player: cur.player })
    }
    if (!cur.parent) {
      break
    }
    cur = nodes.get(cur.parent)
  }
  stack.push({ x: 3, y: 3, player: 1 })
  return stack.reverse()
}

function collectLeafIds(nodeId, nodes) {
  const node = nodes.get(nodeId)
  if (!node) return []
  if (node.depth === 3) return [nodeId]
  let out = []
  for (const child of node.children || []) {
    out = out.concat(collectLeafIds(child, nodes))
  }
  return out
}

function buildBoard(moves) {
  const board = Array.from({ length: size }, () => Array(size).fill(0))
  let idx = 1
  for (const move of moves) {
    board[move.y][move.x] = idx
    idx++
  }
  return board
}

function computeMinmax(nodeId, nodes) {
  const node = nodes.get(nodeId)
  if (node.depth === 3) {
    return { value: node.leafScore, chosenChild: '' }
  }
  const children = node.children
  if (!children || children.length === 0) {
    return { value: 0, chosenChild: '' }
  }
  const childResults = children.map((id) => ({ id, ...computeMinmax(id, nodes) }))
  const useMax = node.depth % 2 === 0
  let best = childResults[0]
  for (let i = 1; i < childResults.length; i++) {
    const cur = childResults[i]
    if (useMax ? cur.value > best.value : cur.value < best.value) {
      best = cur
    }
  }
  return { value: best.value, chosenChild: best.id }
}

function scoreVisibleAtStep(depth, step) {
  if (depth === 3) return step >= 1
  if (depth === 2) return step >= 2
  if (depth === 1) return step >= 3
  return step >= 4
}

function edgeVisibleAtStep(parentDepth, step) {
  if (parentDepth === 2) return step >= 2 // depth3 -> depth2 resolved
  if (parentDepth === 1) return step >= 3 // depth2 -> depth1 resolved
  if (parentDepth === 0) return step >= 4 // depth1 -> root resolved
  return false
}

function nodeHighlightVisible(node, nodes, step, alphaBeta) {
  if (!node.parent) return false
  const parent = nodes.get(node.parent)
  if (!parent) return false
  if (step <= 4) {
    return edgeVisibleAtStep(parent.depth, step)
  }
  return step >= (alphaBeta.nodeResolveStep.get(parent.id) || Number.MAX_SAFE_INTEGER)
}

function orderedChildrenForAlphaBeta(nodeId, children) {
  // Deterministic ordering that creates clear pruning in this demo:
  // root: n2 first, n1 second
  // n1: n4 then n3
  // n2: n6 then n5
  if (nodeId === 'n0') {
    return ['n2', 'n1']
  }
  if (nodeId === 'n1') {
    return ['n4', 'n3']
  }
  if (nodeId === 'n2') {
    return ['n6', 'n5']
  }
  return [...children]
}

function markLeaves(nodeId, nodes, bucket, stepMap, step) {
  const node = nodes.get(nodeId)
  if (!node) return
  if (node.depth === 3) {
    bucket.add(nodeId)
    stepMap.set(nodeId, step)
    return
  }
  for (const child of node.children || []) {
    markLeaves(child, nodes, bucket, stepMap, step)
  }
}

function computeAlphaBeta(rootId, nodes) {
  const chosenByNode = new Map()
  const evaluatedLeaves = new Set()
  const prunedLeaves = new Set()
  const leafEvalStep = new Map()
  const leafPruneStep = new Map()
  const nodeResolveStep = new Map()
  const stepEvents = new Map()
  let currentStep = alphaBetaStartStep
  stepEvents.set(alphaBetaStartStep, 'Step 6: Alpha-beta starts (ordered search, right branch first).')

  const record = (message) => {
    currentStep++
    stepEvents.set(currentStep, message)
    return currentStep
  }

  const visit = (nodeId, alpha, beta) => {
    const node = nodes.get(nodeId)
    if (!node) {
      return 0
    }
    if (node.depth === 3) {
      evaluatedLeaves.add(nodeId)
      const s = record(`AB evaluate ${node.label}: score=${node.leafScore}`)
      leafEvalStep.set(nodeId, s)
      return node.leafScore
    }
    const children = orderedChildrenForAlphaBeta(nodeId, node.children || [])
    const isMax = node.depth % 2 === 0
    let bestChild = ''
    let best = isMax ? Number.NEGATIVE_INFINITY : Number.POSITIVE_INFINITY

    for (let i = 0; i < children.length; i++) {
      const childId = children[i]
      const score = visit(childId, alpha, beta)
      if (isMax ? score > best : score < best) {
        best = score
        bestChild = childId
      }
      if (isMax) {
        if (best > alpha) alpha = best
      } else if (best < beta) {
        beta = best
      }
      if (beta <= alpha) {
        const pruneStep = record(`AB cutoff at ${node.label}: prune remaining sibling branch(es)`)
        for (let j = i + 1; j < children.length; j++) {
          markLeaves(children[j], nodes, prunedLeaves, leafPruneStep, pruneStep)
        }
        break
      }
    }
    if (bestChild) {
      chosenByNode.set(nodeId, bestChild)
    }
    const resolveStep = record(`AB resolve ${node.label}: value=${best} via ${bestChild || 'n/a'}`)
    nodeResolveStep.set(nodeId, resolveStep)
    return best
  }

  const value = visit(rootId, Number.NEGATIVE_INFINITY, Number.POSITIVE_INFINITY)
  return {
    value,
    chosenByNode,
    evaluatedLeaves,
    prunedLeaves,
    leafEvalStep,
    leafPruneStep,
    nodeResolveStep,
    stepEvents,
    maxStep: currentStep
  }
}

export default function MinmaxPage() {
  const [step, setStep] = useState(0)
  const [treeSize, setTreeSize] = useState({ width: 0, height: 0 })
  const [lineSegments, setLineSegments] = useState([])
  const treeRef = useRef(null)
  const nodeRefs = useRef(new Map())
  const nodes = useMemo(() => buildMap(nodeDefs), [])

  const nodePositions = useMemo(() => {
    const positions = new Map()
    for (const node of nodes.values()) {
      const pos = layoutPositions[node.id]
      if (pos) {
        positions.set(node.id, pos)
      }
    }
    return positions
  }, [nodes])

  const minimax = useMemo(() => computeMinmax('n0', nodes), [nodes])
  const alphaBeta = useMemo(() => computeAlphaBeta('n0', nodes), [nodes])
  const maxStep = alphaBeta.maxStep
  const chosenByNode = useMemo(() => {
    const map = new Map()
    const visit = (id) => {
      const node = nodes.get(id)
      if (!node || node.depth === 3) return computeMinmax(id, nodes)
      const result = computeMinmax(id, nodes)
      map.set(id, result.chosenChild)
      for (const child of node.children) {
        visit(child)
      }
      return result
    }
    visit('n0')
    return map
  }, [nodes])
  const leavesByNode = useMemo(() => {
    const map = new Map()
    for (const node of nodes.values()) {
      map.set(node.id, collectLeafIds(node.id, nodes))
    }
    return map
  }, [nodes])

  useEffect(() => {
    const updateLines = () => {
      const treeEl = treeRef.current
      if (!treeEl) return
      const treeRect = treeEl.getBoundingClientRect()
      const nextSegments = []
      for (const node of nodes.values()) {
        const fromEl = nodeRefs.current.get(node.id)
        if (!fromEl || !node.children) continue
        const fromRect = fromEl.getBoundingClientRect()
        for (const childId of node.children) {
          const toEl = nodeRefs.current.get(childId)
          if (!toEl) continue
          const toRect = toEl.getBoundingClientRect()
          nextSegments.push({
            from: node.id,
            to: childId,
            x1: fromRect.left - treeRect.left + fromRect.width / 2,
            y1: fromRect.bottom - treeRect.top,
            x2: toRect.left - treeRect.left + toRect.width / 2,
            y2: toRect.top - treeRect.top
          })
        }
      }
      setLineSegments(nextSegments)
      setTreeSize({
        width: treeEl.scrollWidth,
        height: treeEl.scrollHeight
      })
    }

    updateLines()
    const treeEl = treeRef.current
    const observer = new ResizeObserver(updateLines)
    if (treeEl) {
      observer.observe(treeEl)
      treeEl.addEventListener('scroll', updateLines, { passive: true })
    }
    window.addEventListener('resize', updateLines)
    return () => {
      observer.disconnect()
      if (treeEl) {
        treeEl.removeEventListener('scroll', updateLines)
      }
      window.removeEventListener('resize', updateLines)
    }
  }, [nodes, nodePositions, step])

  const stepTitle = step <= 4 ? baseStepHelp[step] || '' : alphaBeta.stepEvents.get(step) || 'Alpha-beta step'

  const getNodeScore = (node) => {
    if (node.depth === 3) return node.leafScore
    return computeMinmax(node.id, nodes).value
  }

  return (
    <div className="page minmax-page">
      <header className="app-header">
        <div>
          <h1>Minmax Visualizer</h1>
          <p>Depth-3 simulation without alpha-beta pruning.</p>
        </div>
        <a className="cache-link" href="/">
          Back to game
        </a>
      </header>

      <section className="panel minmax-controls">
        <div className="minmax-step">{stepTitle}</div>
        <div className="actions">
          <button type="button" onClick={() => setStep((prev) => Math.max(0, prev - 1))}>
            Prev
          </button>
          <button type="button" onClick={() => setStep((prev) => Math.min(maxStep, prev + 1))}>
            Next
          </button>
          <span className="minmax-step-indicator">
            Step {step + 1} / {maxStep + 1}
          </span>
        </div>
      </section>

      <section className="panel minmax-tree" ref={treeRef}>
        <svg
          className="minmax-lines"
          width={Math.max(treeSize.width, 1)}
          height={Math.max(treeSize.height, 1)}
          viewBox={`0 0 ${Math.max(treeSize.width, 1)} ${Math.max(treeSize.height, 1)}`}
          preserveAspectRatio="none"
        >
          {lineSegments.map((segment) => {
            const parentNode = nodes.get(segment.from)
            const subtreeLeaves = leavesByNode.get(segment.to) || []
            const allPruned =
              subtreeLeaves.length > 0 && subtreeLeaves.every((leafId) => alphaBeta.prunedLeaves.has(leafId))
            const pruneVisible =
              allPruned &&
              subtreeLeaves.some(
                (leafId) => step >= (alphaBeta.leafPruneStep.get(leafId) || Number.MAX_SAFE_INTEGER)
              )
            const selected =
              step <= 4
                ? chosenByNode.get(segment.from) === segment.to && edgeVisibleAtStep(parentNode?.depth ?? -1, step)
                : alphaBeta.chosenByNode.get(segment.from) === segment.to &&
                  step >= (alphaBeta.nodeResolveStep.get(segment.from) || Number.MAX_SAFE_INTEGER)
            return (
              <line
                key={`${segment.from}-${segment.to}`}
                x1={segment.x1}
                y1={segment.y1}
                x2={segment.x2}
                y2={segment.y2}
                className={pruneVisible ? 'line-pruned' : selected ? 'line-selected' : 'line-normal'}
              />
            )
          })}
        </svg>

        {Array.from(nodes.values()).map((node) => {
          const pos = nodePositions.get(node.id)
          const moves = collectMoves(node.id, nodes)
          const board = buildBoard(moves)
          const inAB = step >= alphaBetaStartStep
          const showScore = inAB
            ? node.depth === 3
              ? step >= (alphaBeta.leafEvalStep.get(node.id) || Number.MAX_SAFE_INTEGER)
              : step >= (alphaBeta.nodeResolveStep.get(node.id) || Number.MAX_SAFE_INTEGER)
            : scoreVisibleAtStep(node.depth, step)
          const score = inAB
            ? node.depth === 3
              ? alphaBeta.leafEvalStep.has(node.id) && step >= (alphaBeta.leafEvalStep.get(node.id) || Number.MAX_SAFE_INTEGER)
                ? node.leafScore
                : null
              : computeMinmax(node.id, nodes).value
            : getNodeScore(node)
          const chosen = node.parent
            ? inAB
              ? alphaBeta.chosenByNode.get(node.parent) === node.id
              : chosenByNode.get(node.parent) === node.id
            : false
          const showChosen = chosen && nodeHighlightVisible(node, nodes, step, alphaBeta)
          const pruned =
            inAB &&
            node.depth === 3 &&
            alphaBeta.prunedLeaves.has(node.id) &&
            step >= (alphaBeta.leafPruneStep.get(node.id) || Number.MAX_SAFE_INTEGER)
          const nodeLeaves = leavesByNode.get(node.id) || []
          const prunedSubtree =
            inAB &&
            node.depth < 3 &&
            nodeLeaves.length > 0 &&
            nodeLeaves.every((leafId) => alphaBeta.prunedLeaves.has(leafId)) &&
            nodeLeaves.some((leafId) => step >= (alphaBeta.leafPruneStep.get(leafId) || Number.MAX_SAFE_INTEGER))
          return (
            <article
              key={node.id}
              ref={(el) => {
                if (el) {
                  nodeRefs.current.set(node.id, el)
                } else {
                  nodeRefs.current.delete(node.id)
                }
              }}
              className={`minmax-node ${showChosen ? 'chosen-node' : ''} ${pruned || prunedSubtree ? 'pruned-node' : ''}`}
              style={{ left: `${pos?.x ?? 0}%`, top: `${pos?.y ?? 0}%` }}
            >
              <div className="minmax-node-head">
                <span>{node.label}</span>
                <span className="minmax-depth">d{node.depth}</span>
              </div>
              <div className="mini-7-board">
                {board.map((row, y) => (
                  <div className="mini-7-row" key={`${node.id}-r-${y}`}>
                    {row.map((cell, x) => (
                      <span
                        className={`mini-7-cell ${cell > 0 ? (cell % 2 === 1 ? 'p1' : 'p2') : ''} ${
                          capturedCellsByNode[node.id]?.has(`${x},${y}`) && cell % 2 === 1 ? 'captured-fade' : ''
                        }`}
                        key={`${node.id}-${x}-${y}`}
                      >
                        {cell > 0 ? cell : ''}
                      </span>
                    ))}
                  </div>
                ))}
              </div>
              <div className={`minmax-score ${pruned ? 'minmax-pruned' : ''}`}>
                {showScore ? (pruned ? 'score: pruned' : score == null ? 'score: ?' : `score: ${score}`) : 'score: ?'}
              </div>
            </article>
          )
        })}
      </section>

      <section className="panel minmax-legend">
        <div>Evaluation perspective: positive = White is better, negative = Black is better.</div>
        <div>
          {step <= 4
            ? 'Minmax phase: leaves are evaluated, then values go up by alternating MAX (white) and MIN (black).'
            : 'Alpha-beta phase: same scores, but some leaves are skipped when bounds prove they cannot change the final choice.'}
        </div>
        <div>
          Current root result:{' '}
          {step <= 4
            ? step >= 4
              ? `${minimax.value}`
              : '?'
            : step >= (alphaBeta.nodeResolveStep.get('n0') || Number.MAX_SAFE_INTEGER)
              ? `${alphaBeta.value}`
              : '?'}
        </div>
      </section>
    </div>
  )
}
