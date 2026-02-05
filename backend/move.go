package main

type Move struct {
	X     int `json:"x"`
	Y     int `json:"y"`
	Depth int `json:"depth,omitempty"`
}

func NewMove(x, y int) Move {
	return Move{X: x, Y: y}
}

func (m Move) IsValid(boardSize int) bool {
	return m.X >= 0 && m.Y >= 0 && m.X < boardSize && m.Y < boardSize
}

func (m Move) Equals(other Move) bool {
	return m.X == other.X && m.Y == other.Y
}
