#include "AIPlayer.hpp"

#include <chrono>
#include <limits>

#include "AIScoring.hpp"
#include "Config.hpp"

AIPlayer::AIPlayer(int moveDelayMs)
	: delayMs(moveDelayMs),
	  thinking(false),
	  moveReady(false),
	  ghostActive(false),
	  readyMove(),
	  ghostBoard() {
}

AIPlayer::~AIPlayer() {
	if (worker.joinable()) {
		worker.join();
	}
}

bool AIPlayer::isHuman() const {
	return false;
}

Move AIPlayer::chooseMove(const GameState& state, const Rules& rules) {
	int delay = (Config::kAiMoveDelayMs > 0) ? Config::kAiMoveDelayMs : delayMs;
	if (delay > 0) {
		std::this_thread::sleep_for(std::chrono::milliseconds(delay));
	}
	AIScoreSettings settings;
	settings.depth = Config::kAiDepth;
	settings.timeoutMs = Config::kAiTimeoutMs;
	settings.boardSize = state.board.getSize();
	settings.player = state.toMove;
	settings.cache = &cache;
	std::lock_guard<std::mutex> cacheLock(cacheMutex);
	std::vector<double> scores = AIScoring::scoreBoard(state, rules, settings);
	int size = settings.boardSize;
	double bestScore = -std::numeric_limits<double>::infinity();
	Move bestMove;
	for (int y = 0; y < size; ++y) {
		for (int x = 0; x < size; ++x) {
			Move move(x, y);
			double score = scores[static_cast<std::size_t>(y * size + x)];
			if (score > bestScore && rules.isLegal(state, move)) {
				bestScore = score;
				bestMove = move;
			}
		}
	}
	if (bestScore != -std::numeric_limits<double>::infinity()) {
		return bestMove;
	}
	return Move();
}

void AIPlayer::startThinking(const GameState& state, const Rules& rules) {
	if (thinking.load()) {
		return;
	}
	if (worker.joinable()) {
		worker.join();
	}
	thinking.store(true);
	moveReady.store(false);
	ghostActive.store(false);
	GameState stateCopy = state;
	Rules rulesCopy = rules;
	worker = std::thread([this, stateCopy, rulesCopy]() mutable {
		int delay = (Config::kAiMoveDelayMs > 0) ? Config::kAiMoveDelayMs : delayMs;
		if (delay > 0) {
			std::this_thread::sleep_for(std::chrono::milliseconds(delay));
		}
		AIScoreSettings settings;
		settings.depth = Config::kAiDepth;
		settings.timeoutMs = Config::kAiTimeoutMs;
		settings.boardSize = stateCopy.board.getSize();
		settings.player = stateCopy.toMove;
		settings.cache = &cache;
		if (Config::kGhostMode) {
			settings.onGhostUpdate = [this](const GameState& ghostState) {
				std::lock_guard<std::mutex> lock(ghostMutex);
				ghostBoard = ghostState.board;
				ghostActive.store(true);
			};
		}
		std::lock_guard<std::mutex> cacheLock(cacheMutex);
		std::vector<double> scores = AIScoring::scoreBoard(stateCopy, rulesCopy, settings);
		int size = settings.boardSize;
		double bestScore = -std::numeric_limits<double>::infinity();
		Move bestMove;
		for (int y = 0; y < size; ++y) {
			for (int x = 0; x < size; ++x) {
				Move move(x, y);
				double score = scores[static_cast<std::size_t>(y * size + x)];
				if (score > bestScore && rulesCopy.isLegal(stateCopy, move)) {
					bestScore = score;
					bestMove = move;
				}
			}
		}
		{
			std::lock_guard<std::mutex> lock(moveMutex);
			readyMove = bestMove;
		}
		moveReady.store(true);
		ghostActive.store(false);
		thinking.store(false);
	});
}

bool AIPlayer::isThinking() const {
	return thinking.load();
}

bool AIPlayer::hasMoveReady() const {
	return moveReady.load();
}

Move AIPlayer::takeMove() {
	std::lock_guard<std::mutex> lock(moveMutex);
	moveReady.store(false);
	return readyMove;
}

bool AIPlayer::hasGhostBoard() const {
	return ghostActive.load();
}

Board AIPlayer::ghostBoardCopy() const {
	std::lock_guard<std::mutex> lock(ghostMutex);
	return ghostBoard;
}

void AIPlayer::onMoveApplied(const GameState& state) {
	std::lock_guard<std::mutex> lock(cacheMutex);
	AIScoring::rerootCache(cache, state);
}

std::size_t AIPlayer::cacheSize() const {
	std::lock_guard<std::mutex> lock(cacheMutex);
	return AIScoring::transpositionSize(cache);
}
