package main

type HistoryEntry struct {
	Move              Move
	Player            PlayerColor
	CapturedPositions []Move
	ElapsedMs         float64
	IsAi              bool
	CapturedCount     int
	Depth             int
}

type MoveHistory struct {
	entries []HistoryEntry
}

func (h *MoveHistory) Clear() {
	h.entries = nil
}

func (h *MoveHistory) Push(entry HistoryEntry) {
	h.entries = append(h.entries, entry)
}

func (h MoveHistory) Size() int {
	return len(h.entries)
}

func (h MoveHistory) All() []HistoryEntry {
	return append([]HistoryEntry(nil), h.entries...)
}
