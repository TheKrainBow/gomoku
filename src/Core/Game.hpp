#ifndef GAME_HPP
#define GAME_HPP

#include <memory>
#include <chrono>

#include "AIPlayer.hpp"
#include "GameSettings.hpp"
#include "GameState.hpp"
#include "HumanPlayer.hpp"
#include "IPlayer.hpp"
#include "MoveHistory.hpp"
#include "Rules.hpp"

class Game {
public:
	explicit Game(const GameSettings& settings);

	void reset(const GameSettings& settings);
	const GameState& getState() const;
	const MoveHistory& getHistory() const;
	bool tryApplyMove(const Move& move);
	void tick();
	bool submitHumanMove(const Move& move);
	bool hasGhostBoard() const;
	Board getGhostBoard() const;

private:
	GameSettings settings;
	Rules rules;
	GameState state;
	MoveHistory history;
	std::unique_ptr<IPlayer> blackPlayer;
	std::unique_ptr<IPlayer> whitePlayer;
	std::chrono::steady_clock::time_point turnStartTime;
	int coordWidth;
	int captureWidth;
	size_t timeWidth;

	IPlayer* currentPlayer();
	IPlayer* playerForColor(GameState::PlayerColor color);
	void createPlayers();
	void logMatchup() const;
	void logMovePlayed(const Move& move, double elapsedMs, bool isAiMove, int totalCaptured, int capturedDelta) const;
	void logWin(GameState::PlayerColor player, const std::string& reason) const;
	void computeLogWidths();
};

#endif
