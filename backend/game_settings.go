package main

type PlayerType int

const (
	PlayerHuman PlayerType = iota
	PlayerAI
)

type GameSettings struct {
	BoardSize              int        `json:"board_size"`
	WinLength              int        `json:"win_length"`
	BlueType              PlayerType `json:"-"`
	RedType              PlayerType `json:"-"`
	BlueStarts            bool       `json:"blue_starts"`
	CaptureWinStones       int        `json:"capture_win_stones"`
	ForbidDoubleThreeBlue bool       `json:"forbid_double_three_blue"`
	ForbidDoubleThreeRed bool       `json:"forbid_double_three_red"`
	BlueHeuristics        *HeuristicConfig
	RedHeuristics        *HeuristicConfig
}

func DefaultGameSettings() GameSettings {
	return GameSettings{
		BoardSize:              19,
		WinLength:              5,
		BlueType:              PlayerHuman,
		RedType:              PlayerAI,
		BlueStarts:            true,
		CaptureWinStones:       10,
		ForbidDoubleThreeBlue: true,
		ForbidDoubleThreeRed: false,
	}
}
