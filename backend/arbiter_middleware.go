package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/go-chi/chi/v5"
)

const (
	arbiterAiStateOpen  = 1 << 0
	arbiterAiStateReady = 1 << 1
	arbiterAiStateBusy  = 1 << 2
)

type arbiterGameStatus string

const (
	arbiterStatusPlaying  arbiterGameStatus = "playing"
	arbiterStatusBlackWin arbiterGameStatus = "black_win"
	arbiterStatusWhiteWin arbiterGameStatus = "white_win"
	arbiterStatusDraw     arbiterGameStatus = "draw"
)

type arbiterSession struct {
	sessionID      string
	active         bool
	game           *Game
	previousConfig *Config
}

type arbiterMiddleware struct {
	mu       sync.Mutex
	sharedAI *AIPlayer
	session  arbiterSession
}

type arbiterPingResponse struct {
	State     int     `json:"state"`
	SessionID *string `json:"sessionid"`
	AliasID   *string `json:"session_id"`
}

type arbiterDoneResponse struct {
	Done string `json:"done"`
}

type arbiterPlayInterruptResponse struct {
	AsPlayed  bool   `json:"as_played"`
	Because   string `json:"because"`
	Msglog    string `json:"msglog,omitempty"`
	WinReason string `json:"win_reason,omitempty"`
}

type arbiterPlaySuccessResponse struct {
	AsPlayed  bool   `json:"as_played"`
	Move      int    `json:"move"`
	Board     []int  `json:"board"`
	Turn      int    `json:"turn"`
	Gstatus   string `json:"gstatus"`
	WinReason string `json:"win_reason,omitempty"`
}

var gomokuArbiter = newArbiterMiddleware()

func newArbiterMiddleware() *arbiterMiddleware {
	return &arbiterMiddleware{
		sharedAI: NewAIPlayer(),
	}
}

func registerArbiterRoutes(r chi.Router) {
	r.Get("/arbiter/ping", func(w http.ResponseWriter, req *http.Request) {
		writeJSON(w, http.StatusOK, gomokuArbiter.ping(req))
	})
	r.Get("/arbiter/start", func(w http.ResponseWriter, req *http.Request) {
		if err := gomokuArbiter.start(req); err != nil {
			writeJSON(w, err.status, map[string]string{"error": err.message})
			return
		}
		writeJSON(w, http.StatusOK, arbiterDoneResponse{Done: "OK"})
	})
	r.Get("/arbiter/play", func(w http.ResponseWriter, req *http.Request) {
		resp, err := gomokuArbiter.play(req)
		if err != nil {
			writeJSON(w, err.status, map[string]string{"error": err.message})
			return
		}
		writeJSON(w, http.StatusOK, resp)
	})
	r.Get("/arbiter/stop", func(w http.ResponseWriter, req *http.Request) {
		gomokuArbiter.stop(req)
		writeJSON(w, http.StatusOK, arbiterDoneResponse{Done: "OK"})
	})
}

type arbiterHTTPError struct {
	status  int
	message string
}

func newArbiterHTTPError(status int, message string) *arbiterHTTPError {
	return &arbiterHTTPError{status: status, message: message}
}

func (m *arbiterMiddleware) ping(req *http.Request) arbiterPingResponse {
	m.mu.Lock()
	defer m.mu.Unlock()

	state := arbiterAiStateOpen | arbiterAiStateReady
	if m.session.active {
		state = arbiterAiStateOpen | arbiterAiStateBusy
	}

	var sessionID *string
	if m.session.active && m.session.sessionID != "" {
		id := m.session.sessionID
		sessionID = &id
	}

	return arbiterPingResponse{
		State:     state,
		SessionID: sessionID,
		AliasID:   sessionID,
	}
}

func (m *arbiterMiddleware) start(req *http.Request) *arbiterHTTPError {
	sessionID, ok := queryValue(req, "sessionid", "session_id")
	if !ok || strings.TrimSpace(sessionID) == "" {
		return newArbiterHTTPError(http.StatusBadRequest, "missing sessionid")
	}
	colorRaw, ok := queryValue(req, "you_are")
	if !ok || strings.TrimSpace(colorRaw) == "" {
		return newArbiterHTTPError(http.StatusBadRequest, "missing you_are")
	}

	color, err := parseArbiterColor(colorRaw)
	if err != nil {
		return newArbiterHTTPError(http.StatusBadRequest, err.Error())
	}

	settings, err := arbiterSettingsFromRequest(req, color)
	if err != nil {
		return newArbiterHTTPError(http.StatusBadRequest, err.Error())
	}

	timeBudgetMs, hasTimeBudget := queryInt(req, "time_budget_ms", "ai_timeout_ms", "move_time_ms")
	if hasTimeBudget && timeBudgetMs <= 0 {
		return newArbiterHTTPError(http.StatusBadRequest, "invalid time_budget_ms")
	}

	force := hasQueryKey(req, "force")

	m.mu.Lock()
	defer m.mu.Unlock()

	if m.session.active && !force {
		return newArbiterHTTPError(http.StatusForbidden, "a game is already running")
	}

	var previousConfig *Config
	if hasTimeBudget {
		prev := GetConfig()
		next := prev
		next.AiTimeoutMs = timeBudgetMs
		next.AiTimeBudgetMs = timeBudgetMs
		configStore.Update(next)
		previousConfig = &prev
	}

	m.session = arbiterSession{
		sessionID:      sessionID,
		active:         true,
		game:           newArbiterGame(settings, color, m.sharedAI),
		previousConfig: previousConfig,
	}

	return nil
}

func (m *arbiterMiddleware) play(req *http.Request) (any, *arbiterHTTPError) {
	sessionID, ok := queryValue(req, "sessionid", "session_id")
	if !ok || strings.TrimSpace(sessionID) == "" {
		return nil, newArbiterHTTPError(http.StatusBadRequest, "missing sessionid")
	}
	gstatusRaw, ok := queryValue(req, "gstatus")
	if !ok || strings.TrimSpace(gstatusRaw) == "" {
		return nil, newArbiterHTTPError(http.StatusBadRequest, "missing gstatus")
	}
	requestStatus, err := parseArbiterGameStatus(gstatusRaw)
	if err != nil {
		return nil, newArbiterHTTPError(http.StatusBadRequest, err.Error())
	}

	moveRaw, movePresent := queryValue(req, "move")
	boardRaw, boardPresent := queryValue(req, "board")

	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.session.active {
		return nil, newArbiterHTTPError(http.StatusForbidden, "no active session")
	}
	if m.session.sessionID != sessionID {
		return nil, newArbiterHTTPError(http.StatusForbidden, "sessionid mismatch")
	}
	if m.session.game == nil {
		return nil, newArbiterHTTPError(http.StatusForbidden, "game not initialized")
	}

	game := m.session.game
	boardSize := game.settings.BoardSize

	if requestStatus != arbiterStatusPlaying {
		return arbiterPlayInterruptResponse{AsPlayed: false, Because: string(requestStatus), Msglog: "game already ended", WinReason: arbiterWinReasonFromGame(game)}, nil
	}

	if movePresent && strings.TrimSpace(moveRaw) == "" {
		return nil, newArbiterHTTPError(http.StatusBadRequest, "empty move")
	}
	if movePresent && !boardPresent {
		return nil, newArbiterHTTPError(http.StatusBadRequest, "missing board")
	}

	if movePresent {
		move, err := parseArbiterCellMove(moveRaw, boardSize)
		if err != nil {
			return nil, newArbiterHTTPError(http.StatusBadRequest, err.Error())
		}
		applied, reason := game.TryApplyMove(move)
		if !applied {
			return arbiterPlayInterruptResponse{AsPlayed: false, Because: "foe_wrongmove", Msglog: reason}, nil
		}
		parsedBoard, err := parseArbiterBoard(boardRaw, boardSize)
		if err != nil {
			return nil, newArbiterHTTPError(http.StatusBadRequest, err.Error())
		}
		if !boardsEqual(game.state.Board, parsedBoard) {
			return arbiterPlayInterruptResponse{AsPlayed: false, Because: "board_doesntmatch", Msglog: "board mismatch after opponent move"}, nil
		}
		if terminal := arbiterGameStatusFromState(game.state.Status); terminal != arbiterStatusPlaying {
			return arbiterPlayInterruptResponse{AsPlayed: false, Because: string(terminal), Msglog: "game already ended", WinReason: arbiterWinReasonFromGame(game)}, nil
		}
	} else if boardPresent {
		parsedBoard, err := parseArbiterBoard(boardRaw, boardSize)
		if err != nil {
			return nil, newArbiterHTTPError(http.StatusBadRequest, err.Error())
		}
		if !boardsEqual(game.state.Board, parsedBoard) {
			return arbiterPlayInterruptResponse{AsPlayed: false, Because: "board_doesntmatch", Msglog: "board mismatch"}, nil
		}
		if !currentPlayerIsAI(game, m.sharedAI) {
			return arbiterPlayInterruptResponse{AsPlayed: false, Because: "board_doesntmatch", Msglog: "missing opponent move"}, nil
		}
	} else if !currentPlayerIsAI(game, m.sharedAI) {
		return nil, newArbiterHTTPError(http.StatusBadRequest, "missing move")
	}

	if terminal := arbiterGameStatusFromState(game.state.Status); terminal != arbiterStatusPlaying {
		return arbiterPlayInterruptResponse{AsPlayed: false, Because: string(terminal), Msglog: "game already ended", WinReason: arbiterWinReasonFromGame(game)}, nil
	}

	player := game.currentPlayer()
	ai, ok := player.(*AIPlayer)
	if !ok {
		return arbiterPlayInterruptResponse{AsPlayed: false, Because: "self_error", Msglog: "current player is not an AI"}, nil
	}

	move := ai.ChooseMove(game.State(), game.rules)
	if !move.IsValid(boardSize) {
		return arbiterPlayInterruptResponse{AsPlayed: false, Because: "self_error", Msglog: "AI returned an invalid move"}, nil
	}

	applied, reason := game.TryApplyMove(move)
	if !applied {
		return arbiterPlayInterruptResponse{AsPlayed: false, Because: "self_error", Msglog: reason}, nil
	}

	return arbiterPlaySuccessResponse{
		AsPlayed:  true,
		Move:      move.Y*boardSize + move.X,
		Board:     boardToFlatSlice(game.state.Board),
		Turn:      game.history.Size(),
		Gstatus:   string(arbiterGameStatusFromState(game.state.Status)),
		WinReason: arbiterWinReasonFromGame(game),
	}, nil
}

func (m *arbiterMiddleware) stop(req *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.session.game != nil {
		m.session.game = nil
	}
	if m.session.previousConfig != nil {
		configStore.Update(*m.session.previousConfig)
		m.session.previousConfig = nil
	}
	m.session.active = false
	m.session.sessionID = ""
}

func currentPlayerIsAI(game *Game, sharedAI *AIPlayer) bool {
	player := game.currentPlayer()
	_, ok := player.(*AIPlayer)
	return ok && player == sharedAI
}

func newArbiterGame(settings GameSettings, color PlayerColor, sharedAI *AIPlayer) *Game {
	settings.BoardSize = normalizeBoardSize(settings.BoardSize)
	if settings.WinLength <= 0 {
		settings.WinLength = 5
	}
	if settings.CaptureWinStones <= 0 {
		settings.CaptureWinStones = 10
	}
	settings.BlueStarts = true

	game := &Game{
		settings: settings,
		rules:    NewRules(settings),
		state:    DefaultGameState(settings),
		history:  MoveHistory{},
	}

	if color == PlayerBlue {
		game.bluePlayer = sharedAI
		game.redPlayer = NewHumanPlayer()
	} else {
		game.bluePlayer = NewHumanPlayer()
		game.redPlayer = sharedAI
	}

	game.Start()
	return game
}

func arbiterSettingsFromRequest(req *http.Request, color PlayerColor) (GameSettings, error) {
	settings := DefaultGameSettings()
	settings.BlueStarts = true

	if value, ok := queryInt(req, "board_size"); ok {
		if value < 3 {
			return GameSettings{}, fmt.Errorf("invalid board_size")
		}
		settings.BoardSize = value
	}
	if value, ok := queryInt(req, "win_length"); ok {
		if value < 3 {
			return GameSettings{}, fmt.Errorf("invalid win_length")
		}
		settings.WinLength = value
	}
	if value, ok := queryInt(req, "capture_win_stones"); ok {
		if value < 0 {
			return GameSettings{}, fmt.Errorf("invalid capture_win_stones")
		}
		settings.CaptureWinStones = value
	}
	if value, ok := queryBool(req, "forbid_double_three_blue"); ok {
		settings.ForbidDoubleThreeBlue = value
	}
	if value, ok := queryBool(req, "forbid_double_three_red"); ok {
		settings.ForbidDoubleThreeRed = value
	}

	if color == PlayerBlue {
		settings.BlueType = PlayerAI
		settings.RedType = PlayerHuman
	} else {
		settings.BlueType = PlayerHuman
		settings.RedType = PlayerAI
	}

	return settings, nil
}

func parseArbiterColor(raw string) (PlayerColor, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "black":
		return PlayerBlue, nil
	case "white":
		return PlayerRed, nil
	default:
		return PlayerBlue, fmt.Errorf("invalid you_are value")
	}
}

func parseArbiterGameStatus(raw string) (arbiterGameStatus, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(arbiterStatusPlaying):
		return arbiterStatusPlaying, nil
	case string(arbiterStatusBlackWin):
		return arbiterStatusBlackWin, nil
	case string(arbiterStatusWhiteWin):
		return arbiterStatusWhiteWin, nil
	case string(arbiterStatusDraw):
		return arbiterStatusDraw, nil
	default:
		return "", fmt.Errorf("invalid gstatus")
	}
}

func arbiterGameStatusFromState(status GameStatus) arbiterGameStatus {
	switch status {
	case StatusBlueWon:
		return arbiterStatusBlackWin
	case StatusRedWon:
		return arbiterStatusWhiteWin
	case StatusDraw:
		return arbiterStatusDraw
	default:
		return arbiterStatusPlaying
	}
}

func parseArbiterCellMove(raw string, boardSize int) (Move, error) {
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return Move{}, fmt.Errorf("invalid move")
	}
	if value < 0 || value >= boardSize*boardSize {
		return Move{}, fmt.Errorf("move out of bounds")
	}
	return Move{X: value % boardSize, Y: value / boardSize}, nil
}

func parseArbiterBoard(raw string, boardSize int) (Board, error) {
	values := strings.Split(strings.TrimSpace(raw), ",")
	if len(values) != boardSize*boardSize {
		return Board{}, fmt.Errorf("invalid board size")
	}
	board := NewBoard(boardSize)
	for i, rawCell := range values {
		value, err := strconv.Atoi(strings.TrimSpace(rawCell))
		if err != nil {
			return Board{}, fmt.Errorf("invalid board cell")
		}
		switch value {
		case 0, 1, 2:
			board.cells[i] = intToCell(value)
		default:
			return Board{}, fmt.Errorf("invalid board cell")
		}
	}
	return board, nil
}

func boardsEqual(left, right Board) bool {
	if left.size != right.size {
		return false
	}
	if len(left.cells) != len(right.cells) {
		return false
	}
	for i := range left.cells {
		if left.cells[i] != right.cells[i] {
			return false
		}
	}
	return true
}

func boardToFlatSlice(board Board) []int {
	size := board.Size()
	result := make([]int, 0, size*size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			result = append(result, cellToInt(board.At(x, y)))
		}
	}
	return result
}

func queryValue(req *http.Request, keys ...string) (string, bool) {
	values := req.URL.Query()
	for _, key := range keys {
		if raw, ok := values[key]; ok && len(raw) > 0 {
			return raw[0], true
		}
	}
	return "", false
}

func hasQueryKey(req *http.Request, key string) bool {
	_, ok := req.URL.Query()[key]
	return ok
}

func queryInt(req *http.Request, keys ...string) (int, bool) {
	raw, ok := queryValue(req, keys...)
	if !ok {
		return 0, false
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, false
	}
	return value, true
}

func queryBool(req *http.Request, key string) (bool, bool) {
	raw, ok := queryValue(req, key)
	if !ok {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func normalizeBoardSize(size int) int {
	if size < 3 {
		return 19
	}
	return size
}

func arbiterWinReasonFromGame(game *Game) string {
	if game == nil {
		return ""
	}
	switch game.state.Status {
	case StatusBlueWon, StatusRedWon:
		if len(game.state.WinningLine) > 0 {
			return "alignment"
		}
		return "capture"
	case StatusDraw:
		return "draw"
	default:
		return ""
	}
}
