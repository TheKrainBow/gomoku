package main

type HumanPlayer struct {
	pending     bool
	pendingMove Move
}

func NewHumanPlayer() *HumanPlayer {
	return &HumanPlayer{}
}

func (h *HumanPlayer) IsHuman() bool {
	return true
}

func (h *HumanPlayer) ChooseMove(GameState, Rules) Move {
	return Move{}
}

func (h *HumanPlayer) SetPendingMove(move Move) {
	h.pendingMove = move
	h.pending = true
}

func (h *HumanPlayer) HasPendingMove() bool {
	return h.pending
}

func (h *HumanPlayer) TakePendingMove() Move {
	h.pending = false
	return h.pendingMove
}
