package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

type trainer struct {
	client       *http.Client
	baseURL      string
	pollInterval time.Duration
	logger       *log.Logger
	totalBoards  int
	mode         string
	apiAddr      string
	rng          *rand.Rand

	matchesPerRound    int
	mutationStrength   float64
	heuristicTimeout   time.Duration
	aiTimeBudgetMs     int
	populationSize     int
	eliteCount         int
	trainingOpenings   int
	validationOpenings int
	openingPlies       int
	eloK               float64
	validationPassRate float64
	originalConfig     map[string]any
	configOverridden   bool

	statusMu  sync.RWMutex
	status    trainerStatus
	jobMu     sync.Mutex
	jobCancel context.CancelFunc
	jobDone   chan struct{}
}

type statusResponse struct {
	Status    string            `json:"status"`
	Winner    int               `json:"winner"`
	History   []json.RawMessage `json:"history"`
	BoardSize int               `json:"board_size"`
	Config    map[string]any    `json:"config"`
}

type queueResponse struct {
	TotalInQueue int `json:"total_in_queue"`
}

type ttCacheStatusResponse struct {
	Count    int     `json:"count"`
	Capacity int     `json:"capacity"`
	Usage    float64 `json:"usage"`
	Full     bool    `json:"full"`
}

type trainerStatus struct {
	Running             bool    `json:"running"`
	Mode                string  `json:"mode"`
	Phase               string  `json:"phase"`
	Message             string  `json:"message"`
	StartedAt           string  `json:"started_at"`
	UpdatedAt           string  `json:"updated_at"`
	GamesPlayed         int     `json:"games_played"`
	Generation          int     `json:"generation"`
	PopulationSize      int     `json:"population_size"`
	HistoricalCount     int     `json:"historical_count"`
	LastValidationRate  float64 `json:"last_validation_rate"`
	ValidationThreshold float64 `json:"validation_threshold"`
	TrainingOpenings    int     `json:"training_openings"`
	GenerationStartedAt string  `json:"generation_started_at"`
	RoundMatchesTotal   int     `json:"round_matches_total"`
	EtaSeconds          int     `json:"eta_seconds"`

	CurrentMatch        *trainerMatch     `json:"current_match,omitempty"`
	TopContenders       []trainerStanding `json:"top_contenders,omitempty"`
	ChampionHeuristic   heuristicConfig   `json:"champion_heuristic"`
	ChallengerHeuristic heuristicConfig   `json:"challenger_heuristic"`
	ChallengerDetails   []trainerDetail   `json:"challenger_details,omitempty"`
}

type trainerMatch struct {
	BlackID      string `json:"black_id"`
	WhiteID      string `json:"white_id"`
	OpeningIndex int    `json:"opening_index"`
	Stage        string `json:"stage"`
}

type trainerStanding struct {
	ID  string  `json:"id"`
	Elo float64 `json:"elo"`
}

type trainerDetail struct {
	ID         string          `json:"id"`
	Elo        float64         `json:"elo"`
	Heuristics heuristicConfig `json:"heuristics"`
}

type heuristicConfig struct {
	Open4               float64 `json:"open_4"`
	Closed4             float64 `json:"closed_4"`
	Broken4             float64 `json:"broken_4"`
	Open3               float64 `json:"open_3"`
	Broken3             float64 `json:"broken_3"`
	Closed3             float64 `json:"closed_3"`
	Open2               float64 `json:"open_2"`
	Broken2             float64 `json:"broken_2"`
	ForkOpen3           float64 `json:"fork_open_3"`
	ForkFourPlus        float64 `json:"fork_four_plus"`
	CaptureNow          float64 `json:"capture_now"`
	CaptureDoubleThreat float64 `json:"capture_double_threat"`
	CaptureNearWin      float64 `json:"capture_near_win"`
	CaptureInTwo        float64 `json:"capture_in_two"`
	HangingPair         float64 `json:"hanging_pair"`
	CaptureWinSoonScale float64 `json:"capture_win_soon_scale"`
	CaptureInTwoLimit   int     `json:"capture_in_two_limit"`
}

type heuristicsResponse struct {
	Heuristics heuristicConfig `json:"heuristics"`
}

type openingMove struct {
	X int
	Y int
}

type contender struct {
	ID         string
	Heuristics heuristicConfig
	Elo        float64
}

func main() {
	logger, closeLog, err := buildLogger("/logs/AITrainer.log")
	if err != nil {
		log.Fatalf("failed to initialize logger: %v", err)
	}
	defer closeLog()

	baseURL := getenv("BACKEND_URL", "http://backend:8080")
	pollMs := getenvInt("POLL_INTERVAL_MS", 2000)
	mode := getenv("TRAINER_MODE", "cache")
	apiAddr := getenv("TRAINER_API_ADDR", ":8090")
	autostart := getenv("TRAINER_AUTOSTART_MODE", "")
	matchesPerRound := getenvInt("HEURISTIC_MATCHES_PER_ROUND", 50)
	if matchesPerRound < 2 {
		matchesPerRound = 2
	}
	if matchesPerRound%2 != 0 {
		matchesPerRound++
	}
	mutationStrength := getenvFloat("HEURISTIC_MUTATION_STRENGTH", 0.08)
	if mutationStrength <= 0 {
		mutationStrength = 0.08
	}
	heuristicTimeoutSec := getenvInt("HEURISTIC_GAME_TIMEOUT_SEC", 180)
	aiTimeBudgetMs := getenvInt("TRAINER_AI_TIME_BUDGET_MS", 800)
	populationSize := getenvInt("HEURISTIC_POPULATION_SIZE", 8)
	if populationSize < 4 {
		populationSize = 4
	}
	eliteCount := getenvInt("HEURISTIC_ELITE_COUNT", 2)
	if eliteCount < 1 {
		eliteCount = 1
	}
	if eliteCount >= populationSize {
		eliteCount = populationSize - 1
	}
	trainingOpenings := getenvInt("HEURISTIC_TRAINING_OPENINGS", 6)
	if trainingOpenings < 1 {
		trainingOpenings = 1
	}
	validationOpenings := getenvInt("HEURISTIC_VALIDATION_OPENINGS", 4)
	if validationOpenings < 1 {
		validationOpenings = 1
	}
	openingPlies := getenvInt("HEURISTIC_OPENING_PLIES", 4)
	if openingPlies < 1 {
		openingPlies = 1
	}
	eloK := getenvFloat("HEURISTIC_ELO_K", 20)
	if eloK <= 0 {
		eloK = 20
	}
	validationPassRate := getenvFloat("HEURISTIC_VALIDATION_PASS_RATE", 0.52)
	if validationPassRate <= 0 || validationPassRate > 1 {
		validationPassRate = 0.52
	}
	t := &trainer{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		baseURL:            baseURL,
		pollInterval:       time.Duration(pollMs) * time.Millisecond,
		logger:             logger,
		mode:               mode,
		apiAddr:            apiAddr,
		rng:                rand.New(rand.NewSource(time.Now().UnixNano())),
		matchesPerRound:    matchesPerRound,
		mutationStrength:   mutationStrength,
		heuristicTimeout:   time.Duration(heuristicTimeoutSec) * time.Second,
		aiTimeBudgetMs:     aiTimeBudgetMs,
		populationSize:     populationSize,
		eliteCount:         eliteCount,
		trainingOpenings:   trainingOpenings,
		validationOpenings: validationOpenings,
		openingPlies:       openingPlies,
		eloK:               eloK,
		validationPassRate: validationPassRate,
		status: trainerStatus{
			Running:   false,
			Mode:      mode,
			Phase:     "idle",
			Message:   "service ready",
			StartedAt: time.Now().UTC().Format(time.RFC3339),
			UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		},
	}

	t.logf("AI trainer service started. backend=%s mode=%s poll_interval=%s", t.baseURL, t.mode, t.pollInterval)
	t.startStatusAPI()

	if autostart != "" {
		startMode := autostart
		if startMode == "1" || startMode == "true" || startMode == "yes" {
			startMode = mode
		}
		if err := t.startTraining(startMode); err != nil {
			t.logf("Autostart failed: %v", err)
		}
	}

	sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	<-sigCtx.Done()
	_ = t.stopTraining("shutdown")
	t.logf("Trainer service stopping")
}

func (t *trainer) startStatusAPI() {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/trainer/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "running": t.getStatus().Running})
	})
	mux.HandleFunc("/api/trainer/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, t.getStatus())
	})
	mux.HandleFunc("/api/trainer/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var payload struct {
			Mode string `json:"mode"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mode := payload.Mode
		if mode == "" {
			mode = t.mode
		}
		if err := t.startTraining(mode); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, t.getStatus())
	})
	mux.HandleFunc("/api/trainer/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if err := t.stopTraining("requested via api"); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, t.getStatus())
	})
	server := &http.Server{Addr: t.apiAddr, Handler: mux}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			t.logf("trainer api server error: %v", err)
		}
	}()
}

func (t *trainer) getStatus() trainerStatus {
	t.statusMu.RLock()
	defer t.statusMu.RUnlock()
	return t.status
}

func (t *trainer) updateStatus(mutator func(*trainerStatus)) {
	t.statusMu.Lock()
	defer t.statusMu.Unlock()
	mutator(&t.status)
	t.status.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
}

func (t *trainer) startTraining(mode string) error {
	t.jobMu.Lock()
	defer t.jobMu.Unlock()
	if t.jobCancel != nil {
		return fmt.Errorf("training already running")
	}
	switch mode {
	case "", "heuristic", "cache":
		if mode == "" {
			mode = t.mode
		}
	default:
		return fmt.Errorf("unknown mode %q", mode)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.jobCancel = cancel
	t.jobDone = done
	t.updateStatus(func(s *trainerStatus) {
		s.Running = true
		s.Mode = mode
		s.Phase = "starting"
		s.Message = "training starting"
		s.GamesPlayed = 0
	})
	go func() {
		defer close(done)
		if err := t.waitBackendReady(ctx); err != nil {
			t.updateStatus(func(s *trainerStatus) {
				s.Phase = "error"
				s.Message = err.Error()
			})
		} else {
			if err := t.runMode(ctx, mode); err != nil && err != context.Canceled {
				t.updateStatus(func(s *trainerStatus) {
					s.Phase = "error"
					s.Message = err.Error()
				})
			}
		}
		t.updateStatus(func(s *trainerStatus) {
			s.Running = false
			if s.Phase != "error" {
				s.Phase = "idle"
				s.Message = "service ready"
			}
		})
		t.jobMu.Lock()
		t.jobCancel = nil
		t.jobDone = nil
		t.jobMu.Unlock()
	}()
	return nil
}

func (t *trainer) stopTraining(reason string) error {
	t.jobMu.Lock()
	cancel := t.jobCancel
	done := t.jobDone
	t.jobMu.Unlock()
	if cancel == nil {
		return fmt.Errorf("no running training job")
	}
	t.logf("Stopping training: %s", reason)
	cancel()
	if done != nil {
		<-done
	}
	t.updateStatus(func(s *trainerStatus) {
		s.Running = false
		s.Phase = "idle"
		s.Message = "service ready"
	})
	return nil
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func buildLogger(path string) (*log.Logger, func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, nil, err
	}
	logger := log.New(io.MultiWriter(os.Stdout, f), "", 0)
	return logger, func() { _ = f.Close() }, nil
}

func (t *trainer) runMode(ctx context.Context, mode string) error {
	if strings.EqualFold(mode, "heuristic") {
		return t.runHeuristicTraining(ctx)
	}
	return t.runCacheTraining(ctx)
}

func (t *trainer) runCacheTraining(ctx context.Context) error {
	t.updateStatus(func(s *trainerStatus) {
		s.Phase = "running"
		s.Message = "cache training running"
		s.PopulationSize = 0
		s.HistoricalCount = 0
		s.TopContenders = nil
		s.ChallengerDetails = nil
		s.ChampionHeuristic = heuristicConfig{}
		s.ChallengerHeuristic = heuristicConfig{}
		s.CurrentMatch = nil
	})
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		full, err := t.ttIsFull()
		if err != nil {
			return err
		}
		if full {
			t.logf("TT cache is full. Stopping trainer.")
			return nil
		}

		queueBefore, err := t.getQueueCount()
		if err != nil {
			return err
		}

		if err := t.startAIVsAIGame(nil, nil); err != nil {
			return err
		}
		t.updateStatus(func(s *trainerStatus) {
			s.GamesPlayed++
		})
		t.logf("Started a game")
		t.logf("Waiting the game to finish...")

		for {
			full, err := t.ttIsFull()
			if err != nil {
				return err
			}
			if full {
				t.logf("TT cache is full during game. Stopping trainer.")
				_ = t.stopGame()
				return nil
			}

			running, err := t.gameRunning()
			if err != nil {
				return err
			}
			if !running {
				break
			}
			if !sleepWithContext(ctx, t.pollInterval) {
				return ctx.Err()
			}
		}

		t.logf("Game is over.")
		t.logf("Waiting the analyze queue to be empty...")

		queueAfterGame, err := t.getQueueCount()
		if err != nil {
			return err
		}
		newBoards := queueAfterGame - queueBefore
		if newBoards < 0 {
			newBoards = 0
		}
		t.totalBoards += newBoards

		lastLogged := -1
		for {
			full, err := t.ttIsFull()
			if err != nil {
				return err
			}
			if full {
				t.logf("TT cache is full while queue is draining. Stopping trainer.")
				return nil
			}

			count, err := t.getQueueCount()
			if err != nil {
				return err
			}
			if count == 0 {
				t.logf("Queue is empty.")
				break
			}
			if count != lastLogged {
				t.logf("%d boards still in queue..", count)
				lastLogged = count
			}
			if !sleepWithContext(ctx, t.pollInterval) {
				return ctx.Err()
			}
		}

		t.logf("Boards sent to analyze this game: %d (total: %d)", newBoards, t.totalBoards)
		if newBoards == 0 {
			t.logf("No new boards were generated by the last game. Stopping trainer to avoid spam.")
			return nil
		}
	}
}

func (t *trainer) runHeuristicTraining(ctx context.Context) error {
	if err := t.applyHeuristicConfigOverride(); err != nil {
		return err
	}
	defer func() {
		if err := t.restoreHeuristicConfigOverride(); err != nil {
			t.logf("failed to restore backend config: %v", err)
		}
	}()

	base, err := t.getBaseHeuristics()
	if err != nil {
		return err
	}
	boardSize := 19
	if st, err := t.fetchStatus(); err == nil && st.BoardSize > 0 {
		boardSize = st.BoardSize
	}
	trainOpenings := t.buildOpeningSuite(boardSize, t.trainingOpenings, 41)
	valOpenings := t.buildOpeningSuite(boardSize, t.validationOpenings, 911)
	champion := contender{ID: "champion", Heuristics: base, Elo: 1500}
	population := t.initializePopulation(champion.Heuristics)
	_ = t.persistHeuristicPair(champion.Heuristics, population[1].Heuristics)

	t.updateStatus(func(s *trainerStatus) {
		s.Phase = "running"
		s.Message = "heuristic training running"
		s.Generation = 0
		s.GamesPlayed = 0
		s.PopulationSize = t.populationSize
		s.HistoricalCount = 0
		s.ValidationThreshold = t.validationPassRate
		s.TrainingOpenings = t.trainingOpenings
		s.GenerationStartedAt = time.Now().UTC().Format(time.RFC3339)
		s.RoundMatchesTotal = 0
		s.EtaSeconds = 0
		s.ChampionHeuristic = champion.Heuristics
		s.ChallengerHeuristic = population[1].Heuristics
		s.TopContenders = toStandings(population, 8)
		s.ChallengerDetails = toChallengerDetails(population, champion.Heuristics, 8)
	})

	generation := 1
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		roundTotal := (len(population) * (len(population) - 1) / 2) * len(trainOpenings)
		roundStart := time.Now().UTC()
		t.updateStatus(func(s *trainerStatus) {
			s.Generation = generation
			s.GamesPlayed = 0
			s.GenerationStartedAt = roundStart.Format(time.RFC3339)
			s.RoundMatchesTotal = roundTotal
			s.EtaSeconds = 0
		})
		gamesPlayed, err := t.runPopulationRound(ctx, population, trainOpenings, generation, roundStart, roundTotal)
		if err != nil {
			return err
		}
		sortContendersByElo(population)
		best := population[0]
		challenger := population[1]

		promoted := false
		if !heuristicsEqual(best.Heuristics, champion.Heuristics) {
			points, total, err := t.runValidation(ctx, best.Heuristics, champion.Heuristics, valOpenings)
			if err != nil {
				return err
			}
			rate := 0.0
			if total > 0 {
				rate = points / total
			}
			t.updateStatus(func(s *trainerStatus) {
				s.LastValidationRate = rate
			})
			if rate >= t.validationPassRate {
				champion = contender{ID: fmt.Sprintf("champion-g%d", generation), Heuristics: best.Heuristics, Elo: 1500}
				promoted = true
			}
		}
		if promoted {
			t.logf("Gen %d champion promoted", generation)
		} else {
			t.logf("Gen %d champion retained", generation)
		}

		_ = t.persistHeuristicPair(champion.Heuristics, challenger.Heuristics)
		t.updateStatus(func(s *trainerStatus) {
			s.Generation = generation
			s.GamesPlayed = gamesPlayed
			s.CurrentMatch = nil
			s.EtaSeconds = 0
			s.ChampionHeuristic = champion.Heuristics
			s.ChallengerHeuristic = challenger.Heuristics
			s.TopContenders = toStandings(population, 8)
			s.ChallengerDetails = toChallengerDetails(population, champion.Heuristics, 8)
		})
		population = t.nextGenerationPopulation(champion.Heuristics, population)
		generation++
	}
}

func (t *trainer) runPopulationRound(ctx context.Context, population []contender, openings [][]openingMove, generation int, roundStart time.Time, roundTotal int) (int, error) {
	games := 0
	for i := 0; i < len(population); i++ {
		for j := i + 1; j < len(population); j++ {
			for openingIdx, opening := range openings {
				if ctx.Err() != nil {
					return games, ctx.Err()
				}
				t.updateStatus(func(s *trainerStatus) {
					s.CurrentMatch = &trainerMatch{
						BlackID:      population[i].ID,
						WhiteID:      population[j].ID,
						OpeningIndex: openingIdx,
						Stage:        "population",
					}
					s.GamesPlayed = games
				})
				result, stones, err := t.playHeadToHead(ctx, population[i].Heuristics, population[j].Heuristics, opening)
				if err != nil {
					return games, err
				}
				updateElo(&population[i], &population[j], result, t.eloK)
				games++
				ranked := make([]contender, len(population))
				copy(ranked, population)
				sortContendersByElo(ranked)
				t.updateStatus(func(s *trainerStatus) {
					s.GamesPlayed = games
					s.TopContenders = toStandings(ranked, 8)
					s.ChallengerDetails = toChallengerDetails(ranked, s.ChampionHeuristic, 8)
					if len(ranked) > 0 {
						s.ChampionHeuristic = ranked[0].Heuristics
					}
					if len(ranked) > 1 {
						s.ChallengerHeuristic = ranked[1].Heuristics
					}
					if roundTotal > 0 && games > 0 {
						elapsedSec := time.Since(roundStart).Seconds()
						avgSec := elapsedSec / float64(games)
						remaining := roundTotal - games
						if remaining < 0 {
							remaining = 0
						}
						s.EtaSeconds = int(math.Round(avgSec * float64(remaining)))
					} else {
						s.EtaSeconds = 0
					}
				})
				if games%5 == 0 || games == 1 {
					t.logf("Gen %d game %d pop(%s vs %s) result=%.1f stones=%d", generation, games, population[i].ID, population[j].ID, result, stones)
				}
			}
		}
	}
	return games, nil
}

func (t *trainer) runValidation(ctx context.Context, candidate heuristicConfig, champion heuristicConfig, openings [][]openingMove) (float64, float64, error) {
	points := 0.0
	total := 0.0
	for _, opening := range openings {
		if ctx.Err() != nil {
			return points, total, ctx.Err()
		}
		result, _, err := t.playHeadToHead(ctx, candidate, champion, opening)
		if err != nil {
			return points, total, err
		}
		points += result
		total += 1.0
	}
	return points, total, nil
}

func (t *trainer) playHeadToHead(ctx context.Context, first, second heuristicConfig, opening []openingMove) (float64, int, error) {
	points := 0.0
	stones := 0
	for _, firstBlack := range []bool{true, false} {
		var black, white heuristicConfig
		if firstBlack {
			black = first
			white = second
		} else {
			black = second
			white = first
		}
		status, matchStones, err := t.playConfiguredGame(ctx, black, white, opening)
		if err != nil {
			return 0, 0, err
		}
		stones += matchStones
		switch status.Winner {
		case 1:
			if firstBlack {
				points += 1.0
			}
		case 2:
			if !firstBlack {
				points += 1.0
			}
		default:
			points += 0.5
		}
	}
	return points / 2.0, stones / 2, nil
}

func (t *trainer) playConfiguredGame(ctx context.Context, black heuristicConfig, white heuristicConfig, opening []openingMove) (statusResponse, int, error) {
	if err := t.startSeededGame(opening, &black, &white); err != nil {
		return statusResponse{}, 0, err
	}
	deadline := time.Now().Add(t.heuristicTimeout)
	for {
		if ctx.Err() != nil {
			return statusResponse{}, 0, ctx.Err()
		}
		status, err := t.fetchStatus()
		if err != nil {
			return statusResponse{}, 0, err
		}
		if status.Status != "running" {
			return status, len(status.History), nil
		}
		if t.heuristicTimeout > 0 && time.Now().After(deadline) {
			_ = t.stopGame()
			return statusResponse{}, 0, fmt.Errorf("heuristic game timeout after %s", t.heuristicTimeout)
		}
		if !sleepWithContext(ctx, t.pollInterval) {
			return statusResponse{}, 0, ctx.Err()
		}
	}
}

func (t *trainer) startSeededGame(opening []openingMove, black *heuristicConfig, white *heuristicConfig) error {
	if err := t.postJSON("/api/start", map[string]any{
		"settings": map[string]any{
			"mode":         "human_vs_human",
			"human_player": 1,
		},
	}, nil); err != nil {
		return err
	}
	for _, move := range opening {
		if err := t.postJSON("/api/move", map[string]any{
			"x": move.X,
			"y": move.Y,
		}, nil); err != nil {
			return err
		}
	}
	return t.postJSON("/api/settings", map[string]any{
		"settings": map[string]any{
			"mode":             "ai_vs_ai",
			"human_player":     1,
			"black_heuristics": black,
			"white_heuristics": white,
		},
	}, nil)
}

func (t *trainer) fetchStatus() (statusResponse, error) {
	var status statusResponse
	if err := t.getJSON("/api/status", &status); err != nil {
		return statusResponse{}, err
	}
	return status, nil
}

func (t *trainer) buildOpeningSuite(boardSize, count int, salt int64) [][]openingMove {
	rng := rand.New(rand.NewSource(int64(boardSize*97+t.openingPlies*13) + salt))
	center := boardSize / 2
	offsets := []openingMove{
		{0, 0}, {1, 0}, {0, 1}, {-1, 0}, {0, -1}, {1, 1}, {-1, -1}, {1, -1}, {-1, 1}, {2, 0}, {0, 2},
	}
	suite := make([][]openingMove, 0, count)
	for i := 0; i < count; i++ {
		used := map[[2]int]bool{}
		opening := make([]openingMove, 0, t.openingPlies)
		for len(opening) < t.openingPlies {
			off := offsets[rng.Intn(len(offsets))]
			x := center + off.X
			y := center + off.Y
			if x < 0 || y < 0 || x >= boardSize || y >= boardSize {
				continue
			}
			key := [2]int{x, y}
			if used[key] {
				continue
			}
			used[key] = true
			opening = append(opening, openingMove{X: x, Y: y})
		}
		suite = append(suite, opening)
	}
	return suite
}

func (t *trainer) initializePopulation(seed heuristicConfig) []contender {
	pop := make([]contender, 0, t.populationSize)
	pop = append(pop, contender{ID: "p0", Heuristics: seed, Elo: 1500})
	for i := 1; i < t.populationSize; i++ {
		pop = append(pop, contender{
			ID:         fmt.Sprintf("p%d", i),
			Heuristics: t.mutateHeuristics(seed),
			Elo:        1500,
		})
	}
	return pop
}

func (t *trainer) nextGenerationPopulation(champion heuristicConfig, ranked []contender) []contender {
	next := make([]contender, 0, t.populationSize)
	next = append(next, contender{ID: "p0", Heuristics: champion, Elo: 1500})
	for i := 0; i < len(ranked) && len(next) < t.populationSize && i < t.eliteCount+1; i++ {
		if heuristicsEqual(ranked[i].Heuristics, champion) {
			continue
		}
		next = append(next, contender{
			ID:         fmt.Sprintf("elite-%d", i),
			Heuristics: ranked[i].Heuristics,
			Elo:        1500,
		})
	}
	parentPool := ranked
	if len(parentPool) > t.eliteCount+1 {
		parentPool = parentPool[:t.eliteCount+1]
	}
	for len(next) < t.populationSize {
		parent := parentPool[t.rng.Intn(len(parentPool))]
		next = append(next, contender{
			ID:         fmt.Sprintf("mut-%d", len(next)),
			Heuristics: t.mutateHeuristics(parent.Heuristics),
			Elo:        1500,
		})
	}
	return next
}

func toStandings(list []contender, limit int) []trainerStanding {
	out := make([]trainerStanding, 0, minInt(len(list), limit))
	for i := 0; i < len(list) && i < limit; i++ {
		out = append(out, trainerStanding{ID: list[i].ID, Elo: list[i].Elo})
	}
	return out
}

func toChallengerDetails(list []contender, champion heuristicConfig, limit int) []trainerDetail {
	out := make([]trainerDetail, 0, minInt(len(list), limit))
	for i := 0; i < len(list) && len(out) < limit; i++ {
		if heuristicsEqual(list[i].Heuristics, champion) {
			continue
		}
		out = append(out, trainerDetail{ID: list[i].ID, Elo: list[i].Elo, Heuristics: list[i].Heuristics})
	}
	return out
}

func sortContendersByElo(list []contender) {
	for i := 0; i < len(list); i++ {
		for j := i + 1; j < len(list); j++ {
			if list[j].Elo > list[i].Elo {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
}

func updateElo(a *contender, b *contender, resultForA float64, k float64) {
	expA := 1.0 / (1.0 + math.Pow(10, (b.Elo-a.Elo)/400.0))
	expB := 1.0 / (1.0 + math.Pow(10, (a.Elo-b.Elo)/400.0))
	a.Elo += k * (resultForA - expA)
	b.Elo += k * ((1.0 - resultForA) - expB)
}

func heuristicsEqual(a, b heuristicConfig) bool {
	return a == b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (t *trainer) getBaseHeuristics() (heuristicConfig, error) {
	var payload heuristicsResponse
	if err := t.getJSON("/api/heuristics", &payload); err == nil {
		return payload.Heuristics, nil
	}
	if fromLogs, err := t.readHeuristicFile("current_best_heuristic.json"); err == nil {
		return fromLogs, nil
	}
	if fromLogs, err := t.readHeuristicFile("champion_heuristics.json"); err == nil {
		return fromLogs, nil
	}
	return defaultHeuristics(), nil
}

func (t *trainer) mutateHeuristics(base heuristicConfig) heuristicConfig {
	out := base
	mutate := func(v float64) float64 {
		factor := 1 + (t.rng.Float64()*2-1)*t.mutationStrength
		next := v * factor
		if math.IsNaN(next) || math.IsInf(next, 0) || next < 1 {
			return v
		}
		return next
	}
	out.Open4 = mutate(out.Open4)
	out.Closed4 = mutate(out.Closed4)
	out.Broken4 = mutate(out.Broken4)
	out.Open3 = mutate(out.Open3)
	out.Broken3 = mutate(out.Broken3)
	out.Closed3 = mutate(out.Closed3)
	out.Open2 = mutate(out.Open2)
	out.Broken2 = mutate(out.Broken2)
	out.ForkOpen3 = mutate(out.ForkOpen3)
	out.ForkFourPlus = mutate(out.ForkFourPlus)
	out.CaptureNow = mutate(out.CaptureNow)
	out.CaptureDoubleThreat = mutate(out.CaptureDoubleThreat)
	out.CaptureNearWin = mutate(out.CaptureNearWin)
	out.CaptureInTwo = mutate(out.CaptureInTwo)
	out.HangingPair = mutate(out.HangingPair)
	out.CaptureWinSoonScale = mutate(out.CaptureWinSoonScale)
	if out.CaptureInTwoLimit <= 0 {
		out.CaptureInTwoLimit = base.CaptureInTwoLimit
	}
	return out
}

func (t *trainer) persistHeuristicPair(champion, challenger heuristicConfig) error {
	if err := t.writeHeuristicFile("champion_heuristics.json", champion); err != nil {
		return err
	}
	if err := t.writeHeuristicFile("challenger_heuristics.json", challenger); err != nil {
		return err
	}
	if err := t.writeHeuristicFile("current_best_heuristic.json", champion); err != nil {
		return err
	}
	return nil
}

func (t *trainer) writeHeuristicFile(name string, heuristics heuristicConfig) error {
	if err := os.MkdirAll("/logs", 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(heuristics, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	path := filepath.Join("/logs", name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (t *trainer) readHeuristicFile(name string) (heuristicConfig, error) {
	path := filepath.Join("/logs", name)
	raw, err := os.ReadFile(path)
	if err != nil {
		return heuristicConfig{}, err
	}
	var cfg heuristicConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return heuristicConfig{}, err
	}
	return cfg, nil
}

func defaultHeuristics() heuristicConfig {
	return heuristicConfig{
		Open4:               120000,
		Closed4:             24000,
		Broken4:             16000,
		Open3:               18000,
		Broken3:             11000,
		Closed3:             800,
		Open2:               400,
		Broken2:             220,
		ForkOpen3:           40000,
		ForkFourPlus:        130000,
		CaptureNow:          2200,
		CaptureDoubleThreat: 2600,
		CaptureNearWin:      12000,
		CaptureInTwo:        700,
		HangingPair:         2400,
		CaptureWinSoonScale: 0.95,
		CaptureInTwoLimit:   8,
	}
}

func (t *trainer) applyHeuristicConfigOverride() error {
	var status statusResponse
	if err := t.getJSON("/api/status", &status); err != nil {
		return err
	}
	cfg := status.Config
	if cfg == nil {
		return nil
	}
	cfg["ai_use_tt_cache"] = false
	cfg["ai_time_budget_ms"] = t.aiTimeBudgetMs
	return t.postJSON("/api/settings", map[string]any{"config": cfg}, nil)
}

func (t *trainer) restoreHeuristicConfigOverride() error {
	return nil
}

func (t *trainer) waitBackendReady(ctx context.Context) error {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := t.ping(); err == nil {
			return nil
		}
		if !sleepWithContext(ctx, 1*time.Second) {
			return ctx.Err()
		}
	}
	return fmt.Errorf("timeout after 60s")
}

func (t *trainer) ping() error {
	req, err := http.NewRequest(http.MethodGet, t.baseURL+"/api/ping", nil)
	if err != nil {
		return err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ping status %d", resp.StatusCode)
	}
	return nil
}

func (t *trainer) startAIVsAIGame(blackHeuristics *heuristicConfig, whiteHeuristics *heuristicConfig) error {
	payload := map[string]any{
		"settings": map[string]any{
			"mode":             "ai_vs_ai",
			"human_player":     1,
			"black_heuristics": blackHeuristics,
			"white_heuristics": whiteHeuristics,
		},
	}
	return t.postJSON("/api/start", payload, nil)
}

func (t *trainer) stopGame() error {
	return t.postJSON("/api/stop", map[string]any{}, nil)
}

func (t *trainer) gameRunning() (bool, error) {
	var status statusResponse
	if err := t.getJSON("/api/status", &status); err != nil {
		return false, err
	}
	return status.Status == "running", nil
}

func (t *trainer) getQueueCount() (int, error) {
	var queue queueResponse
	if err := t.getJSON("/api/analitics/queue", &queue); err != nil {
		return 0, err
	}
	return queue.TotalInQueue, nil
}

func (t *trainer) ttIsFull() (bool, error) {
	var tt ttCacheStatusResponse
	if err := t.getJSON("/api/cache/tt", &tt); err != nil {
		return false, err
	}
	return tt.Full, nil
}

func (t *trainer) getJSON(path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, t.baseURL+path, nil)
	if err != nil {
		return err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("GET %s -> %d: %s", path, resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (t *trainer) postJSON(path string, payload any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, t.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("POST %s -> %d: %s", path, resp.StatusCode, string(respBody))
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (t *trainer) logf(format string, args ...any) {
	ts := time.Now().Format("2006-01-02 15:04:05")
	t.logger.Printf("[%s] %s", ts, fmt.Sprintf(format, args...))
}

func sleepWithContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func getenv(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func getenvInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func getenvFloat(key string, fallback float64) float64 {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	var parsed float64
	if _, err := fmt.Sscanf(value, "%f", &parsed); err != nil {
		return fallback
	}
	return parsed
}
