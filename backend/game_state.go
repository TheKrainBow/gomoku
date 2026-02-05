package main

type PlayerColor int

type GameStatus int

const (
	PlayerBlack PlayerColor = iota
	PlayerWhite
)

const (
	StatusNotStarted GameStatus = iota
	StatusRunning
	StatusBlackWon
	StatusWhiteWon
	StatusDraw
)

type GameState struct {
	Board              Board
	ToMove             PlayerColor
	Status             GameStatus
	HasLastMove        bool
	LastMove           Move
	CapturedBlack      int
	CapturedWhite      int
	Hash               uint64
	HashSym            [8]uint64
	CanonHash          uint64
	MustCapture        bool
	ForcedCaptureMoves []Move
	LastMessage        string
	WinningLine        []Move
}

func DefaultGameState(settings GameSettings) GameState {
	state := GameState{}
	state.Reset(settings)
	return state
}

func (s *GameState) Reset(settings GameSettings) {
	s.Board = NewBoard(settings.BoardSize)
	if settings.BlackStarts {
		s.ToMove = PlayerBlack
	} else {
		s.ToMove = PlayerWhite
	}
	s.Status = StatusNotStarted
	s.HasLastMove = false
	s.LastMove = Move{X: -1, Y: -1}
	s.CapturedBlack = 0
	s.CapturedWhite = 0
	s.Hash = 0
	s.HashSym = [8]uint64{}
	s.CanonHash = 0
	s.MustCapture = false
	s.ForcedCaptureMoves = nil
	s.LastMessage = ""
	s.WinningLine = nil
	s.recomputeHashes()
}

func (s GameState) Clone() GameState {
	clone := s
	clone.Board = s.Board.Clone()
	clone.ForcedCaptureMoves = append([]Move(nil), s.ForcedCaptureMoves...)
	clone.WinningLine = append([]Move(nil), s.WinningLine...)
	return clone
}

func otherPlayer(player PlayerColor) PlayerColor {
	if player == PlayerBlack {
		return PlayerWhite
	}
	return PlayerBlack
}

func (s *GameState) recomputeHashes() {
	hash, sym := computeSymmetricHashes(*s)
	s.Hash = hash
	s.HashSym = sym
	s.CanonHash = canonicalSymHash(sym)
}
