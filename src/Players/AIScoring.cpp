#include "AIScoring.hpp"

#include <algorithm>
#include <chrono>
#include <cstdint>
#include <iostream>
#include <limits>
#include <unordered_map>
#include <unordered_set>

#include "Config.hpp"

namespace {
constexpr double kIllegalScore = -1e9;
constexpr double kWinScore = 10000.0;

struct MinimaxContext {
	const Rules& rules;
	const AIScoreSettings& settings;
	std::chrono::steady_clock::time_point start;
};

double minimax(GameState state,
	const MinimaxContext& ctx,
	int depth,
	GameState::PlayerColor currentPlayer,
	int depthFromRoot,
	double alpha,
	double beta);
double evaluateMoveWithCache(const GameState& state,
	const MinimaxContext& ctx,
	GameState::PlayerColor currentPlayer,
	const Move& move,
	int depthLeft,
	int depthFromRoot,
	std::uint64_t boardHash,
	bool* outCached,
	double alpha,
	double beta);
double heuristicForMove(const GameState& state, const Rules& rules, const AIScoreSettings& settings, const Move& move);

struct CacheKey {
	std::uint64_t hash;
	int depth;
	int boardSize;
	GameState::PlayerColor player;
	bool operator==(const CacheKey& other) const {
		return hash == other.hash && depth == other.depth && boardSize == other.boardSize && player == other.player;
	}
};

struct CacheKeyHash {
	std::size_t operator()(const CacheKey& key) const {
		std::size_t h1 = static_cast<std::size_t>(key.hash);
		std::size_t h2 = static_cast<std::size_t>(key.depth);
		std::size_t h3 = static_cast<std::size_t>(key.boardSize);
		std::size_t h4 = static_cast<std::size_t>(key.player == GameState::PlayerColor::Black ? 1 : 2);
		return (((h1 * 1315423911u) ^ (h2 << 1)) ^ (h3 << 3)) ^ (h4 << 5);
	}
};

std::uint64_t hashBoard(const Board& board, int boardSize) {
	std::uint64_t hash = 1469598103934665603ull;
	for (int y = 0; y < boardSize; ++y) {
		for (int x = 0; x < boardSize; ++x) {
			std::uint64_t v = static_cast<std::uint64_t>(board.at(x, y));
			hash ^= (v + 1u);
			hash *= 1099511628211ull;
		}
	}
	return hash;
}

std::unordered_map<CacheKey, std::vector<double>, CacheKeyHash> g_depthCache;
AISearchCache g_defaultCache;

using TTKey = AISearchCache::TTKey;
using TTEntry = AISearchCache::TTEntry;
using MoveCacheKey = AISearchCache::MoveCacheKey;
using ImmediateWinKey = AISearchCache::ImmediateWinKey;
using ImmediateWinStateKey = AISearchCache::ImmediateWinStateKey;

AISearchCache& selectCache(const MinimaxContext& ctx) {
	return ctx.settings.cache ? *ctx.settings.cache : g_defaultCache;
}

AISearchCache::StateKey makeStateKey(const GameState& state, int boardSize, GameState::PlayerColor player) {
	return AISearchCache::StateKey{
		hashBoard(state.board, boardSize),
		boardSize,
		state.capturedStonesBlack,
		state.capturedStonesWhite,
		state.status,
		player
	};
}

void storeTtEntry(AISearchCache& cache, const AISearchCache::TTKey& key, const AISearchCache::TTEntry& entry) {
	auto insertResult = cache.tt.emplace(key, entry);
	if (!insertResult.second && insertResult.first->second.depthLeft < entry.depthLeft) {
		insertResult.first->second = entry;
		return;
	}
	if (insertResult.second) {
		++cache.ttSize;
		if (cache.ttSize > Config::kAiTtMaxEntries) {
			cache.tt.clear();
			cache.ttSize = 0;
		}
	}
}

void addEdge(AISearchCache& cache,
	const AISearchCache::StateKey& parent,
	const AISearchCache::StateKey& child) {
	auto& children = cache.edges[parent];
	for (const auto& existing : children) {
		if (existing == child) {
			return;
		}
	}
	children.push_back(child);
}
Board::Cell playerCell(GameState::PlayerColor player) {
	return (player == GameState::PlayerColor::Black) ? Board::Cell::Black : Board::Cell::White;
}

GameState::PlayerColor otherPlayer(GameState::PlayerColor player) {
	return (player == GameState::PlayerColor::Black) ? GameState::PlayerColor::White : GameState::PlayerColor::Black;
}

int countDirection(const Board& board, int x, int y, int dx, int dy, Board::Cell cell, int limit) {
	int count = 0;
	for (int step = 1; step <= limit; ++step) {
		int nx = x + step * dx;
		int ny = y + step * dy;
		if (!board.inBounds(nx, ny) || board.at(nx, ny) != cell) {
			break;
		}
		++count;
	}
	return count;
}

std::vector<Move> collectCandidateMoves(const Board& board, int boardSize) {
	std::vector<Move> moves;
	std::vector<bool> seen(static_cast<std::size_t>(boardSize * boardSize), false);
	bool hasStone = false;
	for (int y = 0; y < boardSize; ++y) {
		for (int x = 0; x < boardSize; ++x) {
			if (board.at(x, y) != Board::Cell::Empty) {
				hasStone = true;
				for (int dy = -1; dy <= 1; ++dy) {
					for (int dx = -1; dx <= 1; ++dx) {
						if (dx == 0 && dy == 0) {
							continue;
						}
						int nx = x + dx;
						int ny = y + dy;
						if (!board.inBounds(nx, ny)) {
							continue;
						}
						if (!board.isEmpty(nx, ny)) {
							continue;
						}
						int idx = ny * boardSize + nx;
						if (!seen[static_cast<std::size_t>(idx)]) {
							seen[static_cast<std::size_t>(idx)] = true;
							moves.emplace_back(nx, ny);
						}
					}
				}
			}
		}
	}
	if (!hasStone) {
		int center = boardSize / 2;
		moves.emplace_back(center, center);
	}
	return moves;
}

std::vector<Move> orderCandidates(const GameState& state,
	const MinimaxContext& ctx,
	GameState::PlayerColor currentPlayer,
	bool maximizing,
	std::size_t maxCandidates,
	const Move* pvMove) {
	std::vector<Move> moves = collectCandidateMoves(state.board, ctx.settings.boardSize);
	AIScoreSettings evalSettings = ctx.settings;
	evalSettings.player = currentPlayer;
	std::vector<std::pair<double, Move>> scored;
	scored.reserve(moves.size());
	for (const Move& move : moves) {
		double score = heuristicForMove(state, ctx.rules, evalSettings, move);
		scored.emplace_back(score, move);
	}
	std::stable_sort(scored.begin(), scored.end(),
		[maximizing](const auto& a, const auto& b) {
			return maximizing ? (a.first > b.first) : (a.first < b.first);
		});
	if (pvMove) {
		for (std::size_t i = 0; i < scored.size(); ++i) {
			if (scored[i].second == *pvMove) {
				auto pvEntry = scored[i];
				scored.erase(scored.begin() + static_cast<std::ptrdiff_t>(i));
				scored.insert(scored.begin(), pvEntry);
				break;
			}
		}
	}
	if (maxCandidates > 0 && scored.size() > maxCandidates) {
		scored.resize(maxCandidates);
	}
	moves.clear();
	moves.reserve(scored.size());
	for (const auto& entry : scored) {
		moves.push_back(entry.second);
	}
	return moves;
}

bool hasStoneWithin(const Board& board, int boardSize) {
	for (int y = 0; y < boardSize; ++y) {
		for (int x = 0; x < boardSize; ++x) {
			if (board.at(x, y) != Board::Cell::Empty) {
				return true;
			}
		}
	}
	return false;
}

bool isBlockedEnd(const Board& board, int x, int y, int dx, int dy, int distance) {
	int bx = x + (distance + 1) * dx;
	int by = y + (distance + 1) * dy;
	if (!board.inBounds(bx, by)) {
		return true;
	}
	return board.at(bx, by) != Board::Cell::Empty;
}

double heuristicForMove(const GameState& state, const Rules& rules, const AIScoreSettings& settings, const Move& move) {
	if (!rules.isLegal(state, move, settings.player)) {
		return kIllegalScore;
	}
	const Board& board = state.board;
	Board::Cell selfCell = playerCell(settings.player);
	Board::Cell opponentCell = playerCell(otherPlayer(settings.player));
	double score = 0.0;
	const int size = settings.boardSize;
	int minEdgeDist = std::min({move.x, move.y, size - 1 - move.x, size - 1 - move.y});
	const int edgeMargin = 2;
	if (minEdgeDist < edgeMargin) {
		score -= static_cast<double>((edgeMargin - minEdgeDist) * 2);
	}

	const int directions[4][2] = { {1, 0}, {0, 1}, {1, 1}, {1, -1} };
	bool addsWin = false;
	for (int i = 0; i < 4; ++i) {
		int dx = directions[i][0];
		int dy = directions[i][1];
		int left = countDirection(board, move.x, move.y, -dx, -dy, selfCell, settings.boardSize);
		int right = countDirection(board, move.x, move.y, dx, dy, selfCell, settings.boardSize);
		int length = 1 + left + right;
		if (left + right > 0) {
			score += static_cast<double>(length);
		}
		if (length >= rules.getWinLength()) {
			addsWin = true;
		}

		int oppLeft = countDirection(board, move.x, move.y, -dx, -dy, opponentCell, settings.boardSize);
		if (oppLeft > 0) {
			score += static_cast<double>(oppLeft);
			if (isBlockedEnd(board, move.x, move.y, -dx, -dy, oppLeft)) {
				score += 5.0;
			}
		}
		int oppRight = countDirection(board, move.x, move.y, dx, dy, opponentCell, settings.boardSize);
		if (oppRight > 0) {
			score += static_cast<double>(oppRight);
			if (isBlockedEnd(board, move.x, move.y, dx, dy, oppRight)) {
				score += 5.0;
			}
		}
	}

	if (addsWin) {
		score += 100.0;
	}

	std::vector<Move> captures = rules.findCaptures(board, move, selfCell);
	if (!captures.empty()) {
		int pairs = static_cast<int>(captures.size()) / 2;
		score += 10.0 * static_cast<double>(pairs);
		int currentCaptured = (settings.player == GameState::PlayerColor::Black)
			? state.capturedStonesBlack
			: state.capturedStonesWhite;
		if (currentCaptured + static_cast<int>(captures.size()) >= rules.getCaptureWinStones()) {
			score += 100.0;
		}
	}

	return score;
}

double evaluateStateHeuristic(const GameState& state, const Rules& rules, const AIScoreSettings& settings) {
	if (state.status == GameState::Status::Draw) {
		return 0.0;
	}
	if (state.status == GameState::Status::BlackWon) {
		return (settings.player == GameState::PlayerColor::Black) ? kWinScore : -kWinScore;
	}
	if (state.status == GameState::Status::WhiteWon) {
		return (settings.player == GameState::PlayerColor::White) ? kWinScore : -kWinScore;
	}

	double bestSelf = kIllegalScore;
	double bestOpp = kIllegalScore;
	AIScoreSettings oppSettings = settings;
	oppSettings.player = otherPlayer(settings.player);
	int size = settings.boardSize;
	for (int y = 0; y < size; ++y) {
		for (int x = 0; x < size; ++x) {
			Move move(x, y);
			double scoreSelf = heuristicForMove(state, rules, settings, move);
			if (scoreSelf > bestSelf) {
				bestSelf = scoreSelf;
			}
			double scoreOpp = heuristicForMove(state, rules, oppSettings, move);
			if (scoreOpp > bestOpp) {
				bestOpp = scoreOpp;
			}
		}
	}
	if (bestSelf == kIllegalScore) {
		bestSelf = 0.0;
	}
	if (bestOpp == kIllegalScore) {
		bestOpp = 0.0;
	}
	return bestSelf - bestOpp;
}

bool timedOut(const MinimaxContext& ctx) {
	if (ctx.settings.timeoutMs <= 0) {
		return false;
	}
	auto elapsed = std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - ctx.start).count();
	return elapsed >= ctx.settings.timeoutMs;
}

bool applyMove(GameState& state, const Rules& rules, const Move& move, GameState::PlayerColor player) {
	if (!rules.isLegal(state, move, player)) {
		return false;
	}
	Board::Cell cell = playerCell(player);
	state.board.set(move.x, move.y, cell);
	state.lastMove = move;
	state.hasLastMove = true;
	state.lastMessage.clear();

	std::vector<Move> captures = rules.findCaptures(state.board, move, cell);
	for (const Move& captured : captures) {
		state.board.remove(captured.x, captured.y);
	}
	if (!captures.empty()) {
		int capturedCount = static_cast<int>(captures.size());
		if (player == GameState::PlayerColor::Black) {
			state.capturedStonesBlack += capturedCount;
		} else {
			state.capturedStonesWhite += capturedCount;
		}
	}

	int totalCaptured = (player == GameState::PlayerColor::Black)
		? state.capturedStonesBlack
		: state.capturedStonesWhite;
	if (totalCaptured >= rules.getCaptureWinStones()) {
		state.status = (player == GameState::PlayerColor::Black) ? GameState::Status::BlackWon : GameState::Status::WhiteWon;
	} else if (rules.isWin(state.board, move)) {
		state.status = (player == GameState::PlayerColor::Black) ? GameState::Status::BlackWon : GameState::Status::WhiteWon;
	} else if (rules.isDraw(state.board)) {
		state.status = GameState::Status::Draw;
	} else {
		state.status = GameState::Status::Running;
	}

	state.toMove = otherPlayer(player);
	return true;
}

bool isImmediateWin(const GameState& state,
	const Rules& rules,
	const Move& move,
	GameState::PlayerColor player) {
	if (!rules.isLegal(state, move, player)) {
		return false;
	}
	GameState probe = state;
	Board::Cell cell = (player == GameState::PlayerColor::Black) ? Board::Cell::Black : Board::Cell::White;
	probe.board.set(move.x, move.y, cell);
	std::vector<Move> captures = rules.findCaptures(probe.board, move, cell);
	int capturedCount = static_cast<int>(captures.size());
	int totalCaptured = (player == GameState::PlayerColor::Black)
		? (state.capturedStonesBlack + capturedCount)
		: (state.capturedStonesWhite + capturedCount);
	if (totalCaptured >= rules.getCaptureWinStones()) {
		return true;
	}
	return rules.isWin(probe.board, move);
}

bool isImmediateWinCached(AISearchCache& cache,
	const GameState& state,
	const Rules& rules,
	const Move& move,
	GameState::PlayerColor player,
	int boardSize) {
	std::uint64_t boardHash = hashBoard(state.board, boardSize);
	AISearchCache::ImmediateWinKey key{boardHash, boardSize, state.capturedStonesBlack, state.capturedStonesWhite, state.status, player, move.x, move.y};
	auto it = cache.immediateWinMove.find(key);
	if (it != cache.immediateWinMove.end()) {
		return it->second;
	}
	bool result = isImmediateWin(state, rules, move, player);
	cache.immediateWinMove.emplace(key, result);
	return result;
}

bool hasImmediateWinCached(AISearchCache& cache,
	const GameState& state,
	const Rules& rules,
	GameState::PlayerColor player,
	int boardSize) {
	std::uint64_t boardHash = hashBoard(state.board, boardSize);
	AISearchCache::ImmediateWinStateKey key{boardHash, boardSize, state.capturedStonesBlack, state.capturedStonesWhite, state.status, player};
	auto it = cache.immediateWinState.find(key);
	if (it != cache.immediateWinState.end()) {
		return it->second;
	}
	std::vector<Move> candidates = collectCandidateMoves(state.board, boardSize);
	for (const Move& move : candidates) {
		if (isImmediateWin(state, rules, move, player)) {
			cache.immediateWinState.emplace(key, true);
			return true;
		}
	}
	cache.immediateWinState.emplace(key, false);
	return false;
}

double minimax(GameState state,
	const MinimaxContext& ctx,
	int depth,
	GameState::PlayerColor currentPlayer,
	int depthFromRoot,
	double alpha,
	double beta) {
	if (depth <= 0 || timedOut(ctx) || state.status != GameState::Status::Running) {
		return evaluateStateHeuristic(state, ctx.rules, ctx.settings);
	}

	std::uint64_t boardHash = hashBoard(state.board, ctx.settings.boardSize);
	TTKey ttKey{boardHash, depth, ctx.settings.boardSize,
		state.capturedStonesBlack, state.capturedStonesWhite, state.status, currentPlayer};
	AISearchCache& cache = selectCache(ctx);
	auto ttIt = cache.tt.find(ttKey);
	Move pvMove;
	const Move* pvPtr = nullptr;
	if (ttIt != cache.tt.end()) {
		if (ttIt->second.depthLeft >= depth) {
			return ttIt->second.value;
		}
		pvMove = ttIt->second.bestMove;
		pvPtr = &pvMove;
	}

	bool maximizing = (currentPlayer == ctx.settings.player);
	double best = maximizing ? -std::numeric_limits<double>::infinity() : std::numeric_limits<double>::infinity();
	std::vector<Move> candidates = orderCandidates(state, ctx, currentPlayer, maximizing, static_cast<std::size_t>(Config::kAiTopCandidates), pvPtr);
	Move bestMove;
	bool mustBlock = hasImmediateWinCached(cache, state, ctx.rules, otherPlayer(currentPlayer), ctx.settings.boardSize);
	for (const Move& move : candidates) {
		if (timedOut(ctx)) {
			break;
		}
		if (Config::kAiQuickWinExit && isImmediateWinCached(cache, state, ctx.rules, move, currentPlayer, ctx.settings.boardSize)) {
			double winScore = (currentPlayer == ctx.settings.player) ? kWinScore : -kWinScore;
			storeTtEntry(cache, ttKey, TTEntry{winScore, depth, move});
			return winScore;
		}
		if (mustBlock) {
			GameState blockState = state;
			if (!applyMove(blockState, ctx.rules, move, currentPlayer)) {
				continue;
			}
			if (hasImmediateWinCached(cache, blockState, ctx.rules, otherPlayer(currentPlayer), ctx.settings.boardSize)) {
				continue;
			}
		}
		double value = evaluateMoveWithCache(state, ctx, currentPlayer, move, depth, depthFromRoot, boardHash, nullptr, alpha, beta);
		if (maximizing) {
			if (value > best) {
				best = value;
				bestMove = move;
			}
			alpha = std::max(alpha, best);
		} else {
			if (value < best) {
				best = value;
				bestMove = move;
			}
			beta = std::min(beta, best);
		}
		if (beta <= alpha) {
			break;
		}
		if (timedOut(ctx)) {
			break;
		}
	}

	if (best == std::numeric_limits<double>::infinity() || best == -std::numeric_limits<double>::infinity()) {
		return 0.0;
	}
	storeTtEntry(cache, ttKey, TTEntry{best, depth, bestMove});
	return best;
}

double evaluateMoveWithCache(const GameState& state,
	const MinimaxContext& ctx,
	GameState::PlayerColor currentPlayer,
	const Move& move,
	int depthLeft,
	int depthFromRoot,
	std::uint64_t boardHash,
	bool* outCached,
	double alpha,
	double beta) {
	if (timedOut(ctx)) {
		return evaluateStateHeuristic(state, ctx.rules, ctx.settings);
	}
	AISearchCache& cache = selectCache(ctx);
	MoveCacheKey key{boardHash, depthLeft, ctx.settings.boardSize,
		state.capturedStonesBlack, state.capturedStonesWhite, state.status, currentPlayer, move.x, move.y};
	auto it = cache.moveCache.find(key);
	if (it != cache.moveCache.end()) {
		double cachedScore = it->second;
		if (outCached) {
			*outCached = true;
		}
		return cachedScore;
	}

	double score = kIllegalScore;
	if (ctx.rules.isLegal(state, move, currentPlayer)) {
		GameState next = state;
		if (applyMove(next, ctx.rules, move, currentPlayer)) {
			AISearchCache::StateKey parentKey = makeStateKey(state, ctx.settings.boardSize, currentPlayer);
			AISearchCache::StateKey childKey = makeStateKey(next, ctx.settings.boardSize, next.toMove);
			addEdge(cache, parentKey, childKey);
			if (ctx.settings.onGhostUpdate) {
				ctx.settings.onGhostUpdate(next);
			}
			if (depthLeft <= 1 || timedOut(ctx)) {
				score = evaluateStateHeuristic(next, ctx.rules, ctx.settings);
			} else {
				score = minimax(next, ctx, depthLeft - 1, otherPlayer(currentPlayer), depthFromRoot + 1, alpha, beta);
			}
		}
	}
	cache.moveCache.emplace(key, score);
	if (outCached) {
		*outCached = false;
	}
	return score;
}

std::vector<double> scoreBoardAtDepth(const GameState& state,
	const AIScoreSettings& settings,
	const MinimaxContext& ctx,
	int depth,
	bool* outUsedCache) {
	bool usedCache = false;
	std::vector<double> scores(static_cast<std::size_t>(settings.boardSize * settings.boardSize), kIllegalScore);
	std::uint64_t boardHash = hashBoard(state.board, settings.boardSize);
	TTKey ttKey{boardHash, depth, settings.boardSize,
		state.capturedStonesBlack, state.capturedStonesWhite, state.status, settings.player};
	AISearchCache& cache = selectCache(ctx);
	auto ttIt = cache.tt.find(ttKey);
	Move pvMove;
	const Move* pvPtr = nullptr;
	if (ttIt != cache.tt.end()) {
		pvMove = ttIt->second.bestMove;
		pvPtr = &pvMove;
	}
	std::vector<Move> candidates = orderCandidates(state, ctx, settings.player, true,
		static_cast<std::size_t>(Config::kAiTopCandidates), pvPtr);
	bool mustBlock = hasImmediateWinCached(cache, state, ctx.rules, otherPlayer(settings.player), settings.boardSize);
	for (const Move& move : candidates) {
		if (timedOut(ctx)) {
			break;
		}
		if (Config::kAiQuickWinExit && isImmediateWinCached(cache, state, ctx.rules, move, settings.player, settings.boardSize)) {
			double winScore = (settings.player == ctx.settings.player) ? kWinScore : -kWinScore;
			scores[static_cast<std::size_t>(move.y * settings.boardSize + move.x)] = winScore;
			if (outUsedCache) {
				*outUsedCache = usedCache;
			}
			return scores;
		}
		if (mustBlock) {
			GameState blockState = state;
			if (!applyMove(blockState, ctx.rules, move, settings.player)) {
				continue;
			}
			if (hasImmediateWinCached(cache, blockState, ctx.rules, otherPlayer(settings.player), settings.boardSize)) {
				continue;
			}
		}
		std::size_t idx = static_cast<std::size_t>(move.y * settings.boardSize + move.x);
		bool cached = false;
		double score = evaluateMoveWithCache(state, ctx, settings.player, move, depth, depth, boardHash, &cached,
			-std::numeric_limits<double>::infinity(),
			std::numeric_limits<double>::infinity());
		if (cached) {
			usedCache = true;
		}
		scores[idx] = score;
	}
	if (outUsedCache) {
		*outUsedCache = usedCache;
	}
	return scores;
}
}  // namespace

std::vector<double> AIScoring::scoreBoard(const GameState& state, const Rules& rules, const AIScoreSettings& settingsIn) {
	AIScoreSettings settings = settingsIn;
	if (settings.boardSize <= 0) {
		settings.boardSize = state.board.getSize();
	}
	settings.boardSize = std::min(settings.boardSize, state.board.getSize());
	if (settings.depth < 1) {
		settings.depth = 1;
	}

	MinimaxContext ctx{rules, settings, std::chrono::steady_clock::now()};
	if (!hasStoneWithin(state.board, settings.boardSize)) {
		std::vector<double> scores(static_cast<std::size_t>(settings.boardSize * settings.boardSize), kIllegalScore);
		int center = settings.boardSize / 2;
		const Move centerMove(center, center);
		scores[static_cast<std::size_t>(centerMove.y * settings.boardSize + centerMove.x)] = 0.0;
		return scores;
	}
	std::vector<Move> initialCandidates = collectCandidateMoves(state.board, settings.boardSize);
	if (initialCandidates.empty()) {
		std::vector<double> scores(static_cast<std::size_t>(settings.boardSize * settings.boardSize), kIllegalScore);
		int center = settings.boardSize / 2;
		const Move centerMove(center, center);
		scores[static_cast<std::size_t>(centerMove.y * settings.boardSize + centerMove.x)] = 0.0;
		return scores;
	}
	std::vector<double> scores;
	AISearchCache& cache = selectCache(ctx);
	std::uint64_t boardHash = hashBoard(state.board, settings.boardSize);
	for (int depth = 1; depth <= settings.depth; ++depth) {
		if (timedOut(ctx)) {
			break;
		}
		if (Config::kAiQuickWinExit) {
			for (const Move& move : initialCandidates) {
				if (isImmediateWinCached(cache, state, rules, move, settings.player, settings.boardSize)) {
					std::vector<double> winScores(static_cast<std::size_t>(settings.boardSize * settings.boardSize), kIllegalScore);
					winScores[static_cast<std::size_t>(move.y * settings.boardSize + move.x)] = kWinScore;
					return winScores;
				}
			}
		}
		CacheKey key{boardHash, depth, settings.boardSize, settings.player};
		auto it = g_depthCache.find(key);
		bool cached = (it != g_depthCache.end());
		if (!cached) {
			bool usedCache = false;
			scores = scoreBoardAtDepth(state, settings, ctx, depth, &usedCache);
			g_depthCache.emplace(key, scores);
			cached = usedCache;
		} else {
			scores = it->second;
		}
		if (Config::kLogDepthScores) {
			for (const Move& move : initialCandidates) {
				double score = scores[static_cast<std::size_t>(move.y * settings.boardSize + move.x)];
				std::cout << "[DEPTH " << depth << "] [" << move.x << "," << move.y << "] score is " << score << std::endl;
			}
		}
		double bestScore = -std::numeric_limits<double>::infinity();
		int bestX = -1;
		int bestY = -1;
		for (int y = 0; y < settings.boardSize; ++y) {
			for (int x = 0; x < settings.boardSize; ++x) {
				double score = scores[static_cast<std::size_t>(y * settings.boardSize + x)];
				if (score > bestScore) {
					bestScore = score;
					bestX = x;
					bestY = y;
				}
			}
		}
		if (bestX >= 0 && bestY >= 0) {
			std::cout << "Best move with depth " << depth << " is [" << bestX << "," << bestY << "] with "
			          << bestScore << " score" << std::endl;
		}
	}

	return scores;
}

std::size_t AIScoring::transpositionSize(const AISearchCache& cache) {
	return cache.ttSize;
}

void AIScoring::rerootCache(AISearchCache& cache, const GameState& state) {
	const int boardSize = state.board.getSize();
	cache.root = makeStateKey(state, boardSize, state.toMove);
	cache.hasRoot = true;

	std::unordered_set<AISearchCache::StateKey, AISearchCache::StateKeyHash> reachable;
	std::vector<AISearchCache::StateKey> stack;
	stack.push_back(cache.root);
	while (!stack.empty()) {
		AISearchCache::StateKey key = stack.back();
		stack.pop_back();
		if (reachable.find(key) != reachable.end()) {
			continue;
		}
		reachable.insert(key);
		auto it = cache.edges.find(key);
		if (it == cache.edges.end()) {
			continue;
		}
		for (const auto& child : it->second) {
			stack.push_back(child);
		}
	}

	auto stateFromTt = [](const AISearchCache::TTKey& key) {
		return AISearchCache::StateKey{
			key.hash,
			key.boardSize,
			key.capturedBlack,
			key.capturedWhite,
			key.status,
			key.currentPlayer
		};
	};
	auto stateFromMove = [](const AISearchCache::MoveCacheKey& key) {
		return AISearchCache::StateKey{
			key.hash,
			key.boardSize,
			key.capturedBlack,
			key.capturedWhite,
			key.status,
			key.currentPlayer
		};
	};
	auto stateFromImmediateMove = [](const AISearchCache::ImmediateWinKey& key) {
		return AISearchCache::StateKey{
			key.hash,
			key.boardSize,
			key.capturedBlack,
			key.capturedWhite,
			key.status,
			key.player
		};
	};
	auto stateFromImmediateState = [](const AISearchCache::ImmediateWinStateKey& key) {
		return AISearchCache::StateKey{
			key.hash,
			key.boardSize,
			key.capturedBlack,
			key.capturedWhite,
			key.status,
			key.player
		};
	};

	for (auto it = cache.tt.begin(); it != cache.tt.end();) {
		if (reachable.find(stateFromTt(it->first)) == reachable.end()) {
			it = cache.tt.erase(it);
		} else {
			++it;
		}
	}
	cache.ttSize = cache.tt.size();

	for (auto it = cache.moveCache.begin(); it != cache.moveCache.end();) {
		if (reachable.find(stateFromMove(it->first)) == reachable.end()) {
			it = cache.moveCache.erase(it);
		} else {
			++it;
		}
	}
	for (auto it = cache.immediateWinMove.begin(); it != cache.immediateWinMove.end();) {
		if (reachable.find(stateFromImmediateMove(it->first)) == reachable.end()) {
			it = cache.immediateWinMove.erase(it);
		} else {
			++it;
		}
	}
	for (auto it = cache.immediateWinState.begin(); it != cache.immediateWinState.end();) {
		if (reachable.find(stateFromImmediateState(it->first)) == reachable.end()) {
			it = cache.immediateWinState.erase(it);
		} else {
			++it;
		}
	}
	for (auto it = cache.edges.begin(); it != cache.edges.end();) {
		if (reachable.find(it->first) == reachable.end()) {
			it = cache.edges.erase(it);
		} else {
			auto& children = it->second;
			children.erase(std::remove_if(children.begin(), children.end(),
				[&reachable](const AISearchCache::StateKey& key) {
					return reachable.find(key) == reachable.end();
				}), children.end());
			++it;
		}
	}
}
