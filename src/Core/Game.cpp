#include "Game.hpp"

#include <algorithm>
#include <iomanip>
#include <iostream>
#include <sstream>

Game::Game(const GameSettings& settingsIn)
	: settings(settingsIn),
	  rules(settingsIn),
	  state(),
	  history(),
	  blackPlayer(),
	  whitePlayer(),
	  coordWidth(1),
	  captureWidth(1),
	  timeWidth(0) {
	reset(settingsIn);
}

void Game::reset(const GameSettings& settingsIn) {
	settings = settingsIn;
	rules = Rules(settingsIn);
	state.reset(settingsIn);
	history.clear();
	createPlayers();
	computeLogWidths();
	turnStartTime = std::chrono::steady_clock::now();
	logMatchup();
}

const GameState& Game::getState() const {
	return state;
}

const MoveHistory& Game::getHistory() const {
	return history;
}

bool Game::tryApplyMove(const Move& move) {
	if (state.status != GameState::Status::Running) {
		return false;
	}
	IPlayer* player = currentPlayer();
	bool isAiMove = (player && !player->isHuman());
	std::string reason;
	if (!rules.isLegal(state, move, &reason)) {
		state.lastMessage = "Illegal move: " + reason;
		std::cout << state.lastMessage << std::endl;
		return false;
	}
	state.lastMessage.clear();
	double elapsedMs = std::chrono::duration<double, std::milli>(std::chrono::steady_clock::now() - turnStartTime).count();
	Board::Cell cell = (state.toMove == GameState::PlayerColor::Black) ? Board::Cell::Black : Board::Cell::White;
	state.board.set(move.x, move.y, cell);
	state.lastMove = move;
	state.hasLastMove = true;
	MoveHistory::HistoryEntry entry;
	entry.move = move;
	entry.player = state.toMove;
	entry.capturedPositions = rules.findCaptures(state.board, move, cell);
	for (const Move& captured : entry.capturedPositions) {
		state.board.remove(captured.x, captured.y);
	}
	int capturedCount = 0;
	if (!entry.capturedPositions.empty()) {
		capturedCount = static_cast<int>(entry.capturedPositions.size());
		if (state.toMove == GameState::PlayerColor::Black) {
			state.capturedStonesBlack += capturedCount;
		} else {
			state.capturedStonesWhite += capturedCount;
		}
	}
	int totalCaptured = (state.toMove == GameState::PlayerColor::Black) ? state.capturedStonesBlack : state.capturedStonesWhite;
	logMovePlayed(move, elapsedMs, isAiMove, totalCaptured, capturedCount);
	history.push(entry);

	int captureCount = (state.toMove == GameState::PlayerColor::Black) ? state.capturedStonesBlack : state.capturedStonesWhite;
	if (captureCount >= settings.captureWinStones) {
		logWin(state.toMove, "capture");
		state.status = (state.toMove == GameState::PlayerColor::Black) ? GameState::Status::BlackWon : GameState::Status::WhiteWon;
		state.winningLine.clear();
		return true;
	}

	if (rules.isWin(state.board, move)) {
		GameState::PlayerColor opponent = (state.toMove == GameState::PlayerColor::Black)
			? GameState::PlayerColor::White
			: GameState::PlayerColor::Black;
		if (!rules.opponentCanBreakAlignmentByCapture(state, opponent)) {
			rules.findAlignmentLine(state.board, move, state.winningLine);
			logWin(state.toMove, "alignment");
			state.status = (state.toMove == GameState::PlayerColor::Black) ? GameState::Status::BlackWon : GameState::Status::WhiteWon;
			return true;
		} else {
			std::cout << "\033[33mAlignment formed but can be broken by capture.\033[0m" << std::endl;
		}
	}
	if (rules.isDraw(state.board)) {
		std::cout << "\033[36mGame ends in a draw.\033[0m" << std::endl;
		state.status = GameState::Status::Draw;
		return true;
	}

	state.toMove = (state.toMove == GameState::PlayerColor::Black) ? GameState::PlayerColor::White : GameState::PlayerColor::Black;
	turnStartTime = std::chrono::steady_clock::now();
	return true;
}

void Game::tick() {
	if (state.status != GameState::Status::Running) {
		return;
	}
	IPlayer* player = currentPlayer();
	if (!player) {
		return;
	}
	if (player->isHuman()) {
		HumanPlayer* human = dynamic_cast<HumanPlayer*>(player);
		if (human && human->hasPendingMove()) {
			Move move = human->takePendingMove();
			tryApplyMove(move);
		}
	} else {
		Move move = player->chooseMove(state, rules);
		tryApplyMove(move);
	}
}

bool Game::submitHumanMove(const Move& move) {
	IPlayer* player = currentPlayer();
	if (!player || !player->isHuman()) {
		return false;
	}
	HumanPlayer* human = dynamic_cast<HumanPlayer*>(player);
	if (!human) {
		return false;
	}
	human->setPendingMove(move);
	return true;
}

IPlayer* Game::currentPlayer() {
	return playerForColor(state.toMove);
}

IPlayer* Game::playerForColor(GameState::PlayerColor color) {
	return (color == GameState::PlayerColor::Black) ? blackPlayer.get() : whitePlayer.get();
}

void Game::createPlayers() {
	if (settings.blackType == GameSettings::PlayerType::Human) {
		blackPlayer = std::make_unique<HumanPlayer>();
	} else {
		blackPlayer = std::make_unique<AIPlayer>(settings.aiMoveDelayMs);
	}

	if (settings.whiteType == GameSettings::PlayerType::Human) {
		whitePlayer = std::make_unique<HumanPlayer>();
	} else {
		whitePlayer = std::make_unique<AIPlayer>(settings.aiMoveDelayMs);
	}
}

void Game::logMatchup() const {
	auto typeLabel = [](GameSettings::PlayerType type) {
		return (type == GameSettings::PlayerType::AI) ? "AI" : "Human";
	};
	std::cout << "\033[90mWhite (" << typeLabel(settings.whiteType) << ") vs Black (" << typeLabel(settings.blackType) << ")\033[0m" << std::endl;
}

void Game::logMovePlayed(const Move& move, double elapsedMs, bool isAiMove, int totalCaptured, int capturedDelta) const {
	auto colorTag = [](GameState::PlayerColor player) {
		return (player == GameState::PlayerColor::Black) ? "\033[90m[BLACK]\033[0m" : "\033[97m[WHITE]\033[0m";
	};
	auto timeColor = [](double ms) {
		if (ms > 500.0) {
			return "\033[31m";
		}
		if (ms > 400.0) {
			return "\033[33m";
		}
		return "\033[32m";
	};
	auto formatTime = [](double ms) {
		std::ostringstream out;
		out << std::fixed << std::setprecision(4);
		if (ms >= 1000.0) {
			out << (ms / 1000.0) << "s";
		} else {
			out << ms << "ms";
		}
		return out.str();
	};
	auto padRight = [](const std::string& value, size_t width) {
		if (value.size() >= width) {
			return value;
		}
		return value + std::string(width - value.size(), ' ');
	};
	const char* timeStyle = isAiMove ? timeColor(elapsedMs) : "\033[37m";
	std::ostringstream line;
	std::ostringstream coord;
	coord << std::setw(coordWidth) << std::setfill(' ') << move.x << "," << std::setw(coordWidth) << std::setfill(' ') << move.y;
	std::string timeText = padRight(formatTime(elapsedMs), timeWidth);
	line << colorTag(state.toMove) << " played at [" << coord.str() << "] in "
		 << timeStyle << timeText << "\033[0m"
		 << " | \033[36m[" << std::setw(captureWidth) << std::setfill(' ') << totalCaptured
		 << "/" << std::setw(captureWidth) << std::setfill(' ') << settings.captureWinStones << "]\033[0m";
	if (capturedDelta > 0) {
		line << " \033[32m+" << capturedDelta << "!\033[0m";
	}
	std::cout << line.str() << std::endl;
}

void Game::logWin(GameState::PlayerColor player, const std::string& reason) const {
	const char* colorTag = (player == GameState::PlayerColor::Black) ? "\033[37m[BLACK]\033[0m" : "\033[97m[WHITE]\033[0m";
	if (reason == "capture") {
		std::cout << colorTag << " \033[35mwins by capture\033[0m (" << settings.captureWinStones << "/" << settings.captureWinStones << ")." << std::endl;
	} else if (reason == "alignment") {
		std::cout << colorTag << " \033[35mwins by alignment\033[0m." << std::endl;
	} else {
		std::cout << colorTag << " \033[35mwins\033[0m." << std::endl;
	}
}

void Game::computeLogWidths() {
	auto digits = [](int value) {
		int width = 1;
		while (value >= 10) {
			value /= 10;
			++width;
		}
		return width;
	};
	int maxCoord = std::max(0, settings.boardSize - 1);
	coordWidth = digits(maxCoord);
	captureWidth = digits(settings.captureWinStones);
	auto formatTime = [](double ms) {
		std::ostringstream out;
		out << std::fixed << std::setprecision(4);
		if (ms >= 1000.0) {
			out << (ms / 1000.0) << "s";
		} else {
			out << ms << "ms";
		}
		return out.str();
	};
	timeWidth = 0;
	timeWidth = std::max(timeWidth, formatTime(0.0).size());
	timeWidth = std::max(timeWidth, formatTime(999.9999).size());
	timeWidth = std::max(timeWidth, formatTime(1000.0).size());
	timeWidth = std::max(timeWidth, formatTime(9999.9999).size());
}
