package main

type PlayerType int

const (
	PlayerHuman PlayerType = iota
	PlayerAI
)

type GameSettings struct {
	BoardSize              int        `json:"board_size"`
	WinLength              int        `json:"win_length"`
	BlackType              PlayerType `json:"-"`
	WhiteType              PlayerType `json:"-"`
	BlackStarts            bool       `json:"black_starts"`
	AiMoveDelayMs          int        `json:"ai_move_delay_ms"`
	CaptureWinStones       int        `json:"capture_win_stones"`
	ForbidDoubleThreeBlack bool       `json:"forbid_double_three_black"`
	ForbidDoubleThreeWhite bool       `json:"forbid_double_three_white"`
}

func DefaultGameSettings() GameSettings {
	return GameSettings{
		BoardSize:              19,
		WinLength:              5,
		BlackType:              PlayerHuman,
		WhiteType:              PlayerAI,
		BlackStarts:            true,
		AiMoveDelayMs:          150,
		CaptureWinStones:       10,
		ForbidDoubleThreeBlack: true,
		ForbidDoubleThreeWhite: false,
	}
}
